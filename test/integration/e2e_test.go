package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"testing"
	"time"

	portpilotclient "github.com/portpilot/portpilot/internal/client"
	"github.com/portpilot/portpilot/internal/config"
	portpilotserver "github.com/portpilot/portpilot/internal/server"
	"go.uber.org/zap"
)

const (
	integrationToken = "01234567890123456789012345678901234567890123"
	adminHash        = "$2y$10$/io15KIC2pA.bXQi6J5G0OiLm0T.R.raVU/tokVwDGJolAgSBN2g2"
)

func TestEndToEndTCPDirectUDPAndReconnect(t *testing.T) {
	t.Parallel()
	logger := zap.NewNop()
	dir := t.TempDir()
	tcpEcho, closeTCPEcho := startTCPEcho(t)
	defer closeTCPEcho()
	udpEcho, closeUDPEcho := startUDPEcho(t)
	defer closeUDPEcho()

	adminAddress := freeTCPAddress(t)
	dataAddress := freeTCPAddress(t)
	tcpProxy := freeTCPAddress(t)
	directProxy := freeTCPAddress(t)
	udpProxy := freeUDPAddress(t)
	clientsPath := filepath.Join(dir, "clients.json")
	serverPath := filepath.Join(dir, "server.json")
	clientPath := filepath.Join(dir, "client.json")

	clientFile := config.ClientFile{
		Revision: 1,
		ClientConfig: []config.ClientConfig{{
			ClientID: "integration-client", ClientToken: integrationToken, Enabled: true,
			ProxyList: []config.ProxyEnvelope{
				proxy(t, "tcp-smux", "tcp", "auto", tcpProxy, tcpEcho),
				proxy(t, "tcp-direct", "tcp", "direct", directProxy, tcpEcho),
				proxy(t, "udp-smux", "udp", "smux-datagram", udpProxy, udpEcho),
			},
		}},
	}
	writeJSON(t, clientsPath, clientFile)
	serverConfig := config.DefaultServerConfig()
	serverConfig.AdminAddr = adminAddress
	serverConfig.ServerAddr = dataAddress
	serverConfig.Users = []config.User{{UserName: "admin", PasswordHash: adminHash}}
	serverConfig.ClientConfigFile = clientsPath
	serverConfig.CacheDir = filepath.Join(dir, "cache")
	serverConfig.DataPlane.SMux.MinSessions = 1
	serverConfig.DataPlane.SMux.MaxSessions = 2
	serverConfig.DataPlane.SMux.ScaleUpStreams = 2
	serverConfig.DataPlane.SMux.DrainTimeout = "500ms"
	serverConfig.DataPlane.UDP.ChannelsPerProxy = 2
	serverConfig.DataPlane.UDP.FlowIdleTimeout = "1s"
	serverConfig.Timeouts.HeartbeatInterval = "100ms"
	serverConfig.Timeouts.HeartbeatTimeout = "1s"
	serverConfig.Timeouts.DataConnect = "2s"
	writeJSON(t, serverPath, serverConfig)
	writeJSON(t, clientPath, config.ClientRuntimeConfig{
		Server: dataAddress, EnableTLS: false, Path: "/integration",
		ClientID: "integration-client", ClientToken: integrationToken,
	})

	serverApp, err := portpilotserver.New(serverPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancelServer := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() { serverDone <- serverApp.Run(serverCtx) }()
	t.Cleanup(func() {
		cancelServer()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	waitHTTP(t, "http://"+adminAddress+"/health/ready")

	adminClient := loginAdmin(t, "http://"+adminAddress)
	clientCancel, clientDone := startClient(t, clientPath, logger)
	firstEpoch := waitClientState(t, adminClient, "http://"+adminAddress, "ACTIVE", 0)

	assertTCPEcho(t, tcpProxy, "smux")
	assertTCPEcho(t, directProxy, "direct")
	assertUDPEcho(t, udpProxy, "udp-one")
	assertUDPEcho(t, udpProxy, "udp-two")

	clientCancel()
	waitDone(t, clientDone, "first client")
	waitClientState(t, adminClient, "http://"+adminAddress, "OFFLINE", firstEpoch)
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", tcpProxy)
	if err != nil {
		t.Fatalf("listener disappeared while client was offline: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	_ = conn.Close()
	if err == nil {
		t.Fatal("offline proxy did not fail the new connection")
	}

	secondCancel, secondDone := startClient(t, clientPath, logger)
	secondEpoch := waitClientState(t, adminClient, "http://"+adminAddress, "ACTIVE", firstEpoch)
	if secondEpoch <= firstEpoch {
		t.Fatalf("session epoch did not increase: first=%d second=%d", firstEpoch, secondEpoch)
	}
	assertTCPEcho(t, tcpProxy, "after-reconnect")
	assertUDPEcho(t, udpProxy, "after-reconnect")
	secondCancel()
	waitDone(t, secondDone, "second client")
}

func startClient(t *testing.T, path string, logger *zap.Logger) (context.CancelFunc, <-chan error) {
	t.Helper()
	app, err := portpilotclient.New(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	return cancel, done
}

func waitDone(t *testing.T, done <-chan error, name string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s stopped with error: %v", name, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not stop", name)
	}
}

func loginAdmin(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 2 * time.Second, Jar: jar}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/auth/login", bytes.NewBufferString(`{"user_name":"admin","password":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin login returned %s", response.Status)
	}
	return client
}

func waitClientState(t *testing.T, client *http.Client, base, state string, afterEpoch uint64) uint64 {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/api/clients", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			var payload struct {
				Clients []struct {
					ClientID    string `json:"client_id"`
					State       string `json:"state"`
					Epoch       uint64 `json:"epoch"`
					MuxSessions int    `json:"mux_sessions"`
				} `json:"clients"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if decodeErr == nil {
				for _, item := range payload.Clients {
					if item.ClientID == "integration-client" && item.State == state && item.Epoch >= afterEpoch {
						if state != "ACTIVE" || item.MuxSessions > 0 {
							return item.Epoch
						}
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("client did not reach state %s after epoch %d", state, afterEpoch)
	return 0
}

func assertTCPEcho(t *testing.T, address, label string) {
	t.Helper()
	payload := []byte(label + "-" + time.Now().String())
	conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(t.Context(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, response) {
		t.Fatalf("TCP echo mismatch: %q != %q", response, payload)
	}
}

func assertUDPEcho(t *testing.T, address, label string) {
	t.Helper()
	target, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	payload := []byte(label + "-" + time.Now().String())
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.WriteToUDP(payload, target); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 65507)
	n, _, err := conn.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, buffer[:n]) {
		t.Fatalf("UDP echo mismatch: %q != %q", buffer[:n], payload)
	}
}

func startTCPEcho(t *testing.T) (string, func()) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func startUDPEcho(t *testing.T) (string, func()) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 65507)
		for {
			n, address, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			if _, writeErr := conn.WriteToUDP(buffer[:n], address); writeErr != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String(), func() { _ = conn.Close() }
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func freeUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	address := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func proxy(t *testing.T, id, network, transport, bind, connect string) config.ProxyEnvelope {
	t.Helper()
	data, err := json.Marshal(config.TunnelData{
		Network: network, Transport: transport, Bind: bind, Connect: connect,
	})
	if err != nil {
		t.Fatal(err)
	}
	return config.ProxyEnvelope{
		ProxyID: id, Remark: id, Kind: "tunnel", Enabled: true, Data: data,
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitHTTP(t *testing.T, target string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("HTTP endpoint did not become ready: %s", target)
}
