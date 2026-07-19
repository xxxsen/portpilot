package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTLSClientConfigRejectsInvalidCAAndSetsSNI(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := TLSClientConfig("gateway.example:443", path); err == nil {
		t.Fatal("invalid CA file was accepted")
	}
	if _, err := TLSClientConfig("gateway.example:443", filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing CA file silently fell back to system roots")
	}
	config, err := TLSClientConfig("gateway.example:443", "")
	if err != nil {
		t.Fatal(err)
	}
	if config.ServerName != "gateway.example" {
		t.Fatalf("unexpected TLS server name %q", config.ServerName)
	}
}

func TestDialUpgradeHonorsContextTimeout(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer func() { _ = conn.Close() }()
			<-t.Context().Done()
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = DialUpgrade(ctx, listener.Addr().String(), "/data", false, "", time.Second)
	if err == nil {
		t.Fatal("stalled upgrade ignored its context timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		var netError net.Error
		if !errors.As(err, &netError) || !netError.Timeout() {
			t.Fatalf("unexpected timeout error: %v", err)
		}
	}
}
