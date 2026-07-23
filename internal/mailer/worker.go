package mailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var ErrClosed = errors.New("mail worker closed")

type Task struct {
	Message   Message
	OnSuccess func()
	OnFailure func(error)
}

type WorkerStats struct {
	QueueDepth    int
	QueuedTotal   uint64
	SentTotal     uint64
	FailedTotal   uint64
	LastSuccessAt int64
	LastErrorAt   int64
}

type Worker struct {
	sender      Sender
	queue       chan Task
	closeCh     chan struct{}
	enqueueMu   sync.RWMutex
	closed      atomic.Bool
	closeOnce   sync.Once
	wg          sync.WaitGroup
	queued      atomic.Uint64
	sent        atomic.Uint64
	failed      atomic.Uint64
	lastSuccess atomic.Int64
	lastError   atomic.Int64
	retryDelay  time.Duration
	sendTimeout time.Duration
}

func NewWorker(sender Sender, capacity int) *Worker {
	return newWorker(sender, capacity, 2*time.Second, 10*time.Second)
}

func newWorker(sender Sender, capacity int, retryDelay, sendTimeout time.Duration) *Worker {
	worker := &Worker{
		sender: sender, queue: make(chan Task, capacity), closeCh: make(chan struct{}),
		retryDelay: retryDelay, sendTimeout: sendTimeout,
	}
	worker.wg.Add(1)
	go worker.run()
	return worker
}

func (w *Worker) Enqueue(task Task) bool {
	if w == nil {
		return false
	}
	w.enqueueMu.RLock()
	defer w.enqueueMu.RUnlock()
	if w.closed.Load() {
		return false
	}
	select {
	case w.queue <- task:
		w.queued.Add(1)
		return true
	default:
		return false
	}
}

func (w *Worker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.enqueueMu.Lock()
		w.closed.Store(true)
		close(w.closeCh)
		w.enqueueMu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close mail worker: %w", ctx.Err())
	}
}

func (w *Worker) Stats() WorkerStats {
	if w == nil {
		return WorkerStats{}
	}
	return WorkerStats{
		QueueDepth: len(w.queue), QueuedTotal: w.queued.Load(),
		SentTotal: w.sent.Load(), FailedTotal: w.failed.Load(),
		LastSuccessAt: w.lastSuccess.Load(), LastErrorAt: w.lastError.Load(),
	}
}

func (w *Worker) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.closeCh:
			w.failQueued(ErrClosed)
			return
		case task := <-w.queue:
			if w.closed.Load() {
				w.fail(task, ErrClosed)
				w.failQueued(ErrClosed)
				return
			}
			w.send(task)
		}
	}
}

func (w *Worker) send(task Task) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), w.sendTimeout)
		err = w.sender.Send(ctx, task.Message)
		cancel()
		if err == nil {
			w.sent.Add(1)
			w.lastSuccess.Store(time.Now().UTC().UnixMilli())
			if task.OnSuccess != nil {
				task.OnSuccess()
			}
			return
		}
		if attempt == 0 {
			select {
			case <-time.After(w.retryDelay):
			case <-w.closeCh:
				w.fail(task, ErrClosed)
				return
			}
		}
	}
	w.fail(task, err)
}

func (w *Worker) fail(task Task, err error) {
	w.failed.Add(1)
	w.lastError.Store(time.Now().UTC().UnixMilli())
	if task.OnFailure != nil {
		task.OnFailure(err)
	}
}

func (w *Worker) failQueued(err error) {
	for {
		select {
		case task := <-w.queue:
			w.fail(task, err)
		default:
			return
		}
	}
}
