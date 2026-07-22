package server

import (
	"testing"

	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
)

func TestProxyAvailabilityPriorityAndTransport(t *testing.T) {
	baseClient := config.ClientConfig{ClientID: "client-a", Enabled: true}
	baseProxy := config.ProxyEnvelope{ProxyID: "proxy-a", Enabled: true}
	listening := ListenerObservation{State: "listening"}
	tests := []struct {
		name       string
		client     config.ClientConfig
		proxy      config.ProxyEnvelope
		data       config.TunnelData
		exists     bool
		listener   ListenerObservation
		online     bool
		session    SessionStatus
		fallback   bool
		wantState  string
		wantReason string
		wantCarry  string
	}{
		{"client disabled", config.ClientConfig{ClientID: "client-a"}, baseProxy, config.TunnelData{Transport: "auto"}, true, listening, true, SessionStatus{State: "ACTIVE", MuxSessions: 1}, true, "disabled", "client_disabled", "auto"},
		{"proxy disabled", baseClient, config.ProxyEnvelope{ProxyID: "proxy-a"}, config.TunnelData{Transport: "auto"}, true, listening, true, SessionStatus{State: "ACTIVE", MuxSessions: 1}, true, "disabled", "proxy_disabled", "auto"},
		{"listener missing", baseClient, baseProxy, config.TunnelData{Transport: "auto"}, false, ListenerObservation{}, true, SessionStatus{State: "ACTIVE", MuxSessions: 1}, true, "error", "listener_missing", "auto"},
		{"listener failed", baseClient, baseProxy, config.TunnelData{Transport: "auto"}, true, ListenerObservation{State: "error"}, false, SessionStatus{}, true, "error", "listener_failed", "auto"},
		{"offline", baseClient, baseProxy, config.TunnelData{Transport: "auto"}, true, listening, false, SessionStatus{}, true, "waiting_client", "client_offline", "auto"},
		{"session degraded", baseClient, baseProxy, config.TunnelData{Transport: "auto"}, true, listening, true, SessionStatus{State: "DEGRADED"}, true, "degraded", "client_degraded", "auto"},
		{"session draining", baseClient, baseProxy, config.TunnelData{Transport: "auto"}, true, listening, true, SessionStatus{State: "DRAINING"}, true, "degraded", "client_draining", "auto"},
		{"udp without mux", baseClient, baseProxy, config.TunnelData{Network: "udp", Transport: "smux-datagram"}, true, listening, true, SessionStatus{State: "ACTIVE"}, true, "degraded", "mux_unavailable", "smux-datagram"},
		{"strict smux without mux", baseClient, baseProxy, config.TunnelData{Network: "tcp", Transport: "smux"}, true, listening, true, SessionStatus{State: "ACTIVE"}, true, "degraded", "mux_unavailable", "smux"},
		{"auto fallback disabled", baseClient, baseProxy, config.TunnelData{Network: "tcp", Transport: "auto"}, true, listening, true, SessionStatus{State: "ACTIVE"}, false, "degraded", "mux_unavailable", "auto"},
		{"auto direct fallback", baseClient, baseProxy, config.TunnelData{Network: "tcp", Transport: "auto"}, true, listening, true, SessionStatus{State: "ACTIVE"}, true, "ready", "ready", "direct_fallback"},
		{"direct ready", baseClient, baseProxy, config.TunnelData{Network: "tcp", Transport: "direct"}, true, listening, true, SessionStatus{State: "ACTIVE"}, false, "ready", "ready", "direct"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultServerConfig()
			cfg.DataPlane.DirectFallback = test.fallback
			service := &ProxyStatusService{app: &App{Config: cfg}}
			state, reason, transport := service.availability(
				test.client, test.proxy, test.data, test.exists, test.listener,
				test.online, test.session,
			)
			if state != test.wantState || reason != test.wantReason || transport != test.wantCarry {
				t.Fatalf("got %s/%s/%s want %s/%s/%s", state, reason, transport, test.wantState, test.wantReason, test.wantCarry)
			}
		})
	}
}

func TestHistoryUnavailableDoesNotMasqueradeAsZero(t *testing.T) {
	value := historyStatus(store.ProxyHistory{}, false)
	if value.Status != "unavailable" || value.Connections != nil ||
		value.Failures != nil || value.FailureRate != nil {
		t.Fatalf("unavailable history=%#v", value)
	}
}
