package mailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedSender struct {
	mu      sync.Mutex
	results []error
	calls   int
	block   <-chan struct{}
}

func (s *scriptedSender) Send(ctx context.Context, _ Message) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return fmt.Errorf("wait for scripted sender: %w", ctx.Err())
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if call <= len(s.results) {
		return s.results[call-1]
	}
	return nil
}

func (s *scriptedSender) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestWorkerRetriesOnceThenSucceeds(t *testing.T) {
	t.Parallel()
	sender := &scriptedSender{results: []error{errors.New("temporary"), nil}}
	worker := newWorker(sender, 2, time.Millisecond, time.Second)
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	done := make(chan struct{})
	if !worker.Enqueue(Task{Message: Message{To: "user@example.com"}, OnSuccess: func() { close(done) }}) {
		t.Fatal("enqueue failed")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish retry")
	}
	stats := worker.Stats()
	if sender.Calls() != 2 || stats.QueuedTotal != 1 || stats.SentTotal != 1 || stats.FailedTotal != 0 {
		t.Fatalf("unexpected worker result: calls=%d stats=%#v", sender.Calls(), stats)
	}
}

func TestWorkerFailsAfterTwoAttempts(t *testing.T) {
	t.Parallel()
	sender := &scriptedSender{results: []error{errors.New("first"), errors.New("second")}}
	worker := newWorker(sender, 2, time.Millisecond, time.Second)
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	failed := make(chan error, 1)
	if !worker.Enqueue(Task{OnFailure: func(err error) { failed <- err }}) {
		t.Fatal("enqueue failed")
	}
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("failure callback received nil")
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not report failure")
	}
	stats := worker.Stats()
	if sender.Calls() != 2 || stats.FailedTotal != 1 || stats.LastErrorAt == 0 {
		t.Fatalf("unexpected worker failure result: calls=%d stats=%#v", sender.Calls(), stats)
	}
}

func TestWorkerQueueIsBoundedAndShutdownFailsUnsentTasks(t *testing.T) {
	t.Parallel()
	block := make(chan struct{})
	sender := &scriptedSender{block: block}
	worker := newWorker(sender, 1, time.Millisecond, 20*time.Millisecond)
	firstFailed := make(chan error, 1)
	secondFailed := make(chan error, 1)
	if !worker.Enqueue(Task{OnFailure: func(err error) { firstFailed <- err }}) {
		t.Fatal("first enqueue failed")
	}
	deadline := time.Now().Add(time.Second)
	for sender.Calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !worker.Enqueue(Task{OnFailure: func(err error) { secondFailed <- err }}) {
		t.Fatal("second enqueue should fit queue")
	}
	if worker.Enqueue(Task{}) {
		t.Fatal("queue accepted task over capacity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstFailed:
	case <-time.After(time.Second):
		t.Fatal("in-flight task was not failed on shutdown")
	}
	select {
	case err := <-secondFailed:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("queued shutdown error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued task was not failed on shutdown")
	}
}

func TestWorkerRejectsConcurrentEnqueueAfterClose(t *testing.T) {
	t.Parallel()
	sender := &scriptedSender{}
	worker := newWorker(sender, 64, time.Millisecond, time.Second)

	var accepted atomic.Int64
	var completed atomic.Int64
	start := make(chan struct{})
	var producers sync.WaitGroup
	for range 16 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			<-start
			for range 100 {
				task := Task{
					OnSuccess: func() { completed.Add(1) },
					OnFailure: func(error) { completed.Add(1) },
				}
				if worker.Enqueue(task) {
					accepted.Add(1)
				}
			}
		}()
	}
	close(start)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	producers.Wait()
	if worker.Enqueue(Task{}) {
		t.Fatal("worker accepted task after close")
	}
	if got, want := completed.Load(), accepted.Load(); got != want {
		t.Fatalf("completed callbacks = %d, accepted tasks = %d", got, want)
	}
}
