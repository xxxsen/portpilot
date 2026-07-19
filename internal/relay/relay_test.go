package relay

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestBidirectionalPreservesHalfClose(t *testing.T) {
	t.Parallel()
	leftClient, leftRelay := tcpPair(t)
	rightRelay, rightClient := tcpPair(t)
	defer func() { _ = leftClient.Close() }()
	defer func() { _ = rightClient.Close() }()

	done := make(chan Result, 1)
	go func() {
		done <- Bidirectional(context.Background(), leftRelay, rightRelay, 2*time.Second)
	}()
	if _, err := leftClient.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	if err := leftClient.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	request, err := io.ReadAll(rightClient)
	if err != nil {
		t.Fatal(err)
	}
	if string(request) != "request" {
		t.Fatalf("unexpected request %q", request)
	}
	if _, err := rightClient.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	if err := rightClient.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(leftClient)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "response" {
		t.Fatalf("unexpected response %q", response)
	}
	select {
	case result := <-done:
		if result.Err != nil || result.UpBytes != 7 || result.DownBytes != 8 {
			t.Fatalf("unexpected relay result: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not complete after both half-closes")
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan *net.TCPConn, 1)
	go func() {
		conn, acceptErr := listener.AcceptTCP()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := <-accepted
	_ = listener.Close()
	return client, server
}
