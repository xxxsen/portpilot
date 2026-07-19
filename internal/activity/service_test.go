package activity

import (
	"errors"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

func TestLifecyclePersistsFinalCounters(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(database, Config{
		SampleInterval: 5 * time.Millisecond, CleanupInterval: time.Hour,
		AuditRetention: time.Hour, NetworkRetention: time.Hour,
		SampleRetention: time.Hour, StatisticsRetention: time.Hour,
		FineRollupRetention: time.Hour, MaxActive: 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := service.Start(Start{
		Protocol: "tcp", ClientID: "client-a", ClientRemark: "Client A",
		ProxyID: "ssh", ProxyRemark: "SSH", ExternalAddr: "192.0.2.1:5000",
		ServerBind: ":7000", TargetAddr: "10.0.0.2:22", Transport: "smux",
		SessionEpoch: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	handle.Connecting()
	handle.Connected()
	handle.AddUp(100, 0)
	handle.AddDown(200, 0)
	handle.Close("closed", "relay completed")
	handle.Fail("ignored", "idempotent finalize")
	service.Close()
	defer func() { _ = database.Close() }()

	detail, err := database.NetworkConnectionDetail(t.Context(), handle.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "closed" || detail.UpBytes != 100 || detail.DownBytes != 200 {
		t.Fatalf("unexpected persisted lifecycle: %#v", detail.NetworkConnection)
	}
	if len(detail.Samples) == 0 {
		t.Fatal("final traffic sample was not persisted")
	}
	if total, _ := service.Counts(); total != 0 {
		t.Fatalf("finished activity remains active: %d", total)
	}
}

func TestActiveLimitRejectsWithoutLeakingRegistry(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(database, Config{
		SampleInterval: time.Hour, CleanupInterval: time.Hour,
		AuditRetention: time.Hour, NetworkRetention: time.Hour,
		SampleRetention: time.Hour, StatisticsRetention: time.Hour,
		FineRollupRetention: time.Hour, MaxActive: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Start(Start{Protocol: "udp", ClientID: "client", ProxyID: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(Start{Protocol: "udp", ClientID: "client", ProxyID: "proxy"}); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("second activity error = %v, want ErrLimitReached", err)
	}
	first.Close("done", "done")
	service.Close()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotReportsLatestTransferRates(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(database, Config{
		SampleInterval: time.Hour, CleanupInterval: time.Hour,
		AuditRetention: time.Hour, NetworkRetention: time.Hour,
		SampleRetention: time.Hour, StatisticsRetention: time.Hour,
		FineRollupRetention: time.Hour, MaxActive: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := service.Start(Start{
		Protocol: "tcp", ClientID: "client", ProxyID: "proxy",
	})
	if err != nil {
		t.Fatal(err)
	}
	handle.AddUp(1500, 0)
	handle.AddDown(500, 0)
	handle.rateSampledAt.Store(time.Now().UTC().Add(-time.Second).UnixMilli())
	service.sampleRate(handle)

	snapshot := handle.Snapshot()
	if snapshot.UpBytesPerSecond <= 0 || snapshot.DownBytesPerSecond <= 0 {
		t.Fatalf("unexpected transfer rates: %#v", snapshot)
	}
	if snapshot.RateSampledAt <= 0 || snapshot.RateWindowMillis < 1000 {
		t.Fatalf("unexpected rate window: %#v", snapshot)
	}

	handle.Close("done", "done")
	service.Close()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillAggregatesInterruptedConnection(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	if err := database.CreateNetworkConnection(t.Context(), store.NetworkConnection{
		ActivityID: "00000000000000000000000000000101",
		Protocol:   "tcp", State: "active", ClientID: "client", ProxyID: "proxy",
		ExternalAddr: "10.0.0.5:5000",
		AcceptedAt:   now, LastActivityAt: now + 1000, UpBytes: 50, DownBytes: 75,
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := database.InterruptOpenConnections(t.Context(), now+2000); err != nil || count != 1 {
		t.Fatalf("interrupt count=%d error=%v", count, err)
	}
	service, err := New(database, Config{
		SampleInterval: time.Hour, CleanupInterval: time.Hour,
		AuditRetention: time.Hour, NetworkRetention: time.Hour,
		SampleRetention: time.Hour, StatisticsRetention: 90 * 24 * time.Hour,
		FineRollupRetention: 48 * time.Hour, MaxActive: 10,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		_ = database.Close()
	})
	deadline := time.Now().Add(time.Second)
	for {
		meta, metaErr := service.AnalyticsMeta(t.Context())
		if metaErr == nil && meta.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backfill did not become ready: meta=%#v error=%v", meta, metaErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	overview, err := database.NetworkAnalyticsOverview(t.Context(), store.AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: store.AnalyticsHourlyResolutionSeconds, OutputResolution: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Connections != 1 || overview.UpBytes != 50 || overview.DownBytes != 75 {
		t.Fatalf("interrupted connection was not backfilled: %#v", overview)
	}
}

func TestBytesPerSecond(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		bytes  int64
		window int64
		want   int64
	}{
		{name: "one second", bytes: 4096, window: 1000, want: 4096},
		{name: "half second", bytes: 1024, window: 500, want: 2048},
		{name: "fraction", bytes: 1, window: 2000, want: 0},
		{name: "empty window", bytes: 1024, window: 0, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := bytesPerSecond(test.bytes, test.window); got != test.want {
				t.Fatalf("bytesPerSecond(%d, %d) = %d, want %d", test.bytes, test.window, got, test.want)
			}
		})
	}
}
