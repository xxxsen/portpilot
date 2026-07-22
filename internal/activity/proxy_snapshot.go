package activity

type ProxyRef struct {
	ClientID string
	ProxyID  string
}

type ProxySnapshot struct {
	ActiveConnections  int   `json:"active_connections"`
	PendingConnections int   `json:"pending_connections"`
	UpBytesPerSecond   int64 `json:"up_bytes_per_second"`
	DownBytesPerSecond int64 `json:"down_bytes_per_second"`
}

// ProxySnapshots aggregates every active handle in one registry traversal.
func (s *Service) ProxySnapshots() map[ProxyRef]ProxySnapshot {
	result := make(map[ProxyRef]ProxySnapshot)
	if s == nil {
		return result
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, handle := range s.active {
		if handle.closed.Load() {
			continue
		}
		key := ProxyRef{ClientID: handle.clientID, ProxyID: handle.proxyID}
		value := result[key]
		state := dereference(handle.state.Load())
		if state == "accepted" || state == "connecting" {
			value.PendingConnections++
		} else {
			value.ActiveConnections++
		}
		value.UpBytesPerSecond += handle.upBytesPerSecond.Load()
		value.DownBytesPerSecond += handle.downBytesPerSecond.Load()
		result[key] = value
	}
	return result
}
