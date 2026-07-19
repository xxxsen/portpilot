package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
)

const (
	DefaultMaxJSONFrame = 64 * 1024
	UDPHeaderSize       = 18
	UDPTypeDataToClient = 1
	UDPTypeDataToServer = 2
	UDPTypeFlowClose    = 3
)

type MuxAuth struct {
	Version      int    `json:"version"`
	Role         string `json:"role"`
	ClientID     string `json:"client_id"`
	SessionID    string `json:"session_id"`
	SessionEpoch uint64 `json:"session_epoch"`
	MuxID        string `json:"mux_id"`
	MuxTicket    string `json:"mux_ticket"`
}

type StreamOpen struct {
	Version       int    `json:"version"`
	Role          string `json:"role"`
	ClientID      string `json:"client_id"`
	SessionID     string `json:"session_id"`
	SessionEpoch  uint64 `json:"session_epoch"`
	MuxID         string `json:"mux_id,omitempty"`
	ConnectionID  string `json:"connection_id,omitempty"`
	DataTicket    string `json:"data_ticket,omitempty"`
	ProxyID       string `json:"proxy_id,omitempty"`
	ChannelID     string `json:"channel_id,omitempty"`
	ChannelTicket string `json:"udp_channel_ticket,omitempty"`
}

type HandshakeResult struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func WriteJSONFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode JSON frame: %w", err)
	}
	if len(data) > DefaultMaxJSONFrame {
		return errors.New("JSON frame exceeds maximum")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data))) //nolint:gosec // JSON frames are capped at 64 KiB above.
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func ReadJSONFrame(r io.Reader, max int, target any) error {
	if max <= 0 {
		max = DefaultMaxJSONFrame
	}
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > uint32(max) {
		return fmt.Errorf("invalid JSON frame length %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode JSON frame: %w", err)
	}
	return nil
}

type UDPFrame struct {
	Version uint8
	Type    uint8
	Flags   uint16
	FlowID  uint64
	Addr    *net.UDPAddr
	Data    []byte
}

func WriteUDPFrame(w io.Writer, frame UDPFrame, maxDatagram int) error {
	if frame.Version == 0 {
		frame.Version = Version
	}
	if frame.Type < UDPTypeDataToClient || frame.Type > UDPTypeFlowClose {
		return errors.New("invalid UDP frame type")
	}
	if len(frame.Data) > maxDatagram {
		return errors.New("UDP datagram exceeds maximum")
	}
	addr, err := encodeUDPAddr(frame.Addr)
	if err != nil {
		return err
	}
	if frame.Type != UDPTypeDataToClient && len(addr) != 0 {
		return errors.New("address is only valid for DATA_TO_CLIENT")
	}
	var header [UDPHeaderSize]byte
	header[0] = frame.Version
	header[1] = frame.Type
	binary.BigEndian.PutUint16(header[2:4], frame.Flags)
	binary.BigEndian.PutUint64(header[4:12], frame.FlowID)
	binary.BigEndian.PutUint16(header[12:14], uint16(len(addr)))       //nolint:gosec // Encoded UDP addresses are at most 19 bytes.
	binary.BigEndian.PutUint32(header[14:18], uint32(len(frame.Data))) //nolint:gosec // UDP datagrams are capped by validated configuration.
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(addr) > 0 {
		if _, err := w.Write(addr); err != nil {
			return err
		}
	}
	if len(frame.Data) > 0 {
		_, err = w.Write(frame.Data)
	}
	return err
}

func ReadUDPFrame(r io.Reader, maxDatagram int) (UDPFrame, error) {
	var header [UDPHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return UDPFrame{}, err
	}
	if header[0] != Version {
		return UDPFrame{}, fmt.Errorf("unsupported UDP frame version %d", header[0])
	}
	kind := header[1]
	if kind < UDPTypeDataToClient || kind > UDPTypeFlowClose {
		return UDPFrame{}, fmt.Errorf("invalid UDP frame type %d", kind)
	}
	addrLen := int(binary.BigEndian.Uint16(header[12:14]))
	dataLen := int(binary.BigEndian.Uint32(header[14:18]))
	if addrLen > 19 || dataLen > maxDatagram || dataLen < 0 {
		return UDPFrame{}, errors.New("invalid UDP frame lengths")
	}
	addrData := make([]byte, addrLen)
	if _, err := io.ReadFull(r, addrData); err != nil {
		return UDPFrame{}, err
	}
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return UDPFrame{}, err
	}
	addr, err := decodeUDPAddr(addrData)
	if err != nil {
		return UDPFrame{}, err
	}
	if kind != UDPTypeDataToClient && addr != nil {
		return UDPFrame{}, errors.New("unexpected UDP address")
	}
	return UDPFrame{
		Version: header[0], Type: kind,
		Flags:  binary.BigEndian.Uint16(header[2:4]),
		FlowID: binary.BigEndian.Uint64(header[4:12]),
		Addr:   addr, Data: data,
	}, nil
}

func encodeUDPAddr(addr *net.UDPAddr) ([]byte, error) {
	if addr == nil {
		return []byte{}, nil
	}
	if addr.Port < 0 || addr.Port > 65535 {
		return nil, errors.New("invalid UDP port")
	}
	if ip4 := addr.IP.To4(); ip4 != nil {
		data := make([]byte, 7)
		data[0] = 4
		copy(data[1:5], ip4)
		binary.BigEndian.PutUint16(data[5:7], uint16(addr.Port))
		return data, nil
	}
	ip6 := addr.IP.To16()
	if ip6 == nil {
		return nil, errors.New("invalid UDP IP")
	}
	data := make([]byte, 19)
	data[0] = 6
	copy(data[1:17], ip6)
	binary.BigEndian.PutUint16(data[17:19], uint16(addr.Port))
	return data, nil
}

func decodeUDPAddr(data []byte) (*net.UDPAddr, error) {
	if len(data) == 0 {
		return nil, nil //nolint:nilnil // An omitted address is valid for client-to-server frames.
	}
	switch data[0] {
	case 4:
		if len(data) != 7 {
			return nil, errors.New("invalid IPv4 UDP address")
		}
		return &net.UDPAddr{IP: net.IP(data[1:5]), Port: int(binary.BigEndian.Uint16(data[5:7]))}, nil
	case 6:
		if len(data) != 19 {
			return nil, errors.New("invalid IPv6 UDP address")
		}
		return &net.UDPAddr{IP: net.IP(data[1:17]), Port: int(binary.BigEndian.Uint16(data[17:19]))}, nil
	default:
		return nil, errors.New("invalid UDP address family")
	}
}
