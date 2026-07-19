package transport

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialUpgradeValidatesTLSCertificate(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := AcceptUpgrade(w, r)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		data := make([]byte, 4)
		if _, err := io.ReadFull(conn, data); err == nil {
			_, _ = conn.Write(data)
		}
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	if _, err := DialUpgrade(context.Background(), parsed.Host, "/data", true, "", 2*time.Second); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}

	certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(caPath, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := DialUpgrade(context.Background(), parsed.Host, "/data", true, caPath, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4)
	if _, err := io.ReadFull(conn, data); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "ping" {
		t.Fatalf("unexpected echo %q", data)
	}
}
