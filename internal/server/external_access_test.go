package server

import (
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
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

func TestExternalAccessDeniedWritesRateLimitedLogAndExactMetricOnly(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	core, observed := observer.New(zap.WarnLevel)
	app := &App{
		Events: database, Logger: zap.New(core),
		RuntimeLogLimiter: auth.NewRateLimiter(1, time.Minute),
		GlobalAccess: accesspolicy.Compile(config.AccessPolicyConfig{
			DenyCIDRs: []string{"192.0.2.0/24"},
		}),
	}
	runtime := ProxyRuntime{
		ClientID: "client-a",
		Proxy:    config.ProxyEnvelope{ProxyID: "proxy-a"},
	}
	app.recordExternalAccessDenied(runtime, "tcp", "192.0.2.10", 0)
	app.recordExternalAccessDenied(runtime, "tcp", "192.0.2.10", 0)

	logs := observed.FilterMessage("external access denied").All()
	if len(logs) != 1 {
		t.Fatalf("external access logs = %d, want 1", len(logs))
	}
	fields := logs[0].ContextMap()
	if fields["client_id"] != "client-a" || fields["proxy_id"] != "proxy-a" ||
		fields["source_ip"] != "192.0.2.10" || fields["reason"] != "deny" {
		t.Fatalf("unexpected external access log: %#v", fields)
	}
	metrics := app.access.snapshot()
	if len(metrics) != 1 || metrics[0].Connections != 2 {
		t.Fatalf("external access metric lost denied connections: %#v", metrics)
	}
	page, err := database.ListAudit(t.Context(), store.AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("external access denial leaked into audit: %#v", page.Items)
	}
}
