package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTargetPolicyAllowAndDeny(t *testing.T) {
	t.Parallel()
	policy := TargetPolicyConfig{
		AllowCIDRs: []string{"10.0.0.0/8"},
		DenyCIDRs:  []string{"10.20.0.0/16"},
		AllowPorts: []int{22, 443},
		DenyPorts:  []int{443},
	}
	for _, test := range []struct {
		target string
		ok     bool
	}{
		{target: "10.1.2.3:22", ok: true},
		{target: "10.20.1.2:22", ok: false},
		{target: "192.168.1.2:22", ok: false},
		{target: "10.1.2.3:443", ok: false},
		{target: "internal.example:22", ok: false},
	} {
		err := policy.Allows(test.target)
		if test.ok && err != nil {
			t.Fatalf("%s was unexpectedly rejected: %v", test.target, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("%s was unexpectedly accepted", test.target)
		}
	}
}

func TestValidateTargetsIdentifiesProxy(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(TunnelData{
		Network: "tcp", Bind: ":17000", Connect: "192.168.1.10:22",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := ClientFile{ClientConfig: []ClientConfig{{
		ClientID: "client-a",
		ProxyList: []ProxyEnvelope{{
			ProxyID: "proxy-a", Kind: "tunnel", Data: data,
		}},
	}}}
	err = ValidateTargets(file, TargetPolicyConfig{AllowCIDRs: []string{"10.0.0.0/8"}})
	if err == nil || !strings.Contains(err.Error(), "proxy-a") {
		t.Fatalf("expected a proxy-specific policy error, got %v", err)
	}
}
