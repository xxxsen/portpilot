package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
)

type clientUDPChannel struct {
	session *controlSession
	request protocol.OpenUDPChannel
	conn    net.Conn
	target  *net.UDPAddr
	params  protocol.UDPParameters
	send    chan protocol.UDPFrame
	done    chan struct{}
	flowsMu sync.Mutex
	flows   map[uint64]*clientUDPFlow
	closed  atomic.Bool
}

type clientUDPFlow struct {
	id       uint64
	conn     *net.UDPConn
	lastSeen atomic.Int64
}

func newUDPChannel(session *controlSession, request protocol.OpenUDPChannel, conn net.Conn, params protocol.UDPParameters) (*clientUDPChannel, error) {
	target, err := net.ResolveUDPAddr("udp", request.Connect)
	if err != nil {
		return nil, fmt.Errorf("resolve UDP target %s: %w", request.Connect, err)
	}
	normalized := normalizedUDP(params)
	return &clientUDPChannel{
		session: session, request: request, conn: conn, params: normalized,
		target: target, flows: make(map[uint64]*clientUDPFlow),
		send: make(chan protocol.UDPFrame, normalized.ChannelQueueDatagrams),
		done: make(chan struct{}),
	}, nil
}

func (c *clientUDPChannel) Run() {
	defer c.Close()
	go c.writeLoop()
	go c.reaper()
	for {
		frame, err := protocol.ReadUDPFrame(c.conn, c.params.MaxDatagramSize)
		if err != nil {
			if !errors.Is(err, io.EOF) && !c.closed.Load() {
				c.session.app.Logger.Warn("UDP channel read failed", "error", err)
			}
			return
		}
		switch frame.Type {
		case protocol.UDPTypeDataToClient:
			c.forward(frame)
		case protocol.UDPTypeFlowClose:
			c.removeFlow(frame.FlowID)
		default:
			return
		}
	}
}

func (c *clientUDPChannel) forward(frame protocol.UDPFrame) {
	c.flowsMu.Lock()
	flow := c.flows[frame.FlowID]
	if flow == nil {
		if c.session.ctx.Err() != nil {
			c.flowsMu.Unlock()
			return
		}
		if len(c.flows) >= c.params.MaxFlowsPerProxy {
			c.flowsMu.Unlock()
			return
		}
		conn, err := net.DialUDP("udp", nil, c.target)
		if err != nil {
			c.flowsMu.Unlock()
			return
		}
		flow = &clientUDPFlow{id: frame.FlowID, conn: conn}
		flow.lastSeen.Store(time.Now().UnixNano())
		c.flows[frame.FlowID] = flow
		go c.readResponses(flow)
	}
	flow.lastSeen.Store(time.Now().UnixNano())
	c.flowsMu.Unlock()
	_, _ = flow.conn.Write(frame.Data)
}

func (c *clientUDPChannel) readResponses(flow *clientUDPFlow) {
	buffer := make([]byte, c.params.MaxDatagramSize)
	for {
		n, err := flow.conn.Read(buffer)
		if err != nil {
			return
		}
		flow.lastSeen.Store(time.Now().UnixNano())
		frame := protocol.UDPFrame{
			Type: protocol.UDPTypeDataToServer, FlowID: flow.id,
			Data: append([]byte(nil), buffer[:n]...),
		}
		select {
		case c.send <- frame:
		default:
			c.session.app.Logger.Warn("UDP client channel queue full", "proxy_id", c.request.ProxyID)
		}
	}
}

func (c *clientUDPChannel) reaper() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.session.dataCtx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
			now := time.Now()
			var expired []uint64
			c.flowsMu.Lock()
			for id, flow := range c.flows {
				if now.Sub(time.Unix(0, flow.lastSeen.Load())) > durationOr(c.params.FlowIdleTimeout, 60*time.Second) {
					delete(c.flows, id)
					expired = append(expired, id)
					_ = flow.conn.Close()
				}
			}
			c.flowsMu.Unlock()
			for _, id := range expired {
				select {
				case c.send <- protocol.UDPFrame{Type: protocol.UDPTypeFlowClose, FlowID: id}:
				default:
				}
			}
		}
	}
}

func (c *clientUDPChannel) writeLoop() {
	for {
		select {
		case <-c.session.dataCtx.Done():
			return
		case <-c.done:
			return
		case frame := <-c.send:
			if err := protocol.WriteUDPFrame(c.conn, frame, c.params.MaxDatagramSize); err != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *clientUDPChannel) removeFlow(id uint64) {
	c.flowsMu.Lock()
	flow := c.flows[id]
	delete(c.flows, id)
	c.flowsMu.Unlock()
	if flow != nil {
		_ = flow.conn.Close()
	}
}

func (c *clientUDPChannel) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	close(c.done)
	_ = c.conn.Close()
	c.flowsMu.Lock()
	for _, flow := range c.flows {
		_ = flow.conn.Close()
	}
	c.flows = make(map[uint64]*clientUDPFlow)
	c.flowsMu.Unlock()
}
