package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccessPolicyValidation(t *testing.T) {
	t.Parallel()
	valid := AccessPolicyConfig{
		AllowCIDRs: []string{"192.0.2.0/24", "2001:db8::/32"},
		DenyCIDRs:  []string{"192.0.2.128/25"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	tests := map[string]AccessPolicyConfig{
		"host bits": {AllowCIDRs: []string{"192.0.2.1/24"}},
		"bare ip":   {AllowCIDRs: []string{"192.0.2.1"}},
		"duplicate": {DenyCIDRs: []string{"192.0.2.0/24", "192.0.2.0/24"}},
		"private only with CIDR": {
			PrivateOnly: true,
			AllowCIDRs:  []string{"192.0.2.0/24"},
		},
		"conflict": {
			AllowCIDRs: []string{"192.0.2.0/24"},
			DenyCIDRs:  []string{"192.0.2.0/24"},
		},
	}
	for name, policy := range tests {
		if err := policy.Validate(); err == nil {
			t.Errorf("%s policy was accepted", name)
		}
	}
}

func TestAccessPolicyPrivateOnlyPersistsInJSON(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(AccessPolicyConfig{PrivateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"private_only":true`) {
		t.Fatalf("private-only marker missing from JSON: %s", raw)
	}
	var decoded AccessPolicyConfig
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.PrivateOnly {
		t.Fatal("private-only marker was not restored")
	}
}

func TestRateLimitValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []int64{0, 1024, 1 << 20, 1 << 40} {
		if err := (RateLimitConfig{
			UpBytesPerSecond: value, DownBytesPerSecond: value,
		}).Validate(); err != nil {
			t.Errorf("rate %d rejected: %v", value, err)
		}
	}
	for _, value := range []int64{-1, 1, 1023, (1 << 40) + 1} {
		if err := (RateLimitConfig{UpBytesPerSecond: value}).Validate(); err == nil {
			t.Errorf("invalid rate %d accepted", value)
		}
	}
}

func TestProxyProtocolOnlyAllowsTCP(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		network string
		enabled bool
		wantErr bool
	}{
		{name: "missing defaults off", network: "tcp"},
		{name: "tcp enabled", network: "tcp", enabled: true},
		{name: "udp disabled", network: "udp"},
		{name: "udp enabled", network: "udp", enabled: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := "auto"
			if test.network == "udp" {
				transport = "smux-datagram"
			}
			raw, err := json.Marshal(TunnelData{
				Network: test.network, Transport: transport,
				Bind: ":7000", Connect: "127.0.0.1:80",
				EnableProxyProtocol: test.enabled,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = (ProxyEnvelope{
				ProxyID: "proxy", Kind: "tunnel", Data: raw,
			}).Tunnel()
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestRealIPHeaderValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "X-Real-IP", "CF-Connecting-IP"} {
		if err := validateRealIPHeader(value); err != nil {
			t.Errorf("valid header %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{" X-Real-IP", "X Real IP", "X:IP", "X,IP", strings.Repeat("a", 129)} {
		if err := validateRealIPHeader(value); err == nil {
			t.Errorf("invalid header %q accepted", value)
		}
	}
}
