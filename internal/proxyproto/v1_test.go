package proxyproto

import (
	"net"
	"testing"
)

func TestV1(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		source      *net.TCPAddr
		destination *net.TCPAddr
		expected    string
	}{
		{
			name: "IPv4",
			source: &net.TCPAddr{
				IP: net.ParseIP("198.51.100.10"), Port: 54321,
			},
			destination: &net.TCPAddr{
				IP: net.ParseIP("203.0.113.20"), Port: 7000,
			},
			expected: "PROXY TCP4 198.51.100.10 203.0.113.20 54321 7000\r\n",
		},
		{
			name: "IPv6",
			source: &net.TCPAddr{
				IP: net.ParseIP("2001:db8::10"), Port: 1234,
			},
			destination: &net.TCPAddr{
				IP: net.ParseIP("2001:db8::20"), Port: 7000,
			},
			expected: "PROXY TCP6 2001:db8::10 2001:db8::20 1234 7000\r\n",
		},
		{
			name: "mapped IPv4",
			source: &net.TCPAddr{
				IP: net.ParseIP("::ffff:198.51.100.10"), Port: 1234,
			},
			destination: &net.TCPAddr{
				IP: net.ParseIP("203.0.113.20"), Port: 7000,
			},
			expected: "PROXY TCP4 198.51.100.10 203.0.113.20 1234 7000\r\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			header, err := V1(test.source, test.destination)
			if err != nil {
				t.Fatal(err)
			}
			if string(header) != test.expected {
				t.Fatalf("header = %q, want %q", header, test.expected)
			}
		})
	}
}

func TestV1RejectsFamilyMismatch(t *testing.T) {
	t.Parallel()
	_, err := V1(
		&net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1},
		&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 2},
	)
	if err == nil {
		t.Fatal("address family mismatch was accepted")
	}
}
