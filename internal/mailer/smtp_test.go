package mailer

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/config"
)

func TestSMTPSenderTLSAndSTARTTLS(t *testing.T) {
	t.Parallel()
	for _, security := range []string{"tls", "starttls"} {
		security := security
		t.Run(security, func(t *testing.T) {
			t.Parallel()
			server := startSMTPTestServer(t, security, true, true)
			sender, err := NewSMTPSender(server.config(security))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			err = sender.Send(ctx, Message{
				To: "user@example.com", Subject: "subject",
				TextBody: "plain marker", HTMLBody: "<p>html marker</p>",
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case data := <-server.messages:
				if !strings.Contains(data, "plain marker") || !strings.Contains(data, "html marker") ||
					!strings.Contains(data, "multipart/alternative") {
					t.Fatalf("message is not multipart text/html: %s", data)
				}
			case <-time.After(time.Second):
				t.Fatal("SMTP server did not receive message")
			}
		})
	}
}

func TestSMTPSenderRejectsMissingSTARTTLSAndBadAuthentication(t *testing.T) {
	t.Parallel()
	missingTLS := startSMTPTestServer(t, "starttls", false, true)
	sender, err := NewSMTPSender(missingTLS.config("starttls"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := sender.Send(ctx, Message{To: "user@example.com"}); err == nil {
		t.Fatal("STARTTLS downgrade was accepted")
	}

	badAuth := startSMTPTestServer(t, "tls", true, false)
	authSender, err := NewSMTPSender(badAuth.config("tls"))
	if err != nil {
		t.Fatal(err)
	}
	if err := authSender.Send(ctx, Message{To: "user@example.com"}); err == nil {
		t.Fatal("SMTP authentication failure was accepted")
	}
}

func TestSMTPSenderRejectsUntrustedCertificate(t *testing.T) {
	t.Parallel()
	server := startSMTPTestServer(t, "tls", true, true)
	value := server.config("tls")
	value.TLSCAFile = ""
	sender, err := NewSMTPSender(value)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := sender.Send(ctx, Message{To: "user@example.com"}); err == nil {
		t.Fatal("untrusted SMTP certificate was accepted")
	}
}

type smtpTestServer struct {
	host     string
	port     int
	caFile   string
	messages chan string
}

func (s smtpTestServer) config(security string) config.SMTPConfig {
	return config.SMTPConfig{
		Host: s.host, Port: s.port, Security: security,
		UserName: "portpilot", Password: "secret",
		From: "PortPilot <portpilot@example.com>", TLSCAFile: s.caFile,
	}
}

func startSMTPTestServer(t *testing.T, security string, advertiseTLS, acceptAuth bool) smtpTestServer {
	t.Helper()
	certificate, caFile := testCertificate(t)
	base, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := base
	if security == "tls" {
		listener = tls.NewListener(base, &tls.Config{
			Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
		})
	}
	result := smtpTestServer{
		host: "127.0.0.1", port: base.Addr().(*net.TCPAddr).Port,
		caFile: caFile, messages: make(chan string, 1),
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		handleSMTPTestConnection(connection, certificate, security, advertiseTLS, acceptAuth, result.messages)
	}()
	return result
}

func handleSMTPTestConnection(
	connection net.Conn,
	certificate tls.Certificate,
	security string,
	advertiseTLS bool,
	acceptAuth bool,
	messages chan<- string,
) {
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	reply := func(value string) bool {
		_, err := fmt.Fprintf(writer, "%s\r\n", value)
		return err == nil && writer.Flush() == nil
	}
	if !reply("220 localhost test") {
		return
	}
	upgraded := security == "tls"
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		command, argument, _ := strings.Cut(line, " ")
		switch strings.ToUpper(command) {
		case "EHLO", "HELO":
			switch {
			case security == "starttls" && !upgraded && advertiseTLS:
				_, _ = fmt.Fprint(writer, "250-localhost\r\n250 STARTTLS\r\n")
			case upgraded:
				_, _ = fmt.Fprint(writer, "250-localhost\r\n250 AUTH PLAIN\r\n")
			default:
				_, _ = fmt.Fprint(writer, "250 localhost\r\n")
			}
			if writer.Flush() != nil {
				return
			}
		case "STARTTLS":
			if !advertiseTLS || upgraded {
				_ = reply("454 TLS unavailable")
				continue
			}
			if !reply("220 Ready to start TLS") {
				return
			}
			tlsConnection := tls.Server(connection, &tls.Config{
				Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			handshakeErr := tlsConnection.HandshakeContext(ctx)
			cancel()
			if handshakeErr != nil {
				return
			}
			connection = tlsConnection
			reader = bufio.NewReader(connection)
			writer = bufio.NewWriter(connection)
			upgraded = true
		case "AUTH":
			fields := strings.Fields(argument)
			authOK := len(fields) == 2 && strings.EqualFold(fields[0], "PLAIN")
			if authOK {
				raw, decodeErr := base64.StdEncoding.DecodeString(fields[1])
				authOK = decodeErr == nil && string(raw) == "\x00portpilot\x00secret"
			}
			if !acceptAuth || !authOK {
				_ = reply("535 Authentication failed")
				continue
			}
			_ = reply("235 Authentication successful")
		case "MAIL", "RCPT":
			_ = reply("250 OK")
		case "DATA":
			if !reply("354 Continue") {
				return
			}
			var builder strings.Builder
			for {
				dataLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if dataLine == ".\r\n" || dataLine == ".\n" {
					break
				}
				builder.WriteString(dataLine)
			}
			messages <- builder.String()
			_ = reply("250 Queued")
		case "QUIT":
			_ = reply("221 Bye")
			return
		default:
			_ = reply("250 OK")
		}
	}
}

func testCertificate(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificate, caFile
}
