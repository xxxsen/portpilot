package server

import (
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"go.uber.org/zap"
)

type externalAccessMetricKey struct {
	Network string
	ProxyID string
	Reason  string
}

type externalAccessMetric struct {
	Network     string `json:"network"`
	ProxyID     string `json:"proxy_id"`
	Reason      string `json:"reason"`
	Connections uint64 `json:"connections"`
	Datagrams   uint64 `json:"datagrams"`
	Bytes       uint64 `json:"bytes"`
}

type externalAccessMetrics struct {
	mu     sync.Mutex
	values map[externalAccessMetricKey]*externalAccessMetric
}

func (m *externalAccessMetrics) observe(
	network, proxyID, reason string,
	datagramBytes int,
) {
	key := externalAccessMetricKey{
		Network: network, ProxyID: proxyID, Reason: reason,
	}
	m.mu.Lock()
	if m.values == nil {
		m.values = make(map[externalAccessMetricKey]*externalAccessMetric)
	}
	value := m.values[key]
	if value == nil {
		value = &externalAccessMetric{
			Network: network, ProxyID: proxyID, Reason: reason,
		}
		m.values[key] = value
	}
	if network == "tcp" {
		value.Connections++
	} else {
		value.Datagrams++
		if datagramBytes > 0 {
			value.Bytes += uint64(datagramBytes)
		}
	}
	m.mu.Unlock()
}

func (m *externalAccessMetrics) snapshot() []externalAccessMetric {
	m.mu.Lock()
	result := make([]externalAccessMetric, 0, len(m.values))
	for _, value := range m.values {
		result = append(result, *value)
	}
	m.mu.Unlock()
	sort.Slice(result, func(left, right int) bool {
		if result[left].Network != result[right].Network {
			return result[left].Network < result[right].Network
		}
		if result[left].ProxyID != result[right].ProxyID {
			return result[left].ProxyID < result[right].ProxyID
		}
		return result[left].Reason < result[right].Reason
	})
	return result
}

type externalAccessDecision struct {
	Allowed  bool
	SourceIP string
	Reason   string
}

func (a *App) allowExternal(runtime ProxyRuntime, remote net.Addr) externalAccessDecision {
	address, err := accesspolicy.NormalizeAddr(remote)
	if err != nil {
		return externalAccessDecision{Reason: "source_invalid"}
	}
	return a.decideExternalAddress(runtime, address)
}

func (a *App) decideExternalAddress(
	runtime ProxyRuntime,
	address netip.Addr,
) externalAccessDecision {
	result := externalAccessDecision{SourceIP: address.String()}
	if allowed, reason := a.GlobalAccess.Decide(address); !allowed {
		result.Reason = "global_" + reason
		return result
	}
	if allowed, reason := runtime.Access.Decide(address); !allowed {
		result.Reason = "proxy_" + reason
		return result
	}
	if runtime.Data.AllowKnockOnly {
		if a.Knock == nil {
			result.Reason = "knock_unavailable"
			return result
		}
		if _, ok := a.Knock.ValidGrant(address); !ok {
			result.Reason = "knock_required"
			return result
		}
	}
	result.Allowed = true
	return result
}

func (a *App) recordExternalAccessDenied(
	runtime ProxyRuntime,
	network string,
	decision externalAccessDecision,
	datagramBytes int,
) {
	reason := decision.Reason
	sourceIP := decision.SourceIP
	a.access.observe(network, runtime.Proxy.ProxyID, reason, datagramBytes)
	key := network + "\x00" + runtime.Proxy.ProxyID + "\x00" + sourceIP + "\x00" + reason
	if a.RuntimeLogLimiter != nil && !a.RuntimeLogLimiter.Allow(key) {
		return
	}
	layer := "source"
	reasonCode := reason
	if value, rest, found := strings.Cut(reason, "_"); found {
		layer = value
		reasonCode = rest
	}
	if layer == "knock" {
		reasonCode = reason
	}
	if a.Logger != nil {
		a.Logger.Warn(
			"external access denied",
			zap.String("network", network),
			zap.String("client_id", runtime.ClientID),
			zap.String("proxy_id", runtime.Proxy.ProxyID),
			zap.String("source_ip", sourceIP),
			zap.String("policy_layer", layer),
			zap.String("reason", reasonCode),
			zap.Int("datagram_bytes", datagramBytes),
		)
	}
}

func (a *App) handleAccessPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"source_mode": "socket_peer", "ingress_proxy_protocol": false,
		"global": a.Config.AccessPolicy,
	})
}
