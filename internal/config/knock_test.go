package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func validKnockConfig() KnockConfig {
	return KnockConfig{
		Enabled: true, PublicBaseURL: "https://portpilot.example.com",
		MagicLinkTTL: "10m", GrantTTL: "10m", ResendInterval: "1m",
		AllowedEmails: []string{"user@example.com"},
		SMTP: SMTPConfig{
			Host: "smtp.example.com", Port: 465, Security: "tls",
			UserName: "portpilot", Password: "secret",
			From: "PortPilot <portpilot@example.com>",
		},
	}
}

func TestKnockConfigurationValidation(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*KnockConfig){
		"http URL":        func(value *KnockConfig) { value.PublicBaseURL = "http://example.com" },
		"URL query":       func(value *KnockConfig) { value.PublicBaseURL += "?token=x" },
		"URL fragment":    func(value *KnockConfig) { value.PublicBaseURL += "#x" },
		"URL user info":   func(value *KnockConfig) { value.PublicBaseURL = "https://user@example.com" },
		"short token TTL": func(value *KnockConfig) { value.MagicLinkTTL = "1m59s" },
		"long grant TTL":  func(value *KnockConfig) { value.GrantTTL = "24h1s" },
		"short resend":    func(value *KnockConfig) { value.ResendInterval = "29s" },
		"empty allowlist": func(value *KnockConfig) { value.AllowedEmails = nil },
		"duplicate email": func(value *KnockConfig) {
			value.AllowedEmails = []string{"user@example.com", "user@example.com"}
		},
		"display email": func(value *KnockConfig) {
			value.AllowedEmails = []string{"User <user@example.com>"}
		},
		"SMTP scheme":   func(value *KnockConfig) { value.SMTP.Host = "smtp://example.com" },
		"SMTP port":     func(value *KnockConfig) { value.SMTP.Port = 0 },
		"SMTP security": func(value *KnockConfig) { value.SMTP.Security = "none" },
		"SMTP password": func(value *KnockConfig) { value.SMTP.Password = "" },
		"SMTP from":     func(value *KnockConfig) { value.SMTP.From = "invalid" },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			value := validKnockConfig()
			mutate(&value)
			if err := validateKnock(value); err == nil {
				t.Fatal("invalid knock configuration was accepted")
			}
		})
	}
	if err := validateKnock(validKnockConfig()); err != nil {
		t.Fatalf("valid TLS configuration rejected: %v", err)
	}
	startTLS := validKnockConfig()
	startTLS.SMTP.Security = "starttls"
	startTLS.SMTP.Port = 587
	if err := validateKnock(startTLS); err != nil {
		t.Fatalf("valid STARTTLS configuration rejected: %v", err)
	}
}

func TestKnockDisabledIsBackwardCompatible(t *testing.T) {
	t.Parallel()
	cfg := DefaultServerConfig()
	if cfg.Knock.Enabled {
		t.Fatal("knock must default to disabled")
	}
	if err := validateKnock(KnockConfig{}); err != nil {
		t.Fatalf("empty disabled configuration rejected: %v", err)
	}
	if cfg.Limits.MaxKnockRequestsPerHour != 10 ||
		cfg.Limits.MaxKnockPendingTokens != 1024 ||
		cfg.Limits.MaxKnockGrants != 4096 ||
		cfg.Limits.MaxKnockMailQueue != 128 {
		t.Fatalf("unexpected knock defaults: %#v", cfg.Limits)
	}
}

func TestKnockProxyCombination(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(TunnelData{
		Network: "tcp", Bind: ":7000", Connect: "127.0.0.1:22",
		AllowKnockOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := ClientFile{ClientConfig: []ClientConfig{{
		ClientID: "client", ProxyList: []ProxyEnvelope{{
			ProxyID: "proxy", Kind: "tunnel", Data: data,
		}},
	}}}
	if err := ValidateKnockProxyCombination(false, file); err == nil ||
		!strings.Contains(err.Error(), "requires knock_config.enabled=true") {
		t.Fatalf("disabled combination error = %v", err)
	}
	if err := ValidateKnockProxyCombination(true, file); err != nil {
		t.Fatalf("enabled combination rejected: %v", err)
	}
}
