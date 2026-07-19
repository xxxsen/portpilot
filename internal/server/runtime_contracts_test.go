package server

import (
	"encoding/json"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
)

func TestRuntimeMapAppliesDefaultTransportOnlyWhenOmitted(t *testing.T) {
	t.Parallel()
	omitted := runtimeContractProxy(t, "omitted", "")
	explicit := runtimeContractProxy(t, "explicit", "auto")
	file := config.ClientFile{
		Revision: 1,
		ClientConfig: []config.ClientConfig{{
			ClientID: "client", ClientToken: "01234567890123456789012345678901234567890123",
			Enabled: true, ProxyList: []config.ProxyEnvelope{omitted, explicit},
		}},
	}
	runtimes, err := runtimeMap(file, "direct")
	if err != nil {
		t.Fatal(err)
	}
	if runtimes["omitted"].Data.Transport != "direct" {
		t.Fatalf("omitted transport did not inherit server default: %#v", runtimes["omitted"].Data)
	}
	if runtimes["explicit"].Data.Transport != "auto" {
		t.Fatalf("explicit auto transport was overwritten: %#v", runtimes["explicit"].Data)
	}
}

func TestAuthOKCarriesAuthoritativeRelayTimeouts(t *testing.T) {
	t.Parallel()
	cfg := config.DefaultServerConfig()
	app := &App{Config: cfg}
	message := app.authOKMessage(&Session{ID: "session", Epoch: 3})
	payload, err := protocol.DecodePayload[protocol.AuthOK](message)
	if err != nil {
		t.Fatal(err)
	}
	if payload.DataPlane.TargetDial != cfg.Timeouts.TargetDial ||
		payload.DataPlane.TCPIdle != cfg.Timeouts.TCPIdle ||
		payload.DataPlane.TCPMaxLifetime != cfg.Timeouts.TCPMaxLifetime {
		t.Fatalf("relay timeouts were not propagated: %#v", payload.DataPlane)
	}
}

func runtimeContractProxy(t *testing.T, proxyID, transport string) config.ProxyEnvelope {
	t.Helper()
	data, err := json.Marshal(config.TunnelData{
		Network: "tcp", Transport: transport,
		Bind: "127.0.0.1:0", Connect: "127.0.0.1:18080",
	})
	if err != nil {
		t.Fatal(err)
	}
	return config.ProxyEnvelope{
		ProxyID: proxyID, Kind: "tunnel", Enabled: true, Data: data,
	}
}
