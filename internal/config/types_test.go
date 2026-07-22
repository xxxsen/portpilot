package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonlogger "github.com/xxxsen/common/logger"
)

const testToken = "01234567890123456789012345678901234567890123"

func TestClientFileRejectsDuplicateGlobalPort(t *testing.T) {
	t.Parallel()
	file := ClientFile{
		Revision: 1,
		ClientConfig: []ClientConfig{
			{
				ClientID: "a", ClientToken: testToken, Enabled: true,
				ProxyList: []ProxyEnvelope{testProxy("one", "tcp", "127.0.0.1:7000")},
			},
			{
				ClientID: "b", ClientToken: testToken, Enabled: true,
				ProxyList: []ProxyEnvelope{testProxy("two", "udp", "127.0.0.1:7000")},
			},
		},
	}
	if err := file.Validate("127.0.0.1:5100", "127.0.0.1:6100"); err == nil {
		t.Fatal("duplicate global port was accepted")
	}
}

func TestStrictConfigRejectsUnknownField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "client.json")
	data := `{"server":"127.0.0.1:6100","enable_tls":false,"path":"/portpilot","client_id":"a","client_token":"` + testToken + `","typo":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClient(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error not returned: %v", err)
	}
}

func TestRuntimeLogConfigDefaultsAndRelativePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	defaultPath := filepath.Join(dir, "client-default.json")
	defaultData := `{"server":"127.0.0.1:6100","client_id":"a","client_token":"` + testToken + `"}`
	if err := os.WriteFile(defaultPath, []byte(defaultData), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultConfig, err := LoadClient(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.LogConfig != DefaultLogConfig() {
		t.Fatalf("default log_config = %#v", defaultConfig.LogConfig)
	}

	filePath := filepath.Join(dir, "client-file.json")
	fileData := `{"server":"127.0.0.1:6100","client_id":"a","client_token":"` + testToken +
		`","log_config":{"file":"logs/client.log","level":"INFO","file_size":1048576,"file_count":3,"keep_days":7,"console":false}}`
	if err := os.WriteFile(filePath, []byte(fileData), 0o600); err != nil {
		t.Fatal(err)
	}
	fileConfig, err := LoadClient(filePath)
	if err != nil {
		t.Fatal(err)
	}
	expected := commonlogger.LogConfig{
		File: filepath.Join(dir, "logs", "client.log"), Level: "info",
		FileSize: 1048576, FileCount: 3, KeepDays: 7,
	}
	if fileConfig.LogConfig != expected {
		t.Fatalf("file log_config = %#v, want %#v", fileConfig.LogConfig, expected)
	}
}

func TestLogConfigValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value commonlogger.LogConfig
	}{
		{
			name:  "unsupported level",
			value: commonlogger.LogConfig{Level: "verbose", Console: true},
		},
		{
			name:  "no output",
			value: commonlogger.LogConfig{Level: "debug"},
		},
		{
			name:  "file without size",
			value: commonlogger.LogConfig{File: "portpilot.log", Level: "info"},
		},
		{
			name: "rotation without file",
			value: commonlogger.LogConfig{
				Level: "info", Console: true, FileSize: 1048576,
			},
		},
	} {
		if err := validateLogConfig(test.value); err == nil {
			t.Fatalf("%s was accepted", test.name)
		}
	}
}

func TestAnalyticsObservabilityRetentionValidation(t *testing.T) {
	t.Parallel()
	base := DefaultServerConfig().Observability
	if err := validateObservability(base); err != nil {
		t.Fatalf("default observability config is invalid: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ObservabilityConfig)
	}{
		{
			name: "statistics too short",
			mutate: func(value *ObservabilityConfig) {
				value.NetworkStatisticsRetention = (29 * 24 * time.Hour).String()
			},
		},
		{
			name: "fine rollup too long",
			mutate: func(value *ObservabilityConfig) {
				value.NetworkFineRollupRetention = (8 * 24 * time.Hour).String()
			},
		},
		{
			name: "insecure geoip source",
			mutate: func(value *ObservabilityConfig) {
				value.GeoIPCountrySource = "http://example.com/country.mmdb"
			},
		},
	} {
		value := base
		test.mutate(&value)
		if err := validateObservability(value); err == nil {
			t.Fatalf("%s was accepted", test.name)
		}
	}
}

func TestManagerRevisionRedactionAndAtomicUpdate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	initial := ClientFile{
		Revision: 4,
		ClientConfig: []ClientConfig{{
			ClientID: "a", ClientToken: testToken, Enabled: true,
			ProxyList: []ProxyEnvelope{testProxy("one", "tcp", "127.0.0.1:17000")},
		}},
	}
	writeTestJSON(t, path, initial)
	manager, err := NewManager(path, "127.0.0.1:15100", "127.0.0.1:16100")
	if err != nil {
		t.Fatal(err)
	}
	next := Redacted(manager.Snapshot())
	next.ClientConfig[0].ProxyList[0].Remark = "updated"
	committed := false
	updated, err := manager.Replace(4, next, func(ClientFile) (func(), func(), error) {
		return func() { committed = true }, func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !committed || updated.Revision != 5 || updated.ClientConfig[0].ClientToken != testToken {
		t.Fatalf("unexpected update: %#v committed=%v", updated, committed)
	}
	onDisk, err := LoadClientFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.ClientConfig[0].ClientToken != testToken || onDisk.ClientConfig[0].ProxyList[0].Remark != "updated" {
		t.Fatal("atomic update did not preserve token and change")
	}
	if _, err := manager.Replace(4, next, nil); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
}

func testProxy(id, network, bind string) ProxyEnvelope {
	transport := "auto"
	if network == "udp" {
		transport = "smux-datagram"
	}
	data, _ := json.Marshal(TunnelData{
		Network: network, Transport: transport, Bind: bind, Connect: "127.0.0.1:18080",
	})
	return ProxyEnvelope{ProxyID: id, Kind: "tunnel", Enabled: true, Data: data}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
