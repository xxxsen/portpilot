package relay

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestBidirectionalIdleTimeoutReleasesBothSides(t *testing.T) {
	t.Parallel()
	leftPeer, leftRelay := net.Pipe()
	rightRelay, rightPeer := net.Pipe()
	defer func() { _ = leftPeer.Close() }()
	defer func() { _ = rightPeer.Close() }()
	done := make(chan Result, 1)
	go func() {
		done <- Bidirectional(t.Context(), leftRelay, rightRelay, 30*time.Millisecond)
	}()
	select {
	case result := <-done:
		if result.Err == nil {
			t.Fatal("idle relay ended without a timeout error")
		}
		var netError net.Error
		if !errors.As(result.Err, &netError) || !netError.Timeout() {
			t.Fatalf("unexpected idle error: %v", result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle relay did not stop")
	}
}

func TestBidirectionalCancellationIsGraceful(t *testing.T) {
	t.Parallel()
	leftPeer, leftRelay := net.Pipe()
	rightRelay, rightPeer := net.Pipe()
	defer func() { _ = leftPeer.Close() }()
	defer func() { _ = rightPeer.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan Result, 1)
	go func() {
		done <- Bidirectional(ctx, leftRelay, rightRelay, 0)
	}()
	cancel()
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatalf("canceled relay was not graceful: %v", result.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled relay did not stop")
	}
}
