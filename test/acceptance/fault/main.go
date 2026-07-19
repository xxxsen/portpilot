package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"slices"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/transport"
	"github.com/xtaci/smux"
)

const (
	adminURL     = "http://127.0.0.1:5100"
	serverAddr   = "127.0.0.1:6100"
	faultClient  = "fault-client"
	faultToken   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	faultTCP     = "127.0.0.1:17000"
	faultUDP     = "127.0.0.1:17001"
	faultDirect  = "127.0.0.1:17002"
	refusedProxy = "127.0.0.1:17003"
)

type adminAPI struct {
	client *http.Client
	csrf   string
}

type authTrace struct {
	challenge protocol.AuthChallenge
	signature string
}

type controlClient struct {
	conn    *websocket.Conn
	id      string
	epoch   uint64
	client  string
	params  protocol.DataPlane
	seq     uint64
	pending []protocol.Message
	muxID   string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fault test failed:", err)
		os.Exit(1)
	}
	fmt.Println("fault test passed: newest-wins, challenge/ticket replay, malformed frames, target refusal, and audit")
}

func run() (runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := expectInvalidAdminLogin(ctx); err != nil {
		return err
	}
	admin, err := loginAdmin(ctx)
	if err != nil {
		return err
	}
	if err := expectCSRFFailure(ctx, admin); err != nil {
		return err
	}
	original, err := admin.getConfig(ctx)
	if err != nil {
		return err
	}
	temporary, err := faultConfiguration(original)
	if err != nil {
		return err
	}
	if err := admin.putConfig(ctx, original.Revision, temporary); err != nil {
		return fmt.Errorf("install fault configuration: %w", err)
	}
	defer func() {
		current, err := admin.getConfig(context.WithoutCancel(ctx))
		if err == nil {
			original.Revision = 0
			err = admin.putConfig(context.WithoutCancel(ctx), current.Revision, original)
		}
		if err != nil && runErr == nil {
			runErr = fmt.Errorf("restore development configuration: %w", err)
		}
	}()

	first, firstTrace, err := authenticateControl(ctx, faultClient, faultToken)
	if err != nil {
		return err
	}
	defer first.close()
	if err := replayChallenge(ctx, firstTrace); err != nil {
		return err
	}
	second, _, err := authenticateControl(ctx, faultClient, faultToken)
	if err != nil {
		return err
	}
	defer second.close()
	if _, err := first.waitFor(protocol.TypeSessionReplaced); err != nil {
		return fmt.Errorf("newest-wins did not replace the old control session: %w", err)
	}
	if second.epoch <= first.epoch {
		return fmt.Errorf("session epoch did not increase: old=%d new=%d", first.epoch, second.epoch)
	}

	muxSession, muxConn, err := establishMux(ctx, second)
	if err != nil {
		return err
	}
	defer func() {
		_ = muxSession.Close()
		_ = muxConn.Close()
	}()
	if err := testSMuxDataTicket(ctx, second, muxSession); err != nil {
		return err
	}
	if err := testDirectDataTicket(ctx, second); err != nil {
		return err
	}
	if err := testUDPChannelTicket(second, muxSession); err != nil {
		return err
	}
	if err := testMalformedDataFrames(ctx); err != nil {
		return err
	}
	if err := testRefusedTarget(ctx); err != nil {
		return err
	}
	if err := waitForAudit(ctx, admin, []string{
		"client_auth_failed", "mux_ticket_rejected",
		"tcp_ticket_rejected", "udp_ticket_rejected",
		protocol.ErrTargetDialFailed,
	}); err != nil {
		return err
	}
	return ensureHealthy(ctx)
}

func faultConfiguration(original config.ClientFile) (config.ClientFile, error) {
	raw, err := json.Marshal(original)
	if err != nil {
		return config.ClientFile{}, err
	}
	var next config.ClientFile
	if err := json.Unmarshal(raw, &next); err != nil {
		return config.ClientFile{}, err
	}
	next.Revision = 0
	var devFound bool
	for index := range next.ClientConfig {
		if next.ClientConfig[index].ClientID != "dev-client" {
			continue
		}
		devFound = true
		proxy, err := tunnelProxy(
			"fault-target-refused", "Fault target refusal",
			"tcp", "auto", refusedProxy, "127.0.0.1:1",
		)
		if err != nil {
			return config.ClientFile{}, err
		}
		next.ClientConfig[index].ProxyList = append(next.ClientConfig[index].ProxyList, proxy)
	}
	if !devFound {
		return config.ClientFile{}, errors.New("development client is missing")
	}
	tcp, err := tunnelProxy("fault-tcp", "Fault TCP", "tcp", "smux", faultTCP, "127.0.0.1:18080")
	if err != nil {
		return config.ClientFile{}, err
	}
	direct, err := tunnelProxy("fault-direct", "Fault direct", "tcp", "direct", faultDirect, "127.0.0.1:18080")
	if err != nil {
		return config.ClientFile{}, err
	}
	udp, err := tunnelProxy("fault-udp", "Fault UDP", "udp", "smux-datagram", faultUDP, "127.0.0.1:18081")
	if err != nil {
		return config.ClientFile{}, err
	}
	next.ClientConfig = append(next.ClientConfig, config.ClientConfig{
		ClientID: faultClient, ClientToken: faultToken, Enabled: true,
		ProxyList: []config.ProxyEnvelope{tcp, direct, udp},
	})
	return next, nil
}

func tunnelProxy(proxyID, remark, network, mode, bind, connect string) (config.ProxyEnvelope, error) {
	data, err := json.Marshal(config.TunnelData{
		Network: network, Transport: mode, Bind: bind, Connect: connect,
	})
	if err != nil {
		return config.ProxyEnvelope{}, err
	}
	return config.ProxyEnvelope{
		ProxyID: proxyID, Remark: remark, Kind: "tunnel", Enabled: true, Data: data,
	}, nil
}

func authenticateControl(ctx context.Context, clientID, token string) (*controlClient, authTrace, error) {
	url := "ws://" + serverAddr + "/portpilot/control"
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, url, http.Header{
		"User-Agent": []string{"portpilot-fault-test/1"},
	})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, authTrace{}, fmt.Errorf("dial control: %w", err)
	}
	nonce, err := auth.RandomToken(18)
	if err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	init, _ := protocol.NewMessage(protocol.TypeAuthInit, protocol.AuthInit{
		ClientID: clientID, Role: "control", ClientNonce: nonce,
	})
	if err := conn.WriteJSON(init); err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	var challengeMessage protocol.Message
	if err := conn.ReadJSON(&challengeMessage); err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	challenge, err := protocol.DecodePayload[protocol.AuthChallenge](challengeMessage)
	if err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	signature, err := auth.Signature(
		token, clientID, "control",
		challenge.ChallengeID, challenge.Challenge, nonce,
	)
	if err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	proof, _ := protocol.NewMessage(protocol.TypeAuthProof, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID, Signature: signature,
	})
	if err := conn.WriteJSON(proof); err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	var result protocol.Message
	if err := conn.ReadJSON(&result); err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	if result.Type != protocol.TypeAuthOK {
		_ = conn.Close()
		return nil, authTrace{}, fmt.Errorf("control authentication returned %s", result.Type)
	}
	ok, err := protocol.DecodePayload[protocol.AuthOK](result)
	if err != nil {
		_ = conn.Close()
		return nil, authTrace{}, err
	}
	return &controlClient{
		conn: conn, id: ok.SessionID, epoch: ok.SessionEpoch,
		client: clientID, params: ok.DataPlane,
	}, authTrace{challenge: challenge, signature: signature}, nil
}

func replayChallenge(ctx context.Context, trace authTrace) error {
	url := "ws://" + serverAddr + "/portpilot/control"
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	nonce, err := auth.RandomToken(18)
	if err != nil {
		return err
	}
	init, _ := protocol.NewMessage(protocol.TypeAuthInit, protocol.AuthInit{
		ClientID: faultClient, Role: "control", ClientNonce: nonce,
	})
	if err := conn.WriteJSON(init); err != nil {
		return err
	}
	var challenge protocol.Message
	if err := conn.ReadJSON(&challenge); err != nil {
		return err
	}
	proof, _ := protocol.NewMessage(protocol.TypeAuthProof, protocol.AuthProof{
		ChallengeID: trace.challenge.ChallengeID, Signature: trace.signature,
	})
	if err := conn.WriteJSON(proof); err != nil {
		return err
	}
	var result protocol.Message
	if err := conn.ReadJSON(&result); err != nil {
		return err
	}
	if result.Type != protocol.TypeAuthError {
		return fmt.Errorf("replayed challenge returned %s", result.Type)
	}
	return nil
}

func establishMux(ctx context.Context, control *controlClient) (*smux.Session, net.Conn, error) {
	message, err := control.waitFor(protocol.TypeCreateMux)
	if err != nil {
		return nil, nil, err
	}
	request, err := protocol.DecodePayload[protocol.CreateMux](message)
	if err != nil {
		return nil, nil, err
	}
	control.muxID = request.MuxID
	conn, result, err := openMuxConnection(ctx, control, request, protocol.Version)
	if err != nil {
		return nil, nil, err
	}
	if result.Type != "MUX_OK" {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("valid mux ticket returned %s/%s", result.Type, result.Code)
	}
	session, err := smux.Client(conn, smuxConfig(control.params))
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := control.send(protocol.TypeMuxReady, protocol.MuxReady{MuxID: request.MuxID}); err != nil {
		_ = session.Close()
		_ = conn.Close()
		return nil, nil, err
	}
	replay, replayResult, err := openMuxConnection(ctx, control, request, protocol.Version)
	if replay != nil {
		_ = replay.Close()
	}
	if err != nil {
		return nil, nil, err
	}
	if replayResult.Type != "MUX_ERROR" || replayResult.Code != protocol.ErrMuxTicketInvalid {
		return nil, nil, fmt.Errorf("replayed mux ticket returned %s/%s", replayResult.Type, replayResult.Code)
	}
	unknown, unknownResult, err := openMuxConnection(ctx, control, request, protocol.Version+1)
	if unknown != nil {
		_ = unknown.Close()
	}
	if err != nil {
		return nil, nil, err
	}
	if unknownResult.Type != "MUX_ERROR" {
		return nil, nil, fmt.Errorf("unknown mux version returned %s/%s", unknownResult.Type, unknownResult.Code)
	}
	return session, conn, nil
}

func openMuxConnection(
	ctx context.Context,
	control *controlClient,
	request protocol.CreateMux,
	version int,
) (net.Conn, protocol.HandshakeResult, error) {
	conn, err := transport.DialUpgrade(ctx, serverAddr, "/portpilot/data/mux", false, "", 5*time.Second)
	if err != nil {
		return nil, protocol.HandshakeResult{}, err
	}
	handshake := protocol.MuxAuth{
		Version: version, Role: "mux", ClientID: control.client,
		SessionID: control.id, SessionEpoch: control.epoch,
		MuxID: request.MuxID, MuxTicket: request.MuxTicket,
	}
	if err := protocol.WriteJSONFrame(conn, handshake); err != nil {
		_ = conn.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	var result protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &result); err != nil {
		_ = conn.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	return conn, result, nil
}

func testSMuxDataTicket(ctx context.Context, control *controlClient, mux *smux.Session) error {
	external, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", faultTCP)
	if err != nil {
		return err
	}
	defer func() { _ = external.Close() }()
	message, err := control.waitFor(protocol.TypeOpenTCP)
	if err != nil {
		return err
	}
	request, err := protocol.DecodePayload[protocol.OpenTCP](message)
	if err != nil {
		return err
	}
	if request.ProxyID != "fault-tcp" {
		return fmt.Errorf("unexpected smux proxy %s", request.ProxyID)
	}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "tcp-stream", ClientID: control.client,
		SessionID: control.id, SessionEpoch: control.epoch,
		MuxID: control.muxID, ConnectionID: request.ConnectionID,
		DataTicket: request.DataTicket,
	}
	stream, result, err := openMuxStream(mux, open)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	if result.Type != "STREAM_OK" {
		return fmt.Errorf("valid TCP ticket returned %s/%s", result.Type, result.Code)
	}
	replay, replayResult, err := openMuxStream(mux, open)
	if replay != nil {
		_ = replay.Close()
	}
	if err != nil {
		return err
	}
	if replayResult.Type != "STREAM_ERROR" || replayResult.Code != protocol.ErrDataTicketInvalid {
		return fmt.Errorf("replayed TCP ticket returned %s/%s", replayResult.Type, replayResult.Code)
	}
	return relayRoundTrip(external, stream, "smux-ticket-replay")
}

func testDirectDataTicket(ctx context.Context, control *controlClient) error {
	external, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", faultDirect)
	if err != nil {
		return err
	}
	defer func() { _ = external.Close() }()
	message, err := control.waitFor(protocol.TypeOpenTCP)
	if err != nil {
		return err
	}
	request, err := protocol.DecodePayload[protocol.OpenTCP](message)
	if err != nil {
		return err
	}
	if request.ProxyID != "fault-direct" {
		return fmt.Errorf("unexpected direct proxy %s", request.ProxyID)
	}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "direct", ClientID: control.client,
		SessionID: control.id, SessionEpoch: control.epoch,
		ConnectionID: request.ConnectionID, DataTicket: request.DataTicket,
	}
	conn, result, err := openDirect(ctx, open)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if result.Type != "STREAM_OK" {
		return fmt.Errorf("valid direct ticket returned %s/%s", result.Type, result.Code)
	}
	replay, replayResult, err := openDirect(ctx, open)
	if replay != nil {
		_ = replay.Close()
	}
	if err != nil {
		return err
	}
	if replayResult.Type != "STREAM_ERROR" || replayResult.Code != protocol.ErrDataTicketInvalid {
		return fmt.Errorf("replayed direct ticket returned %s/%s", replayResult.Type, replayResult.Code)
	}
	return relayRoundTrip(external, conn, "direct-ticket-replay")
}

func testUDPChannelTicket(control *controlClient, mux *smux.Session) error {
	message, err := control.waitFor(protocol.TypeOpenUDPChannel)
	if err != nil {
		return err
	}
	request, err := protocol.DecodePayload[protocol.OpenUDPChannel](message)
	if err != nil {
		return err
	}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "udp-channel", ClientID: control.client,
		SessionID: control.id, SessionEpoch: control.epoch,
		MuxID: control.muxID, ProxyID: request.ProxyID,
		ChannelID: request.ChannelID, ChannelTicket: request.ChannelTicket,
	}
	stream, result, err := openMuxStream(mux, open)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	if result.Type != "STREAM_OK" {
		return fmt.Errorf("valid UDP ticket returned %s/%s", result.Type, result.Code)
	}
	replay, replayResult, err := openMuxStream(mux, open)
	if replay != nil {
		_ = replay.Close()
	}
	if err != nil {
		return err
	}
	if replayResult.Type != "STREAM_ERROR" || replayResult.Code != protocol.ErrUDPChannelInvalid {
		return fmt.Errorf("replayed UDP ticket returned %s/%s", replayResult.Type, replayResult.Code)
	}
	return nil
}

func openMuxStream(mux *smux.Session, open protocol.StreamOpen) (net.Conn, protocol.HandshakeResult, error) {
	stream, err := mux.OpenStream()
	if err != nil {
		return nil, protocol.HandshakeResult{}, err
	}
	if err := protocol.WriteJSONFrame(stream, open); err != nil {
		_ = stream.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	var result protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(stream, protocol.DefaultMaxJSONFrame, &result); err != nil {
		_ = stream.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	return stream, result, nil
}

func openDirect(ctx context.Context, open protocol.StreamOpen) (net.Conn, protocol.HandshakeResult, error) {
	conn, err := transport.DialUpgrade(ctx, serverAddr, "/portpilot/data/direct", false, "", 5*time.Second)
	if err != nil {
		return nil, protocol.HandshakeResult{}, err
	}
	if err := protocol.WriteJSONFrame(conn, open); err != nil {
		_ = conn.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	var result protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &result); err != nil {
		_ = conn.Close()
		return nil, protocol.HandshakeResult{}, err
	}
	return conn, result, nil
}

func relayRoundTrip(external, data net.Conn, value string) error {
	payload := []byte(value)
	_ = external.SetDeadline(time.Now().Add(5 * time.Second))
	_ = data.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := external.Write(payload); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(data, received); err != nil {
		return err
	}
	if !bytes.Equal(payload, received) {
		return errors.New("external-to-data payload mismatch")
	}
	if _, err := data.Write(received); err != nil {
		return err
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(external, echo); err != nil {
		return err
	}
	if !bytes.Equal(payload, echo) {
		return errors.New("data-to-external payload mismatch")
	}
	return nil
}

func testMalformedDataFrames(ctx context.Context) error {
	conn, err := transport.DialUpgrade(ctx, serverAddr, "/portpilot/data/mux", false, "", 5*time.Second)
	if err != nil {
		return err
	}
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], protocol.DefaultMaxJSONFrame+1)
	if _, err := conn.Write(oversized[:]); err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		_ = conn.Close()
		return errors.New("oversized frame did not close the data connection")
	} else {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			_ = conn.Close()
			return errors.New("oversized frame connection stayed open until timeout")
		}
	}
	_ = conn.Close()

	truncated, err := transport.DialUpgrade(ctx, serverAddr, "/portpilot/data/mux", false, "", 5*time.Second)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 32)
	_, _ = truncated.Write(append(header[:], '{'))
	_ = truncated.Close()
	return ensureHealthy(ctx)
}

func testRefusedTarget(ctx context.Context) error {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", refusedProxy)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		return errors.New("refused target left the external connection open")
	}
	return nil
}

func waitForAudit(ctx context.Context, admin *adminAPI, reasons []string) error {
	for {
		var payload struct {
			Events []map[string]any `json:"events"`
		}
		if err := admin.getJSON(ctx, "/api/events?limit=500", &payload); err != nil {
			return err
		}
		found := make(map[string]bool, len(reasons))
		for _, event := range payload.Events {
			reason, _ := event["reason"].(string)
			if slices.Contains(reasons, reason) {
				found[reason] = true
			}
		}
		var missing []string
		for _, reason := range reasons {
			if !found[reason] {
				missing = append(missing, reason)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("missing audit reasons: %v", missing)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func ensureHealthy(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/health/ready", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server health returned %s", response.Status)
	}
	return nil
}

func expectInvalidAdminLogin(ctx context.Context) error {
	body := bytes.NewBufferString(`{"user_name":"admin","password":"invalid"}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/api/auth/login", body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("invalid admin login returned %s", response.Status)
	}
	return nil
}

func loginAdmin(ctx context.Context) (*adminAPI, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	body := bytes.NewBufferString(`{"user_name":"admin","password":"admin"}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/api/auth/login", body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("admin login returned %s", response.Status)
	}
	var payload struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return &adminAPI{client: client, csrf: payload.CSRF}, nil
}

func expectCSRFFailure(ctx context.Context, admin *adminAPI) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, adminURL+"/api/clients/dev-client/disconnect", nil,
	)
	if err != nil {
		return err
	}
	response, err := admin.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusForbidden {
		return fmt.Errorf("missing CSRF token returned %s", response.Status)
	}
	return nil
}

func (a *adminAPI) getConfig(ctx context.Context) (config.ClientFile, error) {
	var file config.ClientFile
	err := a.getJSON(ctx, "/api/config", &file)
	return file, err
}

func (a *adminAPI) putConfig(
	ctx context.Context,
	revision uint64,
	file config.ClientFile,
) error {
	body, err := json.Marshal(map[string]any{
		"expected_revision": revision,
		"config":            file,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPut, adminURL+"/api/config", bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", a.csrf)
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("config update returned %s: %s", response.Status, detail)
	}
	var updated config.ClientFile
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		return err
	}
	return nil
}

func (a *adminAPI) getJSON(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+path, nil)
	if err != nil {
		return err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %s", path, response.Status)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func (c *controlClient) send(kind string, payload any) error {
	message, err := protocol.NewMessage(kind, payload)
	if err != nil {
		return err
	}
	c.seq++
	message.SessionID = c.id
	message.SessionEpoch = c.epoch
	message.Sequence = c.seq
	return c.conn.WriteJSON(message)
}

func (c *controlClient) waitFor(kind string) (protocol.Message, error) {
	for index, message := range c.pending {
		if message.Type == kind {
			c.pending = append(c.pending[:index], c.pending[index+1:]...)
			return message, nil
		}
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var message protocol.Message
		if err := c.conn.ReadJSON(&message); err != nil {
			return protocol.Message{}, err
		}
		if message.Type == protocol.TypePing {
			if err := c.send(protocol.TypePong, map[string]string{
				"time": time.Now().UTC().Format(time.RFC3339Nano),
			}); err != nil {
				return protocol.Message{}, err
			}
			continue
		}
		if message.Type == kind {
			return message, nil
		}
		c.pending = append(c.pending, message)
	}
}

func (c *controlClient) close() {
	_ = c.conn.Close()
}

func smuxConfig(params protocol.DataPlane) *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.Version = params.SMux.ProtocolVersion
	cfg.KeepAliveInterval = durationOr(params.SMux.KeepAliveInterval, 20*time.Second)
	cfg.KeepAliveTimeout = durationOr(params.SMux.KeepAliveTimeout, 60*time.Second)
	cfg.MaxFrameSize = params.SMux.MaxFrameSize
	cfg.MaxReceiveBuffer = params.SMux.MaxReceiveBuffer
	cfg.MaxStreamBuffer = params.SMux.MaxStreamBuffer
	return cfg
}

func durationOr(value string, fallback time.Duration) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}
