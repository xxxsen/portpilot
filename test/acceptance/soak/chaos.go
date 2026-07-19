package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"

	"github.com/portpilot/portpilot/internal/config"
)

type chaosAdmin struct {
	base   string
	client *http.Client
	csrf   string
}

type clientStatus struct {
	ClientID    string `json:"client_id"`
	State       string `json:"state"`
	Epoch       uint64 `json:"epoch"`
	MuxSessions int    `json:"mux_sessions"`
}

type recoverySummary struct {
	PendingTunnels int    `json:"pending_tunnels"`
	ActiveTunnels  int64  `json:"active_tunnels"`
	UDPFlows       int    `json:"udp_flows"`
	Goroutines     int    `json:"goroutines"`
	OpenFDs        int    `json:"open_fds"`
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
}

func newChaosAdmin(ctx context.Context) (*chaosAdmin, error) {
	base := envString("PORTPILOT_DEV_ADMIN_URL", "http://127.0.0.1:5100")
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	admin := &chaosAdmin{
		base: base,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Jar:     jar,
		},
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	if err := admin.doJSON(
		ctx,
		http.MethodPost,
		"/api/auth/login",
		map[string]string{
			"user_name": "admin", "password": "admin", "session_ttl": "24h",
		},
		"",
		&login,
	); err != nil {
		return nil, err
	}
	if login.CSRF == "" {
		return nil, fmt.Errorf("soak admin login returned no CSRF token")
	}
	admin.csrf = login.CSRF
	return admin, nil
}

func runControlChaos(
	ctx context.Context,
	admin *chaosAdmin,
	deadline time.Time,
	interval time.Duration,
	result *counters,
) {
	if interval <= 0 {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if !time.Now().Before(deadline) {
				return
			}
			if err := admin.rotate(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "soak control chaos failed: %v\n", err)
				result.failed.Add(1)
				return
			}
			result.chaos.Add(1)
			timer.Reset(interval)
		}
	}
}

func (a *chaosAdmin) rotate(ctx context.Context) error {
	var file config.ClientFile
	if err := a.doJSON(ctx, http.MethodGet, "/api/config", nil, "", &file); err != nil {
		return err
	}
	request := map[string]any{
		"expected_revision": file.Revision,
		"config":            file,
	}
	if err := a.doJSON(ctx, http.MethodPut, "/api/config", request, a.csrf, &config.ClientFile{}); err != nil {
		return fmt.Errorf("soak no-op config update: %w", err)
	}
	before, err := a.devClient(ctx)
	if err != nil {
		return err
	}
	duplicateEpoch, err := replaceControlSession(ctx)
	if err != nil {
		return fmt.Errorf("soak duplicate control login: %w", err)
	}
	if duplicateEpoch <= before.Epoch {
		return fmt.Errorf("duplicate control epoch did not advance: before=%d duplicate=%d", before.Epoch, duplicateEpoch)
	}
	reconnectDeadline := time.Now().Add(20 * time.Second)
	for {
		current, err := a.devClient(ctx)
		if err == nil && current.State == "ACTIVE" &&
			current.MuxSessions > 0 && current.Epoch > duplicateEpoch {
			return nil
		}
		if time.Now().After(reconnectDeadline) {
			return fmt.Errorf("dev-client did not reconnect after epoch %d", duplicateEpoch)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (a *chaosAdmin) summary(ctx context.Context) (recoverySummary, error) {
	var summary recoverySummary
	err := a.doJSON(ctx, http.MethodGet, "/api/metrics/summary", nil, "", &summary)
	return summary, err
}

func (a *chaosAdmin) waitForRecovery(ctx context.Context) error {
	deadline := time.Now().Add(75 * time.Second)
	for {
		summary, summaryErr := a.summary(ctx)
		client, clientErr := a.devClient(ctx)
		if summaryErr == nil && clientErr == nil &&
			summary.PendingTunnels == 0 && summary.ActiveTunnels == 0 &&
			summary.UDPFlows == 0 && client.State == "ACTIVE" &&
			client.MuxSessions == 1 {
			return nil
		}
		if time.Now().After(deadline) {
			cause := errors.Join(summaryErr, clientErr)
			if cause == nil {
				cause = errors.New("runtime state did not converge")
			}
			return fmt.Errorf("runtime did not recover: summary=%+v client=%+v: %w", summary, client, cause)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (a *chaosAdmin) devClient(ctx context.Context) (clientStatus, error) {
	var payload struct {
		Clients []clientStatus `json:"clients"`
	}
	if err := a.doJSON(ctx, http.MethodGet, "/api/clients", nil, "", &payload); err != nil {
		return clientStatus{}, err
	}
	for _, client := range payload.Clients {
		if client.ClientID == "dev-client" {
			return client, nil
		}
	}
	return clientStatus{}, fmt.Errorf("dev-client is missing")
}

func (a *chaosAdmin) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	csrf string,
	target any,
) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("%s %s returned %s: %s", method, path, response.Status, detail)
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
