package accesspolicy

import (
	"net"
	"net/netip"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
)

func TestPolicyDenyPrecedesAllow(t *testing.T) {
	t.Parallel()
	policy := Compile(config.AccessPolicyConfig{
		AllowCIDRs: []string{"192.0.2.0/24"},
		DenyCIDRs:  []string{"192.0.2.128/25"},
	})
	for _, test := range []struct {
		address string
		allowed bool
		reason  string
	}{
		{address: "192.0.2.10", allowed: true},
		{address: "192.0.2.200", reason: "deny"},
		{address: "198.51.100.1", reason: "allow_miss"},
	} {
		allowed, reason := policy.Decide(netip.MustParseAddr(test.address))
		if allowed != test.allowed || reason != test.reason {
			t.Errorf("%s = %v/%q", test.address, allowed, reason)
		}
	}
}

func TestNormalizeAddrUnmapsIPv4(t *testing.T) {
	t.Parallel()
	address, err := NormalizeAddr(&net.TCPAddr{
		IP: net.ParseIP("::ffff:192.0.2.10"), Port: 7000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if address.String() != "192.0.2.10" {
		t.Fatalf("normalized address = %s", address)
	}
}
