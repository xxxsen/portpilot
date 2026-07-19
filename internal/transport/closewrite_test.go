package transport

import (
	"bufio"
	"net"
	"sync/atomic"
	"testing"
)

type transportCloseWriter struct {
	net.Conn
	called atomic.Bool
}

func (c *transportCloseWriter) CloseWrite() error {
	c.called.Store(true)
	return nil
}

func TestBufferedConnPreservesCloseWrite(t *testing.T) {
	t.Parallel()
	left, right := net.Pipe()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	underlying := &transportCloseWriter{Conn: left}
	conn := &bufferedConn{Conn: underlying, reader: bufio.NewReader(underlying)}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !underlying.called.Load() {
		t.Fatal("buffered connection hid the underlying CloseWrite")
	}
}
