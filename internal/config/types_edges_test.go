package config

import (
	"encoding/json"
	"strings"
	"testing"
)

const testAdminPasswordHash = "$2y$10$/io15KIC2pA.bXQi6J5G0OiLm0T.R.raVU/tokVwDGJolAgSBN2g2" //nolint:gosec // Public bcrypt test fixture.

func TestProxyTunnelRejectsInvalidNetworkTransportAndAddress(t *testing.T) {
	t.Parallel()
	tests := map[string]TunnelData{
		"network":  {Network: "icmp", Transport: "auto", Bind: ":7000", Connect: "127.0.0.1:80"},
		"tcp mode": {Network: "tcp", Transport: "smux-datagram", Bind: ":7000", Connect: "127.0.0.1:80"},
		"udp mode": {Network: "udp", Transport: "direct", Bind: ":7000", Connect: "127.0.0.1:80"},
		"bind":     {Network: "tcp", Transport: "auto", Bind: "7000", Connect: "127.0.0.1:80"},
		"connect":  {Network: "tcp", Transport: "auto", Bind: ":7000", Connect: "missing-port"},
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			_, err = (ProxyEnvelope{ProxyID: name, Kind: "tunnel", Data: raw}).Tunnel()
			if err == nil {
				t.Fatal("invalid tunnel was accepted")
			}
		})
	}
}

func TestClientFileRejectsServicePortAndDuplicateIdentifiers(t *testing.T) {
	t.Parallel()
	base := ClientFile{
		Revision: 1,
		ClientConfig: []ClientConfig{{
			ClientID: "client", ClientToken: testToken, Enabled: true,
			ProxyList: []ProxyEnvelope{testProxy("proxy", "tcp", "127.0.0.1:5100")},
		}},
	}
	if err := base.Validate("127.0.0.1:5100", "127.0.0.1:6100"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("service port conflict was not rejected: %v", err)
	}

	duplicateClient := base
	duplicateClient.ClientConfig = append(duplicateClient.ClientConfig, ClientConfig{
		ClientID: "client", ClientToken: testToken, Enabled: true,
	})
	if err := duplicateClient.Validate("", ""); err == nil {
		t.Fatal("duplicate client ID was accepted")
	}
}

func TestServerConfigRejectsInvalidDataPlaneLimits(t *testing.T) {
	t.Parallel()
	config := DefaultServerConfig()
	config.ClientConfigFile = "clients.json"
	config.Users = []User{{UserName: "admin", PasswordHash: testAdminPasswordHash}}
	config.DataPlane.SMux.ScaleUpStreams = config.DataPlane.SMux.MaxStreamsPerSession + 1
	if err := config.Validate(); err == nil {
		t.Fatal("invalid smux scale threshold was accepted")
	}
	config = DefaultServerConfig()
	config.ClientConfigFile = "clients.json"
	config.Users = []User{{UserName: "admin", PasswordHash: testAdminPasswordHash}}
	config.DataPlane.UDP.MaxDatagramSize = 65508
	if err := config.Validate(); err == nil {
		t.Fatal("oversized UDP datagram configuration was accepted")
	}
}
