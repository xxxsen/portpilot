package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type capturedMessage struct {
	ID         int64  `json:"id"`
	ReceivedAt int64  `json:"received_at"`
	Recipient  string `json:"recipient"`
	Data       string `json:"data"`
}

type mailbox struct {
	mu       sync.RWMutex
	nextID   int64
	messages []capturedMessage
}

func main() {
	var smtpAddress string
	var httpAddress string
	var certificateDir string
	var username string
	var password string
	flag.StringVar(&smtpAddress, "smtp-addr", "127.0.0.1:1465", "STARTTLS SMTP listen address")
	flag.StringVar(&httpAddress, "http-addr", "127.0.0.1:18083", "capture API listen address")
	flag.StringVar(&certificateDir, "cert-dir", ".dev-data/smtp", "generated certificate directory")
	flag.StringVar(&username, "username", "portpilot", "SMTP username")
	flag.StringVar(&password, "password", "portpilot-dev-password", "SMTP password")
	flag.Parse()

	certificate, err := generateCertificates(certificateDir)
	if err != nil {
		log.Fatal(err)
	}
	box := &mailbox{}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	smtpListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", smtpAddress)
	if err != nil {
		log.Printf("listen for SMTP: %v", err)
		return
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}
	httpServer := &http.Server{
		Addr: httpAddress, Handler: captureMux(box),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go serveSMTP(ctx, smtpListener, tlsConfig, box, username, password)
	go func() {
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("capture API: %v", serveErr)
			stop()
		}
	}()
	log.Printf("SMTP fixture listening on %s; capture API on %s", smtpAddress, httpAddress)
	<-ctx.Done()
	_ = smtpListener.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func captureMux(box *mailbox) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		box.mu.RLock()
		items := append([]capturedMessage(nil), box.messages...)
		box.mu.RUnlock()
		to := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("to")))
		if to != "" {
			filtered := items[:0]
			for _, item := range items {
				if strings.EqualFold(item.Recipient, to) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	})
	mux.HandleFunc("DELETE /messages", func(w http.ResponseWriter, _ *http.Request) {
		box.mu.Lock()
		box.messages = nil
		box.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func serveSMTP(
	ctx context.Context,
	listener net.Listener,
	tlsConfig *tls.Config,
	box *mailbox,
	username string,
	password string,
) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("SMTP accept: %v", err)
			}
			return
		}
		go handleSMTP(ctx, connection, tlsConfig, box, username, password)
	}
}

func handleSMTP(
	ctx context.Context,
	connection net.Conn,
	tlsConfig *tls.Config,
	box *mailbox,
	username string,
	password string,
) {
	session := &smtpSession{
		connection: connection,
		tlsConfig:  tlsConfig,
		box:        box,
		username:   username,
		password:   password,
	}
	defer func() { _ = session.connection.Close() }()
	_ = session.connection.SetDeadline(time.Now().Add(30 * time.Second))
	session.resetIO()
	if !session.reply("220 localhost PortPilot SMTP fixture") {
		return
	}
	session.run(ctx)
}

type smtpSession struct {
	connection    net.Conn
	tlsConfig     *tls.Config
	box           *mailbox
	username      string
	password      string
	reader        *bufio.Reader
	writer        *bufio.Writer
	authenticated bool
	tlsActive     bool
	recipient     string
}

func (s *smtpSession) resetIO() {
	s.reader = bufio.NewReader(s.connection)
	s.writer = bufio.NewWriter(s.connection)
}

func (s *smtpSession) reply(line string) bool {
	if _, err := fmt.Fprintf(s.writer, "%s\r\n", line); err != nil {
		return false
	}
	return s.writer.Flush() == nil
}

func (s *smtpSession) run(ctx context.Context) {
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		command, argument, _ := strings.Cut(line, " ")
		if !s.handleCommand(ctx, strings.ToUpper(command), argument) {
			return
		}
	}
}

func (s *smtpSession) handleCommand(ctx context.Context, command, argument string) bool {
	switch command {
	case "EHLO", "HELO":
		return s.writeCapabilities()
	case "STARTTLS":
		return s.startTLS(ctx)
	case "AUTH":
		return s.authenticate(argument)
	case "MAIL":
		return s.beginMail()
	case "RCPT":
		s.recipient = extractAddress(argument)
		return s.reply("250 2.1.5 OK")
	case "DATA":
		return s.receiveData()
	case "RSET":
		s.recipient = ""
		return s.reply("250 2.0.0 OK")
	case "NOOP":
		return s.reply("250 2.0.0 OK")
	case "QUIT":
		_ = s.reply("221 2.0.0 Bye")
		return false
	default:
		return s.reply("502 5.5.2 Command not implemented")
	}
}

func (s *smtpSession) writeCapabilities() bool {
	capabilities := "250-localhost\r\n250-STARTTLS\r\n250 SIZE 1048576\r\n"
	if s.tlsActive {
		capabilities = "250-localhost\r\n250-AUTH PLAIN\r\n250 SIZE 1048576\r\n"
	}
	if _, err := fmt.Fprint(s.writer, capabilities); err != nil {
		return false
	}
	return s.writer.Flush() == nil
}

func (s *smtpSession) startTLS(parent context.Context) bool {
	if s.tlsActive {
		return s.reply("503 5.5.1 TLS already active")
	}
	if !s.reply("220 2.0.0 Ready to start TLS") {
		return false
	}
	secured := tls.Server(s.connection, s.tlsConfig)
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := secured.HandshakeContext(ctx); err != nil {
		return false
	}
	s.connection = secured
	s.resetIO()
	s.authenticated = false
	s.tlsActive = true
	return true
}

func (s *smtpSession) authenticate(argument string) bool {
	if !s.tlsActive {
		return s.reply("538 5.7.11 Encryption required")
	}
	fields := strings.Fields(argument)
	if len(fields) != 2 || strings.ToUpper(fields[0]) != "PLAIN" ||
		!validPlainAuth(fields[1], s.username, s.password) {
		return s.reply("535 5.7.8 Authentication credentials invalid")
	}
	s.authenticated = true
	return s.reply("235 2.7.0 Authentication successful")
}

func (s *smtpSession) beginMail() bool {
	if !s.authenticated {
		return s.reply("530 5.7.0 Authentication required")
	}
	return s.reply("250 2.1.0 OK")
}

func (s *smtpSession) receiveData() bool {
	if s.recipient == "" {
		return s.reply("503 5.5.1 Bad sequence")
	}
	if !s.reply("354 End data with <CR><LF>.<CR><LF>") {
		return false
	}
	data, err := readData(s.reader)
	if err != nil {
		return false
	}
	s.box.mu.Lock()
	s.box.nextID++
	s.box.messages = append(s.box.messages, capturedMessage{
		ID: s.box.nextID, ReceivedAt: time.Now().UTC().UnixMilli(),
		Recipient: s.recipient, Data: data,
	})
	s.box.mu.Unlock()
	s.recipient = ""
	return s.reply("250 2.0.0 Queued")
}

func validPlainAuth(encoded, username, password string) bool {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	parts := strings.Split(string(raw), "\x00")
	return len(parts) == 3 && parts[1] == username && parts[2] == password
}

func extractAddress(value string) string {
	start := strings.IndexByte(value, '<')
	end := strings.IndexByte(value, '>')
	if start >= 0 && end > start {
		return strings.ToLower(strings.TrimSpace(value[start+1 : end]))
	}
	_, address, _ := strings.Cut(value, ":")
	return strings.ToLower(strings.TrimSpace(address))
}

func readData(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" || line == ".\n" {
			return builder.String(), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		builder.WriteString(line)
	}
}

func generateCertificates(directory string) (tls.Certificate, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer func() { _ = root.Close() }()
	now := time.Now().UTC()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "PortPilot Dev SMTP CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPath := filepath.Join(directory, "server.pem")
	keyPath := filepath.Join(directory, "server-key.pem")
	if err := writePEM(root, "ca.pem", "CERTIFICATE", caDER, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(root, "server.pem", "CERTIFICATE", serverDER, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(root, "server-key.pem", "PRIVATE KEY", keyDER, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func writePEM(root *os.Root, path, blockType string, data []byte, mode os.FileMode) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return pem.Encode(file, &pem.Block{Type: blockType, Bytes: data})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
