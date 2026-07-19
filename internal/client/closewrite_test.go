package client

import (
	"net"
	"sync/atomic"
	"testing"
)

type clientCloseWriter struct {
	net.Conn
	called atomic.Bool
}

func (c *clientCloseWriter) CloseWrite() error {
	c.called.Store(true)
	return nil
}

func TestTrackedConnPreservesCloseWrite(t *testing.T) {
	t.Parallel()
	left, right := net.Pipe()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	underlying := &clientCloseWriter{Conn: left}
	conn := &trackedConn{Conn: underlying, done: func() {}}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !underlying.called.Load() {
		t.Fatal("tracked connection hid the underlying CloseWrite")
	}
}
