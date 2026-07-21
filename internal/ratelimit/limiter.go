package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

var ErrInvalidLimit = errors.New("rate limit must be non-negative")

type Limiter struct {
	mu      sync.Mutex
	limiter *rate.Limiter
	burst   int
	updated chan struct{}
	clock   clock
}

type Pair struct {
	Up   *Limiter
	Down *Limiter
}

func NewPair(up, down int64, udp bool, maxDatagram int) *Pair {
	return &Pair{
		Up:   New(up, udp, maxDatagram),
		Down: New(down, udp, maxDatagram),
	}
}

func New(bytesPerSecond int64, udp bool, maxDatagram int) *Limiter {
	return newWithClock(bytesPerSecond, udp, maxDatagram, realClock{})
}

func newWithClock(
	bytesPerSecond int64,
	udp bool,
	maxDatagram int,
	clock clock,
) *Limiter {
	limiter := &Limiter{updated: make(chan struct{}), clock: clock}
	_ = limiter.Update(bytesPerSecond, udp, maxDatagram)
	return limiter
}

func (l *Limiter) Update(bytesPerSecond int64, udp bool, maxDatagram int) error {
	if bytesPerSecond < 0 {
		return ErrInvalidLimit
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytesPerSecond == 0 {
		l.limiter = nil
		l.burst = 0
	} else {
		burst := tcpBurst(bytesPerSecond)
		if udp {
			burst = udpBurst(bytesPerSecond, maxDatagram)
		}
		l.limiter = rate.NewLimiter(rate.Limit(bytesPerSecond), burst)
		l.burst = burst
	}
	close(l.updated)
	l.updated = make(chan struct{})
	return nil
}

func (l *Limiter) WaitN(ctx context.Context, count int) (time.Duration, error) {
	if count <= 0 {
		return 0, nil
	}
	var waited time.Duration
	remaining := count
	for remaining > 0 {
		l.mu.Lock()
		current := l.limiter
		burst := l.burst
		updated := l.updated
		if current == nil {
			l.mu.Unlock()
			return waited, nil
		}
		chunk := min(remaining, burst)
		now := l.clock.Now()
		reservation := current.ReserveN(now, chunk)
		l.mu.Unlock()
		if !reservation.OK() {
			return waited, errors.New("rate limiter reservation failed")
		}
		delay := reservation.DelayFrom(now)
		if delay > 0 {
			timer := l.clock.NewTimer(delay)
			waitStarted := l.clock.Now()
			select {
			case <-ctx.Done():
				stopTimer(timer)
				reservation.CancelAt(l.clock.Now())
				waited += l.clock.Now().Sub(waitStarted)
				return waited, fmt.Errorf("wait for rate limit: %w", ctx.Err())
			case <-updated:
				stopTimer(timer)
				reservation.CancelAt(l.clock.Now())
				waited += l.clock.Now().Sub(waitStarted)
				continue
			case <-timer.Channel():
				waited += l.clock.Now().Sub(waitStarted)
			}
		}
		remaining -= chunk
	}
	return waited, nil
}

func (l *Limiter) AllowN(count int) bool {
	if count <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limiter == nil {
		return true
	}
	return l.limiter.AllowN(l.clock.Now(), count)
}

func WrapWriter(
	ctx context.Context,
	limiter *Limiter,
	onWait func(time.Duration),
) func(io.Writer) io.Writer {
	return func(writer io.Writer) io.Writer {
		return &limitedWriter{
			ctx: ctx, writer: writer, limiter: limiter, onWait: onWait,
		}
	}
}

type limitedWriter struct {
	ctx     context.Context
	writer  io.Writer
	limiter *Limiter
	onWait  func(time.Duration)
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	waited, err := w.limiter.WaitN(w.ctx, len(data))
	if waited > 0 && w.onWait != nil {
		w.onWait(waited)
	}
	if err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}

func tcpBurst(bytesPerSecond int64) int {
	return int(min(max(bytesPerSecond/10, 1), 64*1024))
}

func udpBurst(bytesPerSecond int64, maxDatagram int) int {
	if maxDatagram < 1 {
		maxDatagram = 1
	}
	return max(maxDatagram, int(min(bytesPerSecond, 256*1024)))
}

type timer interface {
	Channel() <-chan time.Time
	Stop() bool
}

type clock interface {
	Now() time.Time
	NewTimer(time.Duration) timer
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) NewTimer(delay time.Duration) timer {
	return realTimer{Timer: time.NewTimer(delay)}
}

type realTimer struct {
	*time.Timer
}

func (t realTimer) Channel() <-chan time.Time {
	return t.C
}

func stopTimer(timer timer) {
	if !timer.Stop() {
		select {
		case <-timer.Channel():
		default:
		}
	}
}
