package mailer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/portpilot/portpilot/internal/config"
	mail "github.com/wneessen/go-mail"
)

type SMTPSender struct {
	client *mail.Client
	from   string
}

func NewSMTPSender(value config.SMTPConfig) (*SMTPSender, error) {
	roots, err := systemRoots(value.TLSCAFile)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: value.Host,
		RootCAs:    roots,
	}
	options := []mail.Option{
		mail.WithPort(value.Port),
		mail.WithTimeout(10 * time.Second),
		mail.WithTLSConfig(tlsConfig),
		mail.WithSMTPAuth(mail.SMTPAuthPlain),
		mail.WithUsername(value.UserName),
		mail.WithPassword(value.Password),
	}
	if value.Security == "tls" {
		options = append(options, mail.WithSSL())
	} else {
		options = append(options, mail.WithTLSPolicy(mail.TLSMandatory))
	}
	client, err := mail.NewClient(value.Host, options...)
	if err != nil {
		return nil, fmt.Errorf("initialize SMTP client: %w", err)
	}
	return &SMTPSender{client: client, from: value.From}, nil
}

func (s *SMTPSender) Send(ctx context.Context, message Message) error {
	mailMessage := mail.NewMsg()
	if err := mailMessage.From(s.from); err != nil {
		return fmt.Errorf("set SMTP from address: %w", err)
	}
	if err := mailMessage.To(message.To); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	mailMessage.Subject(message.Subject)
	mailMessage.SetDate()
	mailMessage.SetMessageID()
	mailMessage.SetBodyString(mail.TypeTextPlain, message.TextBody)
	mailMessage.AddAlternativeString(mail.TypeTextHTML, message.HTMLBody)
	if err := s.client.DialAndSendWithContext(ctx, mailMessage); err != nil {
		return fmt.Errorf("send SMTP message: %w", err)
	}
	return nil
}

func systemRoots(caFile string) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if caFile == "" {
		return roots, nil
	}
	pemData, err := os.ReadFile(caFile) //nolint:gosec // Operator-configured trust bundle.
	if err != nil {
		return nil, fmt.Errorf("read SMTP TLS CA file: %w", err)
	}
	if ok := roots.AppendCertsFromPEM(pemData); !ok {
		return nil, errors.New("SMTP TLS CA file contains no certificates")
	}
	return roots, nil
}
