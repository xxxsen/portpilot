package activity

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

func TestProxySnapshotsAggregatesByClientAndProxy(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "activity.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := New(database, Config{
		SampleInterval: time.Hour, CleanupInterval: time.Hour,
		AuditRetention: 30 * 24 * time.Hour, NetworkRetention: 24 * time.Hour,
		SampleRetention: 24 * time.Hour, StatisticsRetention: 30 * 24 * time.Hour,
		FineRollupRetention: 24 * time.Hour, MaxActive: 1000,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)

	pending, err := service.Start(Start{Protocol: "tcp", ClientID: "a", ProxyID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Start(Start{Protocol: "tcp", ClientID: "a", ProxyID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	active.Connected()
	active.upBytesPerSecond.Store(100)
	active.downBytesPerSecond.Store(200)
	other, err := service.Start(Start{Protocol: "udp", ClientID: "b", ProxyID: "same"})
	if err != nil {
		t.Fatal(err)
	}
	other.Connected()
	other.upBytesPerSecond.Store(7)

	values := service.ProxySnapshots()
	first := values[ProxyRef{ClientID: "a", ProxyID: "same"}]
	if first.PendingConnections != 1 || first.ActiveConnections != 1 ||
		first.UpBytesPerSecond != 100 || first.DownBytesPerSecond != 200 {
		t.Fatalf("first snapshot=%#v", first)
	}
	second := values[ProxyRef{ClientID: "b", ProxyID: "same"}]
	if second.ActiveConnections != 1 || second.UpBytesPerSecond != 7 {
		t.Fatalf("second snapshot=%#v", second)
	}
	pending.Interrupt("test", "test")
	active.Interrupt("test", "test")
	other.Interrupt("test", "test")
}
