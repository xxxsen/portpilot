package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"github.com/portpilot/portpilot/internal/config"
	"go.uber.org/zap"
)

type ProxyRuntime struct {
	ClientID     string
	ClientRemark string
	Proxy        config.ProxyEnvelope
	Data         config.TunnelData
	Access       accesspolicy.Policy
}

type ListenerHandle struct {
	runtime atomic.Pointer[ProxyRuntime]
	tcp     net.Listener
	udp     *net.UDPConn
	closed  atomic.Bool
}

type ListenerManager struct {
	app     *App
	mu      sync.RWMutex
	handles map[string]*ListenerHandle
}

func NewListenerManager(app *App) *ListenerManager {
	return &ListenerManager{app: app, handles: make(map[string]*ListenerHandle)}
}

func (m *ListenerManager) Prepare(file config.ClientFile) (func(), func(), error) {
	m.mu.Lock()
	desired, err := runtimeMap(file, m.app.Config.DataPlane.DefaultTransport)
	if err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	staged := make(map[string]*ListenerHandle)
	reused := make(map[string]*ListenerHandle)
	for proxyID, runtime := range desired {
		if existing := m.handles[proxyID]; existing != nil {
			current := existing.runtime.Load()
			if current != nil && current.Data.Network == runtime.Data.Network && current.Data.Bind == runtime.Data.Bind {
				reused[proxyID] = existing
				continue
			}
		}
		handle, bindErr := m.bind(runtime)
		if bindErr != nil {
			for _, item := range staged {
				item.close()
			}
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("bind proxy %s: %w", proxyID, bindErr)
		}
		staged[proxyID] = handle
	}
	var once sync.Once
	unlock := func() { once.Do(m.mu.Unlock) }
	abort := func() {
		for _, handle := range staged {
			handle.close()
		}
		unlock()
	}
	commit := func() {
		next := make(map[string]*ListenerHandle, len(desired))
		for proxyID, runtime := range desired {
			if handle := reused[proxyID]; handle != nil {
				copy := runtime
				handle.runtime.Store(&copy)
				next[proxyID] = handle
				continue
			}
			handle := staged[proxyID]
			next[proxyID] = handle
			go m.serve(handle)
		}
		previous := m.handles
		m.handles = next
		for proxyID, handle := range previous {
			if next[proxyID] != handle {
				handle.close()
			}
		}
		unlock()
	}
	return commit, abort, nil
}

func (m *ListenerManager) bind(runtime ProxyRuntime) (*ListenerHandle, error) {
	copy := runtime
	handle := &ListenerHandle{}
	handle.runtime.Store(&copy)
	switch runtime.Data.Network {
	case "tcp":
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", runtime.Data.Bind)
		if err != nil {
			return nil, fmt.Errorf("listen TCP proxy %s: %w", runtime.Data.Bind, err)
		}
		handle.tcp = listener
	case "udp":
		address, err := net.ResolveUDPAddr("udp", runtime.Data.Bind)
		if err != nil {
			return nil, fmt.Errorf("resolve UDP proxy %s: %w", runtime.Data.Bind, err)
		}
		conn, err := net.ListenUDP("udp", address)
		if err != nil {
			return nil, fmt.Errorf("listen UDP proxy %s: %w", runtime.Data.Bind, err)
		}
		handle.udp = conn
	default:
		return nil, errors.New("unsupported network")
	}
	return handle, nil
}

func (m *ListenerManager) serve(handle *ListenerHandle) {
	if handle.tcp != nil {
		m.serveTCP(handle)
		return
	}
	m.serveUDP(handle)
}

func (m *ListenerManager) serveTCP(handle *ListenerHandle) {
	for {
		conn, err := handle.tcp.Accept()
		if err != nil {
			if !handle.closed.Load() {
				m.app.Logger.Error("proxy TCP accept failed", zap.Error(err))
			}
			return
		}
		runtime := handle.runtime.Load()
		if runtime == nil {
			_ = conn.Close()
			continue
		}
		copy := *runtime
		allowed, source := m.app.allowExternal(copy, conn.RemoteAddr())
		if !allowed {
			_ = conn.Close()
			m.app.recordExternalAccessDenied(
				copy, "tcp", source, 0,
			)
			continue
		}
		go m.app.Tunnels.HandleExternal(context.Background(), copy, conn)
	}
}

func (m *ListenerManager) serveUDP(handle *ListenerHandle) {
	max := m.app.Config.DataPlane.UDP.MaxDatagramSize
	buffer := make([]byte, max)
	for {
		n, address, err := handle.udp.ReadFromUDP(buffer)
		if err != nil {
			if !handle.closed.Load() {
				m.app.Logger.Error("proxy UDP read failed", zap.Error(err))
			}
			return
		}
		runtime := handle.runtime.Load()
		if runtime == nil {
			continue
		}
		data := append([]byte(nil), buffer[:n]...)
		copy := *runtime
		allowed, sourceIP := m.app.allowExternal(copy, address)
		if !allowed {
			m.app.recordExternalAccessDenied(
				copy, "udp", sourceIP, n,
			)
			continue
		}
		m.app.UDP.HandleDatagram(copy, handle.udp, address, data)
	}
}

func (m *ListenerManager) UDPListener(proxyID string) *net.UDPConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	handle := m.handles[proxyID]
	if handle == nil {
		return nil
	}
	return handle.udp
}

func (m *ListenerManager) Snapshot() []ProxyRuntime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ProxyRuntime, 0, len(m.handles))
	for _, handle := range m.handles {
		if runtime := handle.runtime.Load(); runtime != nil {
			result = append(result, *runtime)
		}
	}
	return result
}

func (m *ListenerManager) Close() {
	m.mu.Lock()
	handles := m.handles
	m.handles = make(map[string]*ListenerHandle)
	m.mu.Unlock()
	for _, handle := range handles {
		handle.close()
	}
}

func (h *ListenerHandle) close() {
	if !h.closed.CompareAndSwap(false, true) {
		return
	}
	if h.tcp != nil {
		_ = h.tcp.Close()
	}
	if h.udp != nil {
		_ = h.udp.Close()
	}
}

func runtimeMap(file config.ClientFile, defaultTransport string) (map[string]ProxyRuntime, error) {
	if defaultTransport == "" {
		defaultTransport = "auto"
	}
	result := make(map[string]ProxyRuntime)
	for _, client := range file.ClientConfig {
		if !client.Enabled {
			continue
		}
		for _, proxy := range client.ProxyList {
			if !proxy.Enabled {
				continue
			}
			data, err := proxy.Tunnel()
			if err != nil {
				return nil, fmt.Errorf("parse proxy %s: %w", proxy.ProxyID, err)
			}
			if data.Network == "tcp" && !data.TransportExplicit {
				data.Transport = defaultTransport
			}
			result[proxy.ProxyID] = ProxyRuntime{
				ClientID: client.ClientID, ClientRemark: client.Remark,
				Proxy: proxy, Data: data, Access: accesspolicy.Compile(data.AccessPolicy),
			}
		}
	}
	return result, nil
}
