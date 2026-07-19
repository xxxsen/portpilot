package protocol

import (
	"encoding/json"
	"fmt"
)

const Version = 1

const (
	TypeAuthInit         = "AUTH_INIT"
	TypeAuthChallenge    = "AUTH_CHALLENGE"
	TypeAuthProof        = "AUTH_PROOF"
	TypeAuthOK           = "AUTH_OK"
	TypeAuthError        = "AUTH_ERROR"
	TypePing             = "PING"
	TypePong             = "PONG"
	TypeCreateMux        = "CREATE_MUX_SESSION"
	TypeMuxReady         = "MUX_SESSION_READY"
	TypeMuxDrain         = "MUX_DRAIN"
	TypeMuxClosed        = "MUX_CLOSED"
	TypeOpenTCP          = "OPEN_TCP"
	TypeOpenResult       = "OPEN_RESULT"
	TypeOpenUDPChannel   = "OPEN_UDP_CHANNEL"
	TypeUDPChannelReady  = "UDP_CHANNEL_READY"
	TypeUDPChannelClosed = "UDP_CHANNEL_CLOSED"
	TypeCancelTunnel     = "CANCEL_TUNNEL"
	TypeTunnelClosed     = "TUNNEL_CLOSED"
	TypeSessionReplaced  = "SESSION_REPLACED"
	TypeServerDrain      = "SERVER_DRAIN"
	TypeError            = "ERROR"
)

const (
	ErrAuthFailed        = "AUTH_FAILED"
	ErrSessionStale      = "SESSION_STALE"
	ErrMuxTicketInvalid  = "MUX_TICKET_INVALID"
	ErrMuxSessionLimit   = "MUX_SESSION_LIMIT"
	ErrMuxSessionLost    = "MUX_SESSION_LOST"
	ErrDataTicketInvalid = "DATA_TICKET_INVALID"
	ErrUDPChannelInvalid = "UDP_CHANNEL_INVALID"
	ErrClientOffline     = "CLIENT_OFFLINE"
	ErrTargetDialFailed  = "TARGET_DIAL_FAILED"
	ErrResourceLimit     = "RESOURCE_LIMIT"
	ErrConfigConflict    = "CONFIG_CONFLICT"
	ErrInternal          = "INTERNAL_ERROR"
)

type Message struct {
	Version      int             `json:"version"`
	Type         string          `json:"type"`
	RequestID    string          `json:"request_id,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	SessionEpoch uint64          `json:"session_epoch,omitempty"`
	Sequence     uint64          `json:"sequence,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
}

func NewMessage(kind string, payload any) (Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode %s payload: %w", kind, err)
	}
	return Message{Version: Version, Type: kind, Payload: raw}, nil
}

func DecodePayload[T any](message Message) (T, error) {
	var payload T
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		return payload, fmt.Errorf("decode %s payload: %w", message.Type, err)
	}
	return payload, nil
}

type AuthInit struct {
	ClientID    string `json:"client_id"`
	Role        string `json:"role"`
	ClientNonce string `json:"client_nonce"`
}

type AuthChallenge struct {
	ChallengeID string `json:"challenge_id"`
	Challenge   string `json:"challenge"`
	ExpiresAt   string `json:"expires_at"`
}

type AuthProof struct {
	ChallengeID string `json:"challenge_id"`
	Signature   string `json:"signature"`
}

type AuthOK struct {
	SessionID    string       `json:"session_id"`
	SessionEpoch uint64       `json:"session_epoch"`
	Capabilities Capabilities `json:"capabilities"`
	DataPlane    DataPlane    `json:"data_plane"`
}

type Capabilities struct {
	DataTransports      []string `json:"data_transports"`
	SMuxProtocolVersion int      `json:"smux_protocol_version"`
}

type DataPlane struct {
	MinSessions          int            `json:"min_sessions"`
	MaxSessions          int            `json:"max_sessions"`
	MaxStreamsPerSession int            `json:"max_streams_per_session"`
	DirectFallback       bool           `json:"direct_fallback"`
	TargetDial           string         `json:"target_dial"`
	TCPIdle              string         `json:"tcp_idle"`
	TCPMaxLifetime       string         `json:"tcp_max_lifetime"`
	SMux                 SMuxParameters `json:"smux"`
	UDP                  UDPParameters  `json:"udp"`
}

type CreateMux struct {
	MuxID     string `json:"mux_id"`
	MuxTicket string `json:"mux_ticket"`
	ExpiresAt string `json:"expires_at"`
}

type MuxReady struct {
	MuxID string `json:"mux_id"`
}

type OpenTCP struct {
	ConnectionID        string `json:"connection_id"`
	ProxyID             string `json:"proxy_id"`
	Connect             string `json:"connect"`
	Transport           string `json:"transport"`
	DataTicket          string `json:"data_ticket"`
	AllowDirectFallback bool   `json:"allow_direct_fallback"`
	Deadline            string `json:"deadline"`
}

type OpenResult struct {
	ConnectionID    string `json:"connection_id"`
	Success         bool   `json:"success"`
	ErrorCode       string `json:"error_code,omitempty"`
	ActualTransport string `json:"actual_transport,omitempty"`
}

type OpenUDPChannel struct {
	ProxyID       string `json:"proxy_id"`
	ChannelID     string `json:"channel_id"`
	Connect       string `json:"connect"`
	ChannelTicket string `json:"udp_channel_ticket"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}
