package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/knock"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestExternalAccessMetricsAccumulateWithoutSourceCardinality(t *testing.T) {
	t.Parallel()
	var metrics externalAccessMetrics
	metrics.observe("tcp", "proxy-a", "global_deny", 0)
	metrics.observe("tcp", "proxy-a", "global_deny", 0)
	metrics.observe("udp", "proxy-a", "proxy_allow_miss", 120)
	metrics.observe("udp", "proxy-a", "proxy_allow_miss", 80)

	snapshot := metrics.snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("metric series = %#v", snapshot)
	}
	if snapshot[0].Connections != 2 || snapshot[0].Datagrams != 0 {
		t.Fatalf("TCP metric = %#v", snapshot[0])
	}
	if snapshot[1].Datagrams != 2 || snapshot[1].Bytes != 200 {
		t.Fatalf("UDP metric = %#v", snapshot[1])
	}
}

func TestExternalAccessKnockRunsAfterGlobalAndProxyACL(t *testing.T) {
	t.Parallel()
	manager, _ := newTestKnockManager(t)
	runtime := ProxyRuntime{
		ClientID: "client-a",
		Proxy:    config.ProxyEnvelope{ProxyID: "proxy-a"},
		Data:     config.TunnelData{AllowKnockOnly: true},
		Access: accesspolicy.Compile(config.AccessPolicyConfig{
			DenyCIDRs: []string{"198.51.100.0/24"},
		}),
	}
	app := &App{
		Knock: manager,
		GlobalAccess: accesspolicy.Compile(config.AccessPolicyConfig{
			DenyCIDRs: []string{"192.0.2.0/24"},
		}),
	}
	if decision := app.allowExternal(runtime, tcpAddress("192.0.2.10")); decision.Reason != "global_deny" {
		t.Fatalf("global ACL decision = %#v", decision)
	}
	if decision := app.allowExternal(runtime, tcpAddress("198.51.100.10")); decision.Reason != "proxy_deny" {
		t.Fatalf("proxy ACL decision = %#v", decision)
	}
	if decision := app.allowExternal(runtime, tcpAddress("203.0.113.10")); decision.Reason != "knock_required" {
		t.Fatalf("missing grant decision = %#v", decision)
	}
	grantKnockIP(t, manager, "203.0.113.10")
	if decision := app.allowExternal(runtime, tcpAddress("203.0.113.10")); !decision.Allowed {
		t.Fatalf("granted source denied = %#v", decision)
	}
	runtime.Data.AllowKnockOnly = false
	if decision := (&App{GlobalAccess: accesspolicy.Compile(config.AccessPolicyConfig{})}).
		allowExternal(runtime, tcpAddress("203.0.113.11")); !decision.Allowed {
		t.Fatalf("unprotected proxy denied = %#v", decision)
	}
}

func TestExternalAccessKnockUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	app := &App{GlobalAccess: accesspolicy.Compile(config.AccessPolicyConfig{})}
	runtime := ProxyRuntime{
		Proxy: config.ProxyEnvelope{ProxyID: "proxy"},
		Data:  config.TunnelData{AllowKnockOnly: true},
	}
	if decision := app.allowExternal(runtime, tcpAddress("192.0.2.20")); decision.Reason != "knock_unavailable" {
		t.Fatalf("unavailable manager decision = %#v", decision)
	}
}

func TestExternalAccessPrivateOnlyUsesProxyPolicyReason(t *testing.T) {
	t.Parallel()
	app := &App{GlobalAccess: accesspolicy.Compile(config.AccessPolicyConfig{})}
	runtime := ProxyRuntime{
		Proxy:  config.ProxyEnvelope{ProxyID: "proxy"},
		Access: accesspolicy.Compile(config.AccessPolicyConfig{PrivateOnly: true}),
	}
	if decision := app.allowExternal(runtime, tcpAddress("192.168.1.20")); !decision.Allowed {
		t.Fatalf("private source denied = %#v", decision)
	}
	if decision := app.allowExternal(runtime, tcpAddress("8.8.8.8")); decision.Reason != "proxy_private_only" {
		t.Fatalf("public source decision = %#v", decision)
	}
}

func newTestKnockManager(t *testing.T) (*knock.Manager, *serverFakeClock) {
	t.Helper()
	clock := &serverFakeClock{now: time.Now().UTC()}
	randomData := make([]byte, 4096)
	for index := range randomData {
		randomData[index] = byte(index)
	}
	manager := knock.New(knock.Config{
		MagicLinkTTL: time.Minute, GrantTTL: time.Minute, ResendInterval: time.Second,
		AllowedEmails: []string{"user@example.com"}, MaxRequestsPerHour: 100,
		MaxPendingTokens: 8, MaxGrants: 8, Clock: clock,
		Random: bytes.NewReader(randomData),
	})
	t.Cleanup(manager.Close)
	return manager, clock
}

func grantKnockIP(t *testing.T, manager *knock.Manager, source string) {
	t.Helper()
	address := netip.MustParseAddr(source)
	result, _, err := manager.Request("user@example.com", address)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(result.Token)
	verification, err := manager.BeginVerify(sha256.Sum256(raw), address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CommitVerify(verification.ID, "audit"); err != nil {
		t.Fatal(err)
	}
}

func tcpAddress(source string) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(source), Port: 1234}
}

func TestExternalAccessDeniedLogsEveryRejectionAndExactMetricOnly(t *testing.T) {
	t.Parallel()
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			t.Parallel()
			testExternalAccessRejectionLogs(t, network)
		})
	}
}

func testExternalAccessRejectionLogs(t *testing.T, network string) {
	t.Helper()
	database, err := store.Open(":memory:", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	core, observed := observer.New(zap.WarnLevel)
	app := &App{
		Events: database, Logger: zap.New(core),
	}
	runtime := ProxyRuntime{
		ClientID: "client-a",
		Proxy:    config.ProxyEnvelope{ProxyID: "proxy-a"},
	}
	decision := externalAccessDecision{SourceIP: "192.0.2.10", Reason: "global_deny"}
	datagramBytes := 0
	if network == "udp" {
		datagramBytes = 128
	}
	app.recordExternalAccessDenied(runtime, network, decision, datagramBytes)
	app.recordExternalAccessDenied(runtime, network, decision, datagramBytes)

	logs := observed.FilterMessage("external access denied").All()
	if len(logs) != 2 {
		t.Fatalf("external access logs = %d, want 2", len(logs))
	}
	for _, entry := range logs {
		fields := entry.ContextMap()
		if fields["network"] != network || fields["client_id"] != "client-a" || fields["proxy_id"] != "proxy-a" ||
			fields["source_ip"] != "192.0.2.10" || fields["policy_layer"] != "global" || fields["reason"] != "deny" ||
			fields["datagram_bytes"] != int64(datagramBytes) {
			t.Fatalf("unexpected external access log: %#v", fields)
		}
	}
	metrics := app.access.snapshot()
	if len(metrics) != 1 {
		t.Fatalf("external access metrics = %#v, want one series", metrics)
	}
	metric := metrics[0]
	if network == "tcp" && (metric.Connections != 2 || metric.Datagrams != 0 || metric.Bytes != 0) {
		t.Fatalf("external access metric lost denied connections: %#v", metric)
	}
	if network == "udp" && (metric.Connections != 0 || metric.Datagrams != 2 || metric.Bytes != 256) {
		t.Fatalf("external access metric lost denied datagrams: %#v", metric)
	}
	page, err := database.ListAudit(t.Context(), store.AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("external access denial leaked into audit: %#v", page.Items)
	}
}
