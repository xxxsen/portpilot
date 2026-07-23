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
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/cookiejar"
	stdmail "net/mail"
	"os"
	"regexp"
	"strings"
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
	fmt.Println("PortPilot smoke passed: API, Web, ACL, email knock, TCP smux/direct, PROXY v1, rate limit, and UDP-over-TCP")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminURL := env("PORTPILOT_DEV_ADMIN_URL", "http://127.0.0.1:5100")
	if os.Getenv("PORTPILOT_KNOCK_RESTART_CHECK") == "1" {
		return smokeKnockRestart(ctx, adminURL)
	}
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
	if err := smokeKnock(ctx, adminURL, env("PORTPILOT_DEV_SMTP_URL", "http://127.0.0.1:18083")); err != nil {
		return fmt.Errorf("email knock smoke: %w", err)
	}
	return smokeObservability(ctx, adminURL, started)
}

func smokeKnock(ctx context.Context, adminURL, smtpURL string) error {
	adminClient, csrf, err := smokeAdminSession(ctx, adminURL)
	if err != nil {
		return err
	}
	for _, sourceIP := range []string{"127.0.0.6", "127.0.0.8", "127.0.0.9"} {
		if err := revokeKnockGrant(ctx, adminClient, adminURL, csrf, sourceIP); err != nil {
			return err
		}
	}
	if err := clearMailbox(ctx, smtpURL); err != nil {
		return err
	}
	if err := smokeTCPDenied(ctx, "127.0.0.1:7005", "127.0.0.6"); err != nil {
		return fmt.Errorf("ungranted TCP source was not denied: %w", err)
	}
	if err := smokeUDPDenied("127.0.0.1:7006", "127.0.0.6"); err != nil {
		return fmt.Errorf("ungranted UDP source was not denied: %w", err)
	}

	token, err := requestKnockToken(ctx, adminURL, smtpURL, "allowed-a@example.com", "127.0.0.6")
	if err != nil {
		return err
	}
	if status, verifyErr := verifyKnockToken(ctx, adminURL, token, "127.0.0.7"); verifyErr != nil || status != http.StatusForbidden {
		return unexpectedHTTPStatus("scanner mismatch", status, http.StatusForbidden, verifyErr)
	}
	if status, verifyErr := verifyKnockToken(ctx, adminURL, token, "127.0.0.6"); verifyErr != nil || status != http.StatusOK {
		return unexpectedHTTPStatus("matching verification", status, http.StatusOK, verifyErr)
	}
	if err := smokeTCP(ctx, "127.0.0.1:7005", "127.0.0.6", 1024); err != nil {
		return fmt.Errorf("granted TCP source: %w", err)
	}
	if err := smokeUDPFrom("127.0.0.1:7006", "127.0.0.6"); err != nil {
		return fmt.Errorf("granted UDP source: %w", err)
	}

	aclToken, err := requestKnockToken(ctx, adminURL, smtpURL, "allowed-b@example.com", "127.0.0.9")
	if err != nil {
		return err
	}
	if status, verifyErr := verifyKnockToken(ctx, adminURL, aclToken, "127.0.0.9"); verifyErr != nil || status != http.StatusOK {
		return unexpectedHTTPStatus("ACL test verification", status, http.StatusOK, verifyErr)
	}
	if err := smokeTCPDenied(ctx, "127.0.0.1:7005", "127.0.0.9"); err != nil {
		return fmt.Errorf("grant bypassed proxy ACL: %w", err)
	}

	if err := revokeKnockGrant(ctx, adminClient, adminURL, csrf, "127.0.0.6"); err != nil {
		return err
	}
	if err := smokeTCPDenied(ctx, "127.0.0.1:7005", "127.0.0.6"); err != nil {
		return fmt.Errorf("revoked TCP source was not denied: %w", err)
	}
	if err := smokeUDPDenied("127.0.0.1:7006", "127.0.0.6"); err != nil {
		return fmt.Errorf("revoked UDP source was not denied: %w", err)
	}

	restartToken, err := requestKnockToken(ctx, adminURL, smtpURL, "allowed-c@example.com", "127.0.0.8")
	if err != nil {
		return err
	}
	if status, verifyErr := verifyKnockToken(ctx, adminURL, restartToken, "127.0.0.8"); verifyErr != nil || status != http.StatusOK {
		return unexpectedHTTPStatus("restart test verification", status, http.StatusOK, verifyErr)
	}
	return smokeTCP(ctx, "127.0.0.1:7005", "127.0.0.8", 256)
}

func unexpectedHTTPStatus(label string, actual, expected int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: status=%d, expected=%d: %w", label, actual, expected, err)
	}
	return fmt.Errorf("%s: status=%d, expected=%d", label, actual, expected)
}

func smokeKnockRestart(ctx context.Context, adminURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/api/knock/status", nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Real-IP", "127.0.0.8")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	var status struct {
		Authorized bool `json:"authorized"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return err
	}
	if status.Authorized {
		return errors.New("knock grant survived server restart")
	}
	if err := smokeTCPDenied(ctx, "127.0.0.1:7005", "127.0.0.8"); err != nil {
		return fmt.Errorf("protected TCP accepted after restart: %w", err)
	}
	return smokeUDPDenied("127.0.0.1:7006", "127.0.0.8")
}

func smokeAdminSession(ctx context.Context, adminURL string) (*http.Client, string, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, adminURL+"/api/auth/login",
		strings.NewReader(`{"user_name":"admin","password":"admin"}`),
	)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = response.Body.Close() }()
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&login) != nil || login.CSRF == "" {
		return nil, "", fmt.Errorf("admin login for knock returned %s", response.Status)
	}
	return client, login.CSRF, nil
}

func revokeKnockGrant(
	ctx context.Context,
	client *http.Client,
	adminURL, csrf, sourceIP string,
) error {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodDelete, adminURL+"/api/knock/grants/"+sourceIP, nil,
	)
	if err != nil {
		return err
	}
	request.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("revoke %s returned %s", sourceIP, response.Status)
	}
	return nil
}

func clearMailbox(ctx context.Context, smtpURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, smtpURL+"/messages", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("clear SMTP mailbox returned %s", response.Status)
	}
	return nil
}

func requestKnockToken(
	ctx context.Context,
	adminURL, smtpURL, email, sourceIP string,
) (string, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, adminURL+"/api/knock/requests",
		strings.NewReader(`{"email":"`+email+`"}`),
	)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Real-IP", sourceIP)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("knock request for %s returned %s", email, response.Status)
	}
	for attempt := 0; attempt < 100; attempt++ {
		mailRequest, requestErr := http.NewRequestWithContext(
			ctx, http.MethodGet, smtpURL+"/messages?to="+email, nil,
		)
		if requestErr != nil {
			return "", requestErr
		}
		mailResponse, requestErr := http.DefaultClient.Do(mailRequest)
		if requestErr != nil {
			return "", requestErr
		}
		var mailbox struct {
			Items []struct {
				Data string `json:"data"`
			} `json:"items"`
		}
		decodeErr := json.NewDecoder(mailResponse.Body).Decode(&mailbox)
		_ = mailResponse.Body.Close()
		if decodeErr != nil {
			return "", decodeErr
		}
		if len(mailbox.Items) > 0 {
			if token, extractErr := tokenFromMessage(mailbox.Items[len(mailbox.Items)-1].Data); extractErr == nil {
				return token, nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", fmt.Errorf("SMTP fixture did not capture token for %s", email)
}

func tokenFromMessage(data string) (string, error) {
	message, err := stdmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		return "", err
	}
	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		return "", errors.New("knock email is not multipart/alternative")
	}
	parts := multipart.NewReader(message.Body, parameters["boundary"])
	pattern := regexp.MustCompile(`#token=([A-Za-z0-9_-]{43})`)
	for {
		part, nextErr := parts.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		var reader io.Reader = part
		if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
			reader = quotedprintable.NewReader(part)
		}
		body, readErr := io.ReadAll(reader)
		_ = part.Close()
		if readErr != nil {
			return "", readErr
		}
		if match := pattern.FindSubmatch(body); len(match) == 2 {
			return string(match[1]), nil
		}
	}
	return "", errors.New("knock token is missing from email")
}

func verifyKnockToken(ctx context.Context, adminURL, token, sourceIP string) (int, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, adminURL+"/api/knock/verify",
		strings.NewReader(`{"token":"`+token+`"}`),
	)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Real-IP", sourceIP)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, nil
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

func smokeUDPFrom(address, sourceIP string) error {
	target, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(sourceIP)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	payload := []byte(fmt.Sprintf("portpilot-knock-udp-%d", time.Now().UnixNano()))
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.WriteToUDP(payload, target); err != nil {
		return err
	}
	buffer := make([]byte, 65507)
	n, _, err := conn.ReadFromUDP(buffer)
	if err != nil {
		return err
	}
	if !bytes.Equal(buffer[:n], payload) {
		return errors.New("protected UDP payload mismatch")
	}
	return nil
}

func smokeUDPDenied(address, sourceIP string) error {
	target, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(sourceIP)})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := conn.WriteToUDP([]byte("must-not-reach-target"), target); err != nil {
		return err
	}
	if _, _, err := conn.ReadFromUDP(make([]byte, 64)); err == nil {
		return errors.New("denied UDP source received target data")
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
