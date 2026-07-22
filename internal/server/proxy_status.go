package server

import (
	"context"
	"sync"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
)

type ProxySessionStatus struct {
	Current          bool   `json:"current"`
	State            string `json:"state"`
	LastSeenAt       int64  `json:"last_seen_at"`
	ControlRTTMillis int64  `json:"control_rtt_millis"`
	MuxSessions      int    `json:"mux_sessions"`
	ActiveStreams    int64  `json:"active_streams"`
}

type ProxyHistoryStatus struct {
	Status                string   `json:"status"`
	Range                 string   `json:"range"`
	Connections           *int64   `json:"connections"`
	Failures              *int64   `json:"failures"`
	FailureRate           *float64 `json:"failure_rate"`
	UpBytes               *int64   `json:"up_bytes"`
	DownBytes             *int64   `json:"down_bytes"`
	LastActivityAt        *int64   `json:"last_activity_at"`
	LastFailureAt         *int64   `json:"last_failure_at"`
	LastFailureReasonCode string   `json:"last_failure_reason_code"`
}

type ProxyRuntimeStatus struct {
	ClientID           string                 `json:"client_id"`
	ClientRemark       string                 `json:"client_remark"`
	ProxyID            string                 `json:"proxy_id"`
	ProxyRemark        string                 `json:"proxy_remark"`
	Enabled            bool                   `json:"enabled"`
	Protocol           string                 `json:"protocol"`
	Transport          string                 `json:"transport"`
	EffectiveTransport string                 `json:"effective_transport"`
	Bind               string                 `json:"bind"`
	Connect            string                 `json:"connect"`
	Availability       string                 `json:"availability"`
	ReasonCode         string                 `json:"reason_code"`
	Listener           ListenerObservation    `json:"listener"`
	Session            ProxySessionStatus     `json:"session"`
	Realtime           activity.ProxySnapshot `json:"realtime"`
	History            ProxyHistoryStatus     `json:"history"`
}

type ProxyStatusService struct {
	app *App
	now func() time.Time

	historyMu        sync.Mutex
	historyLoadedAt  time.Time
	history          map[store.ProxyHistoryRef]store.ProxyHistory
	historyAvailable bool
}

func NewProxyStatusService(app *App) *ProxyStatusService {
	return &ProxyStatusService{app: app, now: time.Now}
}

func (s *ProxyStatusService) Snapshot(ctx context.Context) (uint64, int64, []ProxyRuntimeStatus) {
	now := s.now().UTC()
	configFile := s.app.ConfigManager.Snapshot()
	listeners := s.app.Listeners.ObservationSnapshot()
	sessionValues := s.app.Sessions.Snapshot()
	sessions := make(map[string]SessionStatus, len(sessionValues))
	for _, session := range sessionValues {
		sessions[session.ClientID] = session
	}
	realtime := s.app.Activity.ProxySnapshots()
	history, historyAvailable := s.historySnapshot(ctx, now)
	offline := map[string]store.ClientAgentSnapshot{}
	if snapshots, err := s.app.Events.ListClientAgentSnapshots(ctx); err == nil {
		offline = snapshots
	}

	items := make([]ProxyRuntimeStatus, 0)
	for _, client := range configFile.ClientConfig {
		session, online := sessions[client.ClientID]
		if !online {
			if snapshot, ok := offline[client.ClientID]; ok {
				session = SessionStatus{
					ClientID: client.ClientID, State: "OFFLINE", Epoch: snapshot.LastSessionEpoch,
					LastSeenAt: snapshot.LastSeenAt,
				}
			}
		}
		for _, proxy := range client.ProxyList {
			data, err := proxy.Tunnel()
			if err != nil {
				continue
			}
			if data.Network == "tcp" && !data.TransportExplicit {
				data.Transport = s.app.Config.DataPlane.DefaultTransport
			}
			key := proxyRuntimeKey(client.ClientID, proxy.ProxyID)
			listener, listenerExists := listeners[key]
			if !listenerExists {
				listener = ListenerObservation{State: "stopped"}
			}
			value := ProxyRuntimeStatus{
				ClientID: client.ClientID, ClientRemark: client.Remark,
				ProxyID: proxy.ProxyID, ProxyRemark: proxy.Remark,
				Enabled:  client.Enabled && proxy.Enabled,
				Protocol: data.Network, Transport: data.Transport,
				EffectiveTransport: data.Transport, Bind: data.Bind, Connect: data.Connect,
				Listener: listener,
				Session: ProxySessionStatus{
					Current: online, State: session.State, LastSeenAt: session.LastSeenAt,
					ControlRTTMillis: session.ControlRTTMillis,
					MuxSessions:      session.MuxSessions, ActiveStreams: session.ActiveStreams,
				},
				Realtime: realtime[activity.ProxyRef{ClientID: client.ClientID, ProxyID: proxy.ProxyID}],
				History:  historyStatus(history[keyHistory(client.ClientID, proxy.ProxyID)], historyAvailable),
			}
			value.Availability, value.ReasonCode, value.EffectiveTransport = s.availability(
				client, proxy, data, listenerExists, listener, online, session,
			)
			items = append(items, value)
		}
	}
	return configFile.Revision, now.UnixMilli(), items
}

func (s *ProxyStatusService) availability(
	client config.ClientConfig,
	proxy config.ProxyEnvelope,
	data config.TunnelData,
	listenerExists bool,
	listener ListenerObservation,
	online bool,
	session SessionStatus,
) (string, string, string) {
	if !client.Enabled {
		return "disabled", "client_disabled", data.Transport
	}
	if !proxy.Enabled {
		return "disabled", "proxy_disabled", data.Transport
	}
	if !listenerExists {
		return "error", "listener_missing", data.Transport
	}
	if listener.State == "error" {
		return "error", "listener_failed", data.Transport
	}
	if !online {
		return "waiting_client", "client_offline", data.Transport
	}
	if session.State == "DEGRADED" || session.State == "DRAINING" {
		return "degraded", "client_" + lowerASCII(session.State), data.Transport
	}
	if data.Network == "udp" || data.Transport == "smux" {
		if session.MuxSessions == 0 {
			return "degraded", "mux_unavailable", data.Transport
		}
		return "ready", "ready", data.Transport
	}
	if data.Transport == "auto" && session.MuxSessions == 0 {
		if !s.app.Config.DataPlane.DirectFallback {
			return "degraded", "mux_unavailable", data.Transport
		}
		return "ready", "ready", "direct_fallback"
	}
	return "ready", "ready", data.Transport
}

func (s *ProxyStatusService) historySnapshot(
	ctx context.Context,
	now time.Time,
) (map[store.ProxyHistoryRef]store.ProxyHistory, bool) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	if !s.historyLoadedAt.IsZero() && now.Sub(s.historyLoadedAt) < 30*time.Second {
		return s.history, s.historyAvailable
	}
	values, err := s.app.Events.ProxyHistory24h(ctx, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
	s.historyLoadedAt = now
	if err != nil {
		s.history = nil
		s.historyAvailable = false
		return nil, false
	}
	s.history = values
	s.historyAvailable = true
	return values, true
}

func historyStatus(value store.ProxyHistory, available bool) ProxyHistoryStatus {
	result := ProxyHistoryStatus{Status: "unavailable", Range: "24h"}
	if !available {
		return result
	}
	result.Status = "ready"
	result.Connections = pointer(value.Connections)
	result.Failures = pointer(value.Failures)
	failureRate := float64(0)
	if value.Connections > 0 {
		failureRate = float64(value.Failures) / float64(value.Connections)
	}
	result.FailureRate = &failureRate
	result.UpBytes = pointer(value.UpBytes)
	result.DownBytes = pointer(value.DownBytes)
	result.LastActivityAt = pointer(value.LastActivityAt)
	result.LastFailureAt = pointer(value.LastFailureAt)
	result.LastFailureReasonCode = value.LastFailureReasonCode
	return result
}

func pointer(value int64) *int64 { return &value }

func keyHistory(clientID, proxyID string) store.ProxyHistoryRef {
	return store.ProxyHistoryRef{ClientID: clientID, ProxyID: proxyID}
}

func lowerASCII(value string) string {
	bytes := []byte(value)
	for index, character := range bytes {
		if character >= 'A' && character <= 'Z' {
			bytes[index] = character + ('a' - 'A')
		}
	}
	return string(bytes)
}
