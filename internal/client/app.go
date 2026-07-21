package client

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/relay"
	"github.com/portpilot/portpilot/internal/transport"
	"github.com/portpilot/portpilot/internal/version"
	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
)

type App struct {
	Config config.ClientRuntimeConfig
	Logger *zap.Logger
	Agent  protocol.Agent
	active atomic.Pointer[controlSession]
	authAt atomic.Int64
}

type controlSession struct {
	app        *App
	conn       *websocket.Conn
	id         string
	epoch      uint64
	parent     context.Context
	ctx        context.Context
	cancel     context.CancelFunc
	dataCtx    context.Context
	dataCancel context.CancelFunc
	sendMu     sync.Mutex
	sequence   atomic.Uint64
	flows      atomic.Int64
	tunnelMu   sync.Mutex
	pending    map[string]context.CancelFunc
	pool       *MuxPool
	targetDial time.Duration
	tcpIdle    time.Duration
	tcpMaxLife time.Duration
}

func New(configPath string, logger *zap.Logger) (*App, error) {
	cfg, err := config.LoadClient(configPath)
	if err != nil {
		return nil, fmt.Errorf("load client configuration: %w", err)
	}
	return NewFromConfig(cfg, logger)
}

func NewFromConfig(cfg config.ClientRuntimeConfig, logger *zap.Logger) (*App, error) {
	if logger == nil {
		logger = logutil.GetLogger(context.Background())
	}
	if !cfg.EnableTLS {
		logger.Warn("TLS is disabled; this is only safe for local development or a protected network")
	}
	hostname, _ := os.Hostname()
	return &App{
		Config: cfg, Logger: logger,
		Agent: protocol.Agent{
			Version: version.Version, Commit: version.Commit, BuildTime: version.BuildTime,
			GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
			Hostname: hostname, StartedAt: time.Now().UTC().UnixMilli(),
			ProtocolVersion: protocol.Version,
			Capabilities: []string{
				"tcp-smux", "tcp-direct", "udp-smux-datagram",
			},
		},
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	const (
		initialBackoff = 250 * time.Millisecond
		maxBackoff     = 15 * time.Second
		stableSession  = 30 * time.Second
	)
	backoff := initialBackoff
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		attemptStarted := time.Now()
		err := a.runSession(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		authenticatedAt := a.authAt.Load()
		if authenticatedAt >= attemptStarted.UnixNano() && time.Since(time.Unix(0, authenticatedAt)) >= stableSession {
			backoff = initialBackoff
		}
		a.Logger.Warn("control session disconnected", zap.Error(err), zap.Duration("retry_in", backoff))
		jitter := randomJitter(backoff / 2)
		timer := time.NewTimer(backoff + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(max)+1))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

func (a *App) runSession(parent context.Context) error {
	conn, err := a.dialControl(parent)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	handshakeTimeout := 10 * time.Second
	_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	clientNonce, err := auth.RandomToken(18)
	if err != nil {
		return fmt.Errorf("create client nonce: %w", err)
	}
	init, _ := protocol.NewMessage(protocol.TypeAuthInit, protocol.AuthInit{
		ClientID: a.Config.ClientID, Role: "control", ClientNonce: clientNonce,
		Agent: &a.Agent,
	})
	if err := conn.WriteJSON(init); err != nil {
		return err
	}
	var challengeMessage protocol.Message
	if err := conn.ReadJSON(&challengeMessage); err != nil || challengeMessage.Type != protocol.TypeAuthChallenge {
		if err == nil {
			err = errors.New("server did not return auth challenge")
		}
		return err
	}
	challenge, err := protocol.DecodePayload[protocol.AuthChallenge](challengeMessage)
	if err != nil {
		return err
	}
	signature, err := auth.Signature(
		a.Config.ClientToken, a.Config.ClientID, "control",
		challenge.ChallengeID, challenge.Challenge, clientNonce,
	)
	if err != nil {
		return fmt.Errorf("sign authentication challenge: %w", err)
	}
	proof, _ := protocol.NewMessage(protocol.TypeAuthProof, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID, Signature: signature,
	})
	if err := conn.WriteJSON(proof); err != nil {
		return err
	}
	var authMessage protocol.Message
	if err := conn.ReadJSON(&authMessage); err != nil {
		return err
	}
	if authMessage.Type != protocol.TypeAuthOK {
		return errors.New(protocol.ErrAuthFailed)
	}
	authOK, err := protocol.DecodePayload[protocol.AuthOK](authMessage)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	dataCtx, dataCancel := context.WithCancel(context.WithoutCancel(parent))
	session := &controlSession{
		app: a, conn: conn, id: authOK.SessionID, epoch: authOK.SessionEpoch,
		parent: parent, ctx: ctx, cancel: cancel, dataCtx: dataCtx, dataCancel: dataCancel,
		pending:    make(map[string]context.CancelFunc),
		targetDial: durationOr(authOK.DataPlane.TargetDial, 10*time.Second),
		tcpIdle:    durationOr(authOK.DataPlane.TCPIdle, 10*time.Minute),
		tcpMaxLife: durationOr(authOK.DataPlane.TCPMaxLifetime, 24*time.Hour),
	}
	session.pool = NewMuxPool(session, authOK.DataPlane)
	a.active.Store(session)
	a.authAt.Store(time.Now().UnixNano())
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	defer func() {
		a.active.CompareAndSwap(session, nil)
		if parent.Err() != nil {
			session.drain(true)
		} else {
			go session.drain(false)
		}
	}()
	_ = conn.SetReadDeadline(time.Time{})
	sessionIDPrefix := session.id
	if len(sessionIDPrefix) > 8 {
		sessionIDPrefix = sessionIDPrefix[:8]
	}
	a.Logger.Info(
		"control session authenticated",
		zap.String("session_id_prefix", sessionIDPrefix),
		zap.Uint64("epoch", session.epoch),
	)
	return session.readLoop(ctx)
}

func (a *App) dialControl(ctx context.Context) (*websocket.Conn, error) {
	scheme := "ws"
	if a.Config.EnableTLS {
		scheme = "wss"
	}
	target := url.URL{Scheme: scheme, Host: a.Config.Server, Path: joinPath(a.Config.Path, "control")}
	var tlsConfig *tls.Config
	if a.Config.EnableTLS {
		var err error
		tlsConfig, err = transport.TLSClientConfig(a.Config.Server, a.Config.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("build control TLS configuration: %w", err)
		}
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext:   (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:  tlsConfig,
	}
	conn, response, err := dialer.DialContext(ctx, target.String(), http.Header{"User-Agent": []string{"portpilot-client/1"}})
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		return nil, fmt.Errorf("dial control websocket: %w", err)
	}
	return conn, nil
}

func (s *controlSession) readLoop(ctx context.Context) error {
	s.conn.SetReadLimit(64 * 1024)
	for {
		var message protocol.Message
		if err := s.conn.ReadJSON(&message); err != nil {
			return err
		}
		if message.Version != protocol.Version || message.SessionID != s.id || message.SessionEpoch != s.epoch {
			return errors.New(protocol.ErrSessionStale)
		}
		switch message.Type {
		case protocol.TypePing:
			pong, _ := protocol.NewMessage(protocol.TypePong, map[string]string{"time": time.Now().UTC().Format(time.RFC3339Nano)})
			pong.RequestID = message.RequestID
			if err := s.send(pong); err != nil {
				return err
			}
		case protocol.TypeCreateMux:
			request, err := protocol.DecodePayload[protocol.CreateMux](message)
			if err != nil {
				return err
			}
			go s.pool.Open(ctx, request)
		case protocol.TypeOpenTCP:
			request, err := protocol.DecodePayload[protocol.OpenTCP](message)
			if err != nil {
				return err
			}
			go s.openTCP(ctx, s.dataCtx, request)
		case protocol.TypeOpenUDPChannel:
			request, err := protocol.DecodePayload[protocol.OpenUDPChannel](message)
			if err != nil {
				return err
			}
			go s.openUDPChannel(request)
		case protocol.TypeCancelTunnel:
			payload, err := protocol.DecodePayload[struct {
				ConnectionID string `json:"connection_id"`
			}](message)
			if err != nil {
				return err
			}
			s.cancelTunnel(payload.ConnectionID)
		case protocol.TypeMuxDrain:
			payload, err := protocol.DecodePayload[protocol.MuxReady](message)
			if err != nil {
				return err
			}
			s.pool.Drain(payload.MuxID)
		case protocol.TypeSessionReplaced, protocol.TypeServerDrain:
			return errors.New(protocol.ErrSessionStale)
		case protocol.TypeError:
			payload, _ := protocol.DecodePayload[protocol.ErrorPayload](message)
			s.app.Logger.Warn("server control error", zap.String("code", payload.Code))
		default:
			return fmt.Errorf("unknown control message %q", message.Type)
		}
	}
}

func (s *controlSession) send(message protocol.Message) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	message.Version = protocol.Version
	message.SessionID = s.id
	message.SessionEpoch = s.epoch
	message.Sequence = s.sequence.Add(1)
	return s.conn.WriteJSON(message)
}

func (s *controlSession) openTCP(parent, dataCtx context.Context, request protocol.OpenTCP) {
	ctx, cancel := context.WithDeadline(parent, parseDeadline(request.Deadline, 15*time.Second))
	if !s.registerTunnel(request.ConnectionID, cancel) {
		cancel()
		return
	}
	defer func() {
		s.unregisterTunnel(request.ConnectionID)
		cancel()
	}()
	targetCtx, targetCancel := context.WithTimeout(ctx, s.targetDial)
	target, err := (&net.Dialer{}).DialContext(targetCtx, "tcp", request.Connect)
	targetCancel()
	if err != nil {
		s.sendOpenResult(request.ConnectionID, false, protocol.ErrTargetDialFailed, "")
		return
	}
	transportName := request.Transport
	if transportName == "" {
		transportName = "auto"
	}
	var dataConn net.Conn
	if transportName != "direct" {
		dataConn, err = s.openSMuxStream(request)
	}
	if transportName == "direct" || (transportName == "auto" && request.AllowDirectFallback && err != nil) {
		dataConn, err = s.openDirect(ctx, request)
		transportName = "direct"
	} else {
		transportName = "smux"
	}
	if err != nil {
		_ = target.Close()
		s.sendOpenResult(request.ConnectionID, false, protocol.ErrDataTicketInvalid, "")
		return
	}
	s.unregisterTunnel(request.ConnectionID)
	s.sendOpenResult(request.ConnectionID, true, "", transportName)
	s.flows.Add(1)
	defer s.flows.Add(-1)
	relayCtx, relayCancel := context.WithTimeout(dataCtx, s.tcpMaxLife)
	defer relayCancel()
	result := relay.Bidirectional(relayCtx, target, dataConn, s.tcpIdle)
	closed, _ := protocol.NewMessage(protocol.TypeTunnelClosed, map[string]any{
		"connection_id": request.ConnectionID, "up_bytes": result.UpBytes,
		"down_bytes": result.DownBytes,
	})
	_ = s.send(closed)
}

func (s *controlSession) openSMuxStream(request protocol.OpenTCP) (net.Conn, error) {
	mux := s.pool.Select()
	if mux == nil {
		return nil, errors.New("no healthy smux session")
	}
	stream, err := mux.session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("open smux stream: %w", err)
	}
	mux.active.Add(1)
	conn := &trackedConn{Conn: stream, done: func() { mux.active.Add(-1) }}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "tcp-stream", ClientID: s.app.Config.ClientID,
		SessionID: s.id, SessionEpoch: s.epoch, MuxID: mux.id,
		ConnectionID: request.ConnectionID, DataTicket: request.DataTicket,
	}
	if err := protocol.WriteJSONFrame(conn, open); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write smux stream handshake: %w", err)
	}
	var response protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read smux stream handshake: %w", err)
	}
	if response.Type != "STREAM_OK" {
		_ = conn.Close()
		return nil, fmt.Errorf("stream rejected: %s", response.Code)
	}
	return conn, nil
}

func (s *controlSession) openDirect(ctx context.Context, request protocol.OpenTCP) (net.Conn, error) {
	conn, err := transport.DialUpgrade(
		ctx, s.app.Config.Server, joinPath(s.app.Config.Path, "data/direct"),
		s.app.Config.EnableTLS, s.app.Config.TLSCAFile, 15*time.Second,
	)
	if err != nil {
		return nil, fmt.Errorf("dial direct data connection: %w", err)
	}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "direct", ClientID: s.app.Config.ClientID,
		SessionID: s.id, SessionEpoch: s.epoch,
		ConnectionID: request.ConnectionID, DataTicket: request.DataTicket,
	}
	if err := protocol.WriteJSONFrame(conn, open); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write direct stream handshake: %w", err)
	}
	var response protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read direct stream handshake: %w", err)
	}
	if response.Type != "STREAM_OK" {
		_ = conn.Close()
		return nil, fmt.Errorf("direct stream rejected: %s", response.Code)
	}
	return conn, nil
}

func (s *controlSession) sendOpenResult(connectionID string, success bool, code, actual string) {
	message, _ := protocol.NewMessage(protocol.TypeOpenResult, protocol.OpenResult{
		ConnectionID: connectionID, Success: success,
		ErrorCode: code, ActualTransport: actual,
	})
	_ = s.send(message)
}

func (s *controlSession) openUDPChannel(request protocol.OpenUDPChannel) {
	mux := s.pool.Select()
	if mux == nil {
		return
	}
	stream, err := mux.session.OpenStream()
	if err != nil {
		return
	}
	mux.active.Add(1)
	conn := &trackedConn{Conn: stream, done: func() { mux.active.Add(-1) }}
	open := protocol.StreamOpen{
		Version: protocol.Version, Role: "udp-channel",
		ClientID: s.app.Config.ClientID, SessionID: s.id, SessionEpoch: s.epoch,
		MuxID: mux.id, ProxyID: request.ProxyID, ChannelID: request.ChannelID,
		ChannelTicket: request.ChannelTicket,
	}
	if err := protocol.WriteJSONFrame(conn, open); err != nil {
		_ = conn.Close()
		return
	}
	var response protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &response); err != nil || response.Type != "STREAM_OK" {
		_ = conn.Close()
		return
	}
	channel, err := newUDPChannel(s, request, conn, s.pool.params.UDP)
	if err != nil {
		_ = conn.Close()
		return
	}
	s.flows.Add(1)
	defer s.flows.Add(-1)
	ready, _ := protocol.NewMessage(protocol.TypeUDPChannelReady, map[string]string{"proxy_id": request.ProxyID, "channel_id": request.ChannelID})
	_ = s.send(ready)
	channel.Run()
	closed, _ := protocol.NewMessage(protocol.TypeUDPChannelClosed, map[string]string{"proxy_id": request.ProxyID, "channel_id": request.ChannelID})
	_ = s.send(closed)
}

func (s *controlSession) registerTunnel(id string, cancel context.CancelFunc) bool {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.pending[id] = cancel
	return true
}

func (s *controlSession) unregisterTunnel(id string) {
	s.tunnelMu.Lock()
	delete(s.pending, id)
	s.tunnelMu.Unlock()
}

func (s *controlSession) cancelTunnel(id string) {
	s.tunnelMu.Lock()
	cancel := s.pending[id]
	delete(s.pending, id)
	s.tunnelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *controlSession) drain(immediate bool) {
	s.cancel()
	s.tunnelMu.Lock()
	for id, cancel := range s.pending {
		cancel()
		delete(s.pending, id)
	}
	s.tunnelMu.Unlock()
	s.pool.BeginDrain()
	if !immediate {
		timeout := durationOr(s.pool.params.SMux.DrainTimeout, 5*time.Minute)
		timer := time.NewTimer(timeout)
		ticker := time.NewTicker(50 * time.Millisecond)
		timedOut := false
		for s.flows.Load() > 0 && !timedOut {
			select {
			case <-timer.C:
				timedOut = true
			case <-ticker.C:
			}
		}
		if !timer.Stop() && !timedOut {
			select {
			case <-timer.C:
			default:
			}
		}
		ticker.Stop()
	}
	s.dataCancel()
	s.pool.Close()
}

func parseDeadline(value string, fallback time.Duration) time.Time {
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Now().Add(fallback)
	}
	return deadline
}

func joinPath(base, suffix string) string {
	return "/" + strings.Trim(base, "/") + "/" + strings.Trim(suffix, "/")
}

type trackedConn struct {
	net.Conn
	once sync.Once
	done func()
}

func (c *trackedConn) CloseWrite() error {
	if writer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return c.Close()
}

func (c *trackedConn) Close() error {
	c.once.Do(c.done)
	return c.Conn.Close()
}
