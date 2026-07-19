package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) CloseWrite() error {
	if writer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return writer.CloseWrite()
	}
	return c.Close()
}

func (c *bufferedConn) Read(data []byte) (int, error) {
	return c.reader.Read(data)
}

func AcceptUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "portpilot") {
		return nil, fmt.Errorf("missing portpilot upgrade headers")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("HTTP server does not support hijacking")
	}
	conn, buffer, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack upgrade connection: %w", err)
	}
	if _, err := buffer.WriteString(
		"HTTP/1.1 101 Switching Protocols\r\n" +
			"Connection: Upgrade\r\n" +
			"Upgrade: portpilot\r\n\r\n",
	); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write upgrade response: %w", err)
	}
	if err := buffer.Flush(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("flush upgrade response: %w", err)
	}
	return &bufferedConn{Conn: conn, reader: buffer.Reader}, nil
}

func dialTLS(ctx context.Context, raw net.Conn, server, caFile string) (net.Conn, error) {
	config, err := TLSClientConfig(server, caFile)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	conn := tls.Client(raw, config)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("perform TLS handshake: %w", err)
	}
	return conn, nil
}

func TLSClientConfig(server, caFile string) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return nil, fmt.Errorf("parse server address: %w", err)
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if caFile == "" {
		return config, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // The CA path is explicit trusted client configuration.
	if err != nil {
		return nil, fmt.Errorf("read TLS CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("TLS CA file contains no certificates")
	}
	config.RootCAs = pool
	return config, nil
}

func DialUpgrade(ctx context.Context, server, path string, enableTLS bool, caFile string, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, fmt.Errorf("dial portpilot server: %w", err)
	}
	conn := raw
	if enableTLS {
		conn, err = dialTLS(ctx, raw, server, caFile)
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	requestURL := &url.URL{Scheme: "http", Host: server, Path: path}
	request := &http.Request{
		Method: http.MethodGet, URL: requestURL, Host: server,
		Header: make(http.Header),
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "portpilot")
	request.Header.Set("User-Agent", "portpilot-client/1")
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		_ = conn.Close()
		return nil, fmt.Errorf("upgrade rejected: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, reader: reader}, nil
}
