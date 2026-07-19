package server

func (m *UDPManager) Counts() (channels, pending, flows int) {
	if m == nil {
		return 0, 0, 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, proxyChannels := range m.channels {
		channels += len(proxyChannels)
	}
	return channels, len(m.pending), len(m.byID)
}
