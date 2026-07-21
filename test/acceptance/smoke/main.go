package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

type smokeClient struct {
	ClientID    string `json:"client_id"`
	State       string `json:"state"`
	MuxSessions int    `json:"mux_sessions"`
	AgentStatus string `json:"agent_status"`
	Agent       *struct {
		Version         string   `json:"version"`
		ProtocolVersion int      `json:"protocol_version"`
		Capabilities    []string `json:"capabilities"`
	} `json:"agent"`
	Session *struct {
		SourceIP    string `json:"source_ip"`
		ConnectedAt int64  `json:"connected_at"`
		LastSeenAt  int64  `json:"last_seen_at"`
	} `json:"session"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "smoke failed:", err)
		os.Exit(1)
	}
	fmt.Println("PortPilot smoke passed: API, Web, source policy, TCP smux/direct, PROXY v1, rate limit, and UDP-over-TCP")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminURL := env("PORTPILOT_DEV_ADMIN_URL", "http://127.0.0.1:5100")
	started := time.Now().Add(-time.Second)
	webURL := env("PORTPILOT_DEV_WEB_URL", "http://127.0.0.1:3000")
	if err := smokeHTTP(ctx, adminURL, webURL); err != nil {
		return err
	}
	if err := smokeTCP(ctx, "127.0.0.1:7000", "127.0.0.2", 512); err != nil {
		return fmt.Errorf("first smux TCP smoke: %w", err)
	}
	if err := smokeTCP(ctx, "127.0.0.1:7000", "127.0.0.2", 512); err != nil {
		return fmt.Errorf("second smux TCP smoke: %w", err)
	}
	if err := smokeTCPDenied(ctx, "127.0.0.1:7000", "127.0.0.9"); err != nil {
		return fmt.Errorf("TCP source policy smoke: %w", err)
	}
	if err := smokeTCP(ctx, "127.0.0.1:7002", "127.0.0.3", 32*1024); err != nil {
		return fmt.Errorf("direct TCP smoke: %w", err)
	}
	if err := smokeTCP(ctx, "127.0.0.1:7003", "127.0.0.4", 512); err != nil {
		return fmt.Errorf("PROXY protocol TCP smoke: %w", err)
	}
	if err := smokeTCPRate(ctx, "127.0.0.1:7004", 128*1024); err != nil {
		return fmt.Errorf("rate limited TCP smoke: %w", err)
	}
	if err := smokeUDP("127.0.0.1:7001"); err != nil {
		return err
	}
	return smokeObservability(ctx, adminURL, started)
}

func smokeHTTP(ctx context.Context, adminURL, webURL string) error {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	body := bytes.NewBufferString(`{"user_name":"admin","password":"admin"}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/api/auth/login", body)
	if err != nil {
		return fmt.Errorf("create admin login request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("admin login returned %s", response.Status)
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil || login.CSRF == "" {
		return errors.New("admin login response has no CSRF token")
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/api/clients", nil)
	if err != nil {
		return fmt.Errorf("create clients request: %w", err)
	}
	response, err = client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	var clients struct {
		Clients []smokeClient `json:"clients"`
	}
	if err := json.NewDecoder(response.Body).Decode(&clients); err != nil {
		return err
	}
	if !devClientReady(clients.Clients) {
		return errors.New("dev-client is missing ACTIVE smux or Agent/session state")
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/api/config", nil)
	if err != nil {
		return fmt.Errorf("create config request: %w", err)
	}
	response, err = client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if bytes.Contains(data, []byte("cG9ydHBpbG90LWRldi")) || !bytes.Contains(data, []byte("__REDACTED__")) {
		return errors.New("config API leaked or failed to redact client token")
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, webURL, nil)
	if err != nil {
		return fmt.Errorf("create web request: %w", err)
	}
	response, err = client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("web returned %s", response.Status)
	}
	return nil
}

func devClientReady(clients []smokeClient) bool {
	for _, item := range clients {
		if item.ClientID != "dev-client" {
			continue
		}
		return item.State == "ACTIVE" && item.MuxSessions > 0 &&
			item.AgentStatus == "available" && item.Agent != nil &&
			item.Agent.Version == "dev" && item.Agent.ProtocolVersion == 1 &&
			len(item.Agent.Capabilities) == 3 && item.Session != nil &&
			item.Session.SourceIP != "" && item.Session.ConnectedAt > 0 &&
			item.Session.LastSeenAt >= item.Session.ConnectedAt
	}
	return false
}

func smokeTCP(ctx context.Context, address, sourceIP string, payloadSize int) error {
	seed := []byte(fmt.Sprintf("portpilot-tcp-%d-", time.Now().UnixNano()))
	payload := bytes.Repeat(seed, payloadSize/len(seed)+1)[:payloadSize]
	conn, err := (&net.Dialer{
		Timeout:   3 * time.Second,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceIP)},
	}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		return err
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if !bytes.Equal(response, payload) {
		return errors.New("TCP payload mismatch")
	}
	expected := sha256.Sum256(payload)
	actual := sha256.Sum256(response)
	if hex.EncodeToString(expected[:]) != hex.EncodeToString(actual[:]) {
		return errors.New("TCP SHA-256 mismatch")
	}
	return nil
}

func smokeTCPDenied(ctx context.Context, address, sourceIP string) error {
	conn, err := (&net.Dialer{
		Timeout:   3 * time.Second,
		LocalAddr: &net.TCPAddr{IP: net.ParseIP(sourceIP)},
	}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("must-not-reach-target")); err != nil {
		return nil
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		return errors.New("denied source received target data")
	}
	return nil
}

func smokeTCPRate(ctx context.Context, address string, payloadSize int) error {
	started := time.Now()
	if err := smokeTCP(ctx, address, "127.0.0.5", payloadSize); err != nil {
		return err
	}
	elapsed := time.Since(started)
	if elapsed < 1200*time.Millisecond || elapsed > 8*time.Second {
		return fmt.Errorf("unexpected shaping duration %s", elapsed)
	}
	return nil
}

func smokeUDP(address string) error {
	target, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}
	for source := 0; source < 2; source++ {
		sourceIP := net.ParseIP(fmt.Sprintf("127.0.0.%d", source+2))
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: sourceIP})
		if err != nil {
			return err
		}
		payload := []byte(fmt.Sprintf("portpilot-udp-source-%d-%d", source, time.Now().UnixNano()))
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.WriteToUDP(payload, target); err != nil {
			_ = conn.Close()
			return err
		}
		buffer := make([]byte, 65507)
		n, _, err := conn.ReadFromUDP(buffer)
		_ = conn.Close()
		if err != nil {
			return err
		}
		if !bytes.Equal(buffer[:n], payload) {
			return fmt.Errorf("UDP payload mismatch for source %d", source)
		}
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
