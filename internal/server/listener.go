package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

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

type ListenerObservation struct {
	State         string `json:"state"`
	BoundAt       int64  `json:"bound_at"`
	LastAcceptAt  int64  `json:"last_accept_at"`
	FailedAt      int64  `json:"failed_at"`
	LastErrorCode string `json:"last_error_code"`
	LastError     string `json:"last_error"`
}

type ListenerHandle struct {
	runtime       atomic.Pointer[ProxyRuntime]
	state         atomic.Pointer[string]
	boundAt       atomic.Int64
	lastAcceptAt  atomic.Int64
	failedAt      atomic.Int64
	lastErrorCode atomic.Pointer[string]
	lastError     atomic.Pointer[string]
	tcp           net.Listener
	udp           *net.UDPConn
	closed        atomic.Bool
}

type ListenerManager struct {
	app                *App
	mu                 sync.RWMutex
	handles            map[string]*ListenerHandle
	unexpectedFailures atomic.Uint64
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
	for key, runtime := range desired {
		if existing := m.handles[key]; existing != nil {
			current := existing.runtime.Load()
			if current != nil && current.Data.Network == runtime.Data.Network && current.Data.Bind == runtime.Data.Bind {
				reused[key] = existing
				continue
			}
		}
		handle, bindErr := m.bind(runtime)
		if bindErr != nil {
			for _, item := range staged {
				item.close()
			}
			m.mu.Unlock()
			return nil, nil, fmt.Errorf("bind proxy %s: %w", key, bindErr)
		}
		staged[key] = handle
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
		for key, runtime := range desired {
			if handle := reused[key]; handle != nil {
				copy := runtime
				handle.runtime.Store(&copy)
				next[key] = handle
				continue
			}
			handle := staged[key]
			next[key] = handle
			go m.serve(handle)
		}
		previous := m.handles
		m.handles = next
		for key, handle := range previous {
			if next[key] != handle {
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
	handle.setState("stopped")
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
	handle.boundAt.Store(time.Now().UTC().UnixMilli())
	handle.setState("listening")
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
				m.unexpectedFailures.Add(1)
				handle.fail("listener_failed", err)
				m.app.Logger.Error("proxy TCP accept failed", zap.Error(err))
			}
			return
		}
		handle.lastAcceptAt.Store(time.Now().UTC().UnixMilli())
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
				m.unexpectedFailures.Add(1)
				handle.fail("listener_failed", err)
				m.app.Logger.Error("proxy UDP read failed", zap.Error(err))
			}
			return
		}
		handle.lastAcceptAt.Store(time.Now().UTC().UnixMilli())
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

func (m *ListenerManager) UDPListener(clientID, proxyID string) *net.UDPConn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	handle := m.handles[proxyRuntimeKey(clientID, proxyID)]
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

func (m *ListenerManager) UnexpectedFailures() uint64 {
	return m.unexpectedFailures.Load()
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
	h.setState("stopped")
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

func (h *ListenerHandle) setState(value string) {
	h.state.Store(&value)
}

func (h *ListenerHandle) fail(code string, err error) {
	message := err.Error()
	if len(message) > 256 {
		message = message[:256]
	}
	h.failedAt.Store(time.Now().UTC().UnixMilli())
	h.lastErrorCode.Store(&code)
	h.lastError.Store(&message)
	h.setState("error")
}

func (h *ListenerHandle) observation() ListenerObservation {
	value := ListenerObservation{
		State: "stopped", BoundAt: h.boundAt.Load(), LastAcceptAt: h.lastAcceptAt.Load(),
		FailedAt: h.failedAt.Load(),
	}
	if state := h.state.Load(); state != nil {
		value.State = *state
	}
	if code := h.lastErrorCode.Load(); code != nil {
		value.LastErrorCode = *code
	}
	if message := h.lastError.Load(); message != nil {
		value.LastError = *message
	}
	return value
}

func (m *ListenerManager) ObservationSnapshot() map[string]ListenerObservation {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]ListenerObservation, len(m.handles))
	for key, handle := range m.handles {
		result[key] = handle.observation()
	}
	return result
}

func proxyRuntimeKey(clientID, proxyID string) string {
	return clientID + "\x00" + proxyID
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
			result[proxyRuntimeKey(client.ClientID, proxy.ProxyID)] = ProxyRuntime{
				ClientID: client.ClientID, ClientRemark: client.Remark,
				Proxy: proxy, Data: data, Access: accesspolicy.Compile(data.AccessPolicy),
			}
		}
	}
	return result, nil
}
