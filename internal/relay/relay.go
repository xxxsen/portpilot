package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type closeWriter interface {
	CloseWrite() error
}

type Result struct {
	UpBytes   int64
	DownBytes int64
	Err       error
}

type countingWriter struct {
	writer  io.Writer
	count   *atomic.Int64
	observe func(int)
}

func (w countingWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.count.Add(int64(n))
	if n > 0 && w.observe != nil {
		w.observe(n)
	}
	return n, err
}

type deadlineReader struct {
	conn net.Conn
	idle time.Duration
}

func (r deadlineReader) Read(data []byte) (int, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(r.idle))
	return r.conn.Read(data)
}

type deadlineWriter struct {
	conn net.Conn
	idle time.Duration
}

func (w deadlineWriter) Write(data []byte) (int, error) {
	_ = w.conn.SetWriteDeadline(time.Now().Add(w.idle))
	return w.conn.Write(data)
}

func Bidirectional(ctx context.Context, left, right io.ReadWriteCloser, idle time.Duration) Result {
	return BidirectionalObserved(ctx, left, right, idle, nil, nil)
}

func BidirectionalObserved(
	ctx context.Context,
	left, right io.ReadWriteCloser,
	idle time.Duration,
	onUp, onDown func(int),
) Result {
	var up atomic.Int64
	var down atomic.Int64
	errCh := make(chan error, 2)
	var closeOnce sync.Once
	closeBoth := func() {
		_ = left.Close()
		_ = right.Close()
	}
	copyOne := func(
		dst io.ReadWriteCloser,
		src io.ReadWriteCloser,
		count *atomic.Int64,
		observe func(int),
	) {
		var reader io.Reader = src
		var writer io.Writer = dst
		if idle > 0 {
			if conn, ok := src.(net.Conn); ok {
				reader = deadlineReader{conn: conn, idle: idle}
			}
			if conn, ok := dst.(net.Conn); ok {
				writer = deadlineWriter{conn: conn, idle: idle}
			}
		}
		buffer := make([]byte, 32*1024)
		_, err := io.CopyBuffer(
			countingWriter{writer: writer, count: count, observe: observe},
			reader,
			buffer,
		)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		errCh <- err
	}
	go copyOne(right, left, &up, onUp)
	go copyOne(left, right, &down, onDown)

	var firstErr error
	completed := 0
	for completed < 2 {
		select {
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			closeOnce.Do(closeBoth)
		case copyErr := <-errCh:
			completed++
			if copyErr != nil && firstErr == nil {
				firstErr = copyErr
				closeOnce.Do(closeBoth)
			}
		}
	}
	closeOnce.Do(closeBoth)
	if errors.Is(firstErr, net.ErrClosed) || errors.Is(firstErr, context.Canceled) {
		firstErr = nil
	}
	return Result{UpBytes: up.Load(), DownBytes: down.Load(), Err: firstErr}
}
