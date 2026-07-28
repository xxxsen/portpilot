package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNetworkAnalyticsFinalizationIsHistoricalExactAndIdempotent(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	closedAt := now + (20 * time.Minute).Milliseconds()
	connection := NetworkConnection{
		ActivityID: "00000000000000000000000000000001",
		Protocol:   "tcp", State: "accepted",
		ClientID: "client-a", ClientRemark: "Client A",
		ProxyID: "ssh", ProxyRemark: "SSH",
		ExternalAddr: "81.2.69.142:50000", ExternalIP: "81.2.69.142",
		ServerBind: ":7000", TargetAddr: "10.0.0.2:22", Transport: "smux",
		AcceptedAt:     now + time.Minute.Milliseconds(),
		LastActivityAt: now + (19 * time.Minute).Milliseconds(),
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	activeOverview, err := database.NetworkAnalyticsOverview(t.Context(), AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsFineResolutionSeconds, OutputResolution: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if activeOverview.Connections != 0 || activeOverview.UpBytes != 0 {
		t.Fatalf("active connection entered history analytics: %#v", activeOverview)
	}
	if err := database.UpsertNetworkSample(t.Context(), NetworkSample{
		ActivityID:   connection.ActivityID,
		BucketAt:     now + (5 * time.Minute).Milliseconds(),
		UpBytesDelta: 70, DownBytesDelta: 50,
	}); err != nil {
		t.Fatal(err)
	}
	connection.State = "closed"
	connection.ClosedAt = &closedAt
	connection.UpBytes = 100
	connection.DownBytes = 60
	result, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "country", CountryCode: "GB", DatabaseFingerprint: "fixture"},
		closedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Aggregated || result.SampleOverflow {
		t.Fatalf("unexpected finalize result: %#v", result)
	}
	again, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "country", CountryCode: "GB", DatabaseFingerprint: "fixture"},
		closedAt+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if again.Aggregated {
		t.Fatal("connection was aggregated twice")
	}

	overview, err := database.NetworkAnalyticsOverview(t.Context(), AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsFineResolutionSeconds, OutputResolution: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Connections != 1 || overview.UpBytes != 100 || overview.DownBytes != 60 ||
		overview.EstimatedBytes != 40 || overview.UniqueIPs != 1 || overview.Countries != 1 {
		t.Fatalf("unexpected historical overview: %#v", overview)
	}
	if len(overview.CountryDistribution) != 1 ||
		overview.CountryDistribution[0].CountryCode != "GB" {
		t.Fatalf("unexpected country distribution: %#v", overview.CountryDistribution)
	}
	fineOverview, err := database.NetworkAnalyticsOverview(t.Context(), AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution:   AnalyticsFineResolutionSeconds,
		OutputResolution: AnalyticsFineResolutionSeconds,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fineOverview.Series) != 12 ||
		fineOverview.Series[0].Connections != 1 ||
		fineOverview.Series[1].UpBytes != 70 ||
		fineOverview.Series[1].DownBytes != 50 ||
		fineOverview.Series[4].EstimatedBytes != 40 {
		t.Fatalf("connection and traffic were assigned to the wrong buckets: %#v", fineOverview.Series)
	}
	for index, point := range fineOverview.Series[5:] {
		if point.Connections != 0 || point.Failures != 0 ||
			point.UpBytes != 0 || point.DownBytes != 0 || point.EstimatedBytes != 0 {
			t.Fatalf("trailing bucket %d was not zero-filled: %#v", index+5, point)
		}
	}
	top, err := database.NetworkAnalyticsTopIPs(t.Context(), AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsFineResolutionSeconds, OutputResolution: 3600,
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top.ByConnections) != 1 || top.ByConnections[0].SourceIP != "81.2.69.142" ||
		top.ByConnections[0].ConnectionShare != 1 || top.ByConnections[0].TrafficShare != 1 {
		t.Fatalf("unexpected Top IP result: %#v", top)
	}
}

func TestNetworkAnalyticsTransactionRollbackCanRetry(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	closedAt := now + 1000
	connection := NetworkConnection{
		ActivityID: "00000000000000000000000000000021",
		Protocol:   "udp", State: "accepted", ClientID: "client", ProxyID: "dns",
		ExternalAddr: "203.0.113.10:5000", ExternalIP: "203.0.113.10",
		AcceptedAt: now, LastActivityAt: closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	connection.State = "failed"
	connection.ClosedAt = &closedAt
	connection.UpBytes = 10
	connection.UpPackets = 1
	if _, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "not-valid"},
		closedAt,
	); err == nil {
		t.Fatal("invalid classification did not fail the aggregation transaction")
	}
	detail, err := database.NetworkConnectionDetail(t.Context(), connection.ActivityID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != "accepted" || detail.AnalyticsAggregatedAt != nil {
		t.Fatalf("failed aggregation transaction was partially committed: %#v", detail.NetworkConnection)
	}
	result, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "reserved"},
		closedAt+1,
	)
	if err != nil || !result.Aggregated {
		t.Fatalf("retry failed: result=%#v error=%v", result, err)
	}
	overview, err := database.NetworkAnalyticsOverview(t.Context(), AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsHourlyResolutionSeconds, OutputResolution: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Connections != 1 || overview.Failures != 1 ||
		overview.UpBytes != 10 {
		t.Fatalf("retry produced incorrect totals: %#v", overview)
	}
}

func TestNetworkAnalyticsClipsSampleOverflowAndPreservesPackets(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	closedAt := now + (10 * time.Minute).Milliseconds()
	connection := NetworkConnection{
		ActivityID: "00000000000000000000000000000022",
		Protocol:   "udp", State: "accepted", ClientID: "client", ProxyID: "dns",
		ExternalAddr: "203.0.113.10:5000", ExternalIP: "203.0.113.10",
		AcceptedAt: now, LastActivityAt: closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertNetworkSample(t.Context(), NetworkSample{
		ActivityID:   connection.ActivityID,
		BucketAt:     now + (5 * time.Minute).Milliseconds(),
		UpBytesDelta: 120, DownBytesDelta: 25,
		UpPacketsDelta: 12, DownPacketsDelta: 3,
	}); err != nil {
		t.Fatal(err)
	}
	connection.State = "failed"
	connection.ClosedAt = &closedAt
	connection.UpBytes = 100
	connection.DownBytes = 50
	connection.UpPackets = 10
	connection.DownPackets = 5
	result, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "reserved"},
		closedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.SampleOverflow || result.EstimatedBytes != 25 {
		t.Fatalf("overflow result=%#v", result)
	}
	var connections, failures, upBytes, downBytes, upPackets, downPackets, estimatedDown int64
	err = database.db.QueryRowContext(t.Context(), `SELECT
		SUM(connections), SUM(failures), SUM(up_bytes), SUM(down_bytes),
		SUM(up_packets), SUM(down_packets), SUM(estimated_down_bytes)
		FROM network_history_ip_rollups
		WHERE resolution_seconds=? AND source_ip=?`,
		AnalyticsFineResolutionSeconds,
		connection.ExternalIP,
	).Scan(
		&connections,
		&failures,
		&upBytes,
		&downBytes,
		&upPackets,
		&downPackets,
		&estimatedDown,
	)
	if err != nil {
		t.Fatal(err)
	}
	if connections != 1 || failures != 1 ||
		upBytes != 100 || downBytes != 50 ||
		upPackets != 10 || downPackets != 5 ||
		estimatedDown != 25 {
		t.Fatalf(
			"clipped rollup=%d/%d bytes=%d/%d packets=%d/%d estimated_down=%d",
			connections,
			failures,
			upBytes,
			downBytes,
			upPackets,
			downPackets,
			estimatedDown,
		)
	}
}

func TestCleanupPreservesTerminalConnectionUntilAnalyticsCompletes(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().UnixMilli()
	acceptedAt := now - (2 * time.Hour).Milliseconds()
	closedAt := acceptedAt + 1000
	connection := NetworkConnection{
		ActivityID: "00000000000000000000000000000031",
		Protocol:   "tcp", State: "closed", ClientID: "client", ProxyID: "proxy",
		ExternalAddr: "192.0.2.1:1000", ExternalIP: "192.0.2.1",
		AcceptedAt: acceptedAt, LastActivityAt: closedAt, ClosedAt: &closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err := database.Cleanup(
		t.Context(),
		now,
		time.Hour,
		time.Hour,
		time.Hour,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.NetworkConnectionDetail(t.Context(), connection.ActivityID, 0); err != nil {
		t.Fatalf("cleanup deleted an unaggregated terminal connection: %v", err)
	}
	if _, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		GeoClassification{Kind: "reserved"},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := database.Cleanup(
		t.Context(),
		now,
		time.Hour,
		time.Hour,
		time.Hour,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.NetworkConnectionDetail(t.Context(), connection.ActivityID, 0); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cleanup retained an expired aggregated connection: %v", err)
	}
}

func TestAnalyticsMetadataPreservesBackfillProgressOnDegradation(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().UnixMilli()
	completeFrom := now - time.Hour.Milliseconds()
	if err := database.EnsureAnalyticsMeta(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAnalyticsMeta(
		t.Context(),
		"backfilling",
		completeFrom,
		10,
		"",
		now+1,
	); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAnalyticsMeta(
		t.Context(),
		"degraded",
		0,
		0,
		"busy",
		now+2,
	); err != nil {
		t.Fatal(err)
	}
	metadata, err := database.AnalyticsMeta(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.CompleteFrom != completeFrom ||
		metadata.ProcessedConnections != 10 ||
		metadata.Status != "degraded" {
		t.Fatalf("backfill progress was lost: %#v", metadata)
	}
}

func TestNetworkAnalyticsTopIPOrderingAndCleanup(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	createFinalizedAnalyticsConnection(t, database, "00000000000000000000000000000011", "198.51.100.1", now, 10)
	createFinalizedAnalyticsConnection(t, database, "00000000000000000000000000000012", "198.51.100.1", now+1000, 10)
	createFinalizedAnalyticsConnection(t, database, "00000000000000000000000000000013", "203.0.113.5", now+2000, 1000)
	query := AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsHourlyResolutionSeconds, OutputResolution: 3600,
	}
	top, err := database.NetworkAnalyticsTopIPs(t.Context(), query, 5)
	if err != nil {
		t.Fatal(err)
	}
	if top.ByConnections[0].SourceIP != "198.51.100.1" ||
		top.ByTraffic[0].SourceIP != "203.0.113.5" {
		t.Fatalf("rankings did not preserve independent ordering: %#v", top)
	}
	if err := database.CleanupAnalytics(
		t.Context(),
		now+(100*24*time.Hour).Milliseconds(),
		48*time.Hour,
		90*24*time.Hour,
	); err != nil {
		t.Fatal(err)
	}
	overview, err := database.NetworkAnalyticsOverview(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Connections != 0 {
		t.Fatalf("expired hourly rollups remain: %#v", overview)
	}
}

func TestNetworkAnalyticsScopesTieBreaksAndDimensionCountries(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	createFinalizedAnalyticsConnectionForScope(
		t,
		database,
		"00000000000000000000000000000041",
		"client-a",
		"shared",
		"tcp",
		"198.51.100.2",
		now,
		100,
		GeoClassification{Kind: "country", CountryCode: "GB", DatabaseFingerprint: "one"},
	)
	createFinalizedAnalyticsConnectionForScope(
		t,
		database,
		"00000000000000000000000000000042",
		"client-b",
		"shared",
		"udp",
		"198.51.100.10",
		now,
		100,
		GeoClassification{Kind: "country", CountryCode: "CN", DatabaseFingerprint: "one"},
	)
	query := AnalyticsQuery{
		From: now, To: now + time.Hour.Milliseconds(),
		BaseResolution: AnalyticsHourlyResolutionSeconds, OutputResolution: 3600,
	}
	top, err := database.NetworkAnalyticsTopIPs(t.Context(), query, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top.ByConnections) != 2 ||
		top.ByConnections[0].SourceIP != "198.51.100.10" ||
		top.ByTraffic[0].SourceIP != "198.51.100.10" {
		t.Fatalf("stable source IP tie-break was not applied: %#v", top)
	}
	if _, err := database.NetworkAnalyticsTopIPs(t.Context(), query, 6); err == nil {
		t.Fatal("invalid Top K was accepted")
	}
	clientQuery := query
	clientQuery.ClientID = "client-a"
	clientTop, err := database.NetworkAnalyticsTopIPs(t.Context(), clientQuery, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(clientTop.ByConnections) != 1 ||
		clientTop.ByConnections[0].SourceIP != "198.51.100.2" ||
		clientTop.ByConnections[0].CountryCode != "GB" {
		t.Fatalf("client scope leaked another client: %#v", clientTop)
	}
	dimensions, err := database.NetworkAnalyticsDimensions(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(dimensions.Items) != 2 {
		t.Fatalf("same proxy ID was merged across clients: %#v", dimensions.Items)
	}
	countries := map[string]string{}
	for _, item := range dimensions.Items {
		countries[item.ClientID] = item.TopCountryCode
	}
	if countries["client-a"] != "GB" || countries["client-b"] != "CN" {
		t.Fatalf("dimension top countries=%#v", countries)
	}
	emptyQuery := query
	emptyQuery.From -= time.Hour.Milliseconds()
	emptyQuery.To -= time.Hour.Milliseconds()
	empty, err := database.NetworkAnalyticsOverview(t.Context(), emptyQuery)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Connections != 0 || len(empty.Series) != 1 {
		t.Fatalf("empty range was not zero-filled: %#v", empty)
	}
}

func createFinalizedAnalyticsConnection(
	t *testing.T,
	database *Store,
	id string,
	ip string,
	acceptedAt int64,
	bytes int64,
) {
	t.Helper()
	createFinalizedAnalyticsConnectionForScope(
		t,
		database,
		id,
		"client",
		"proxy",
		"tcp",
		ip,
		acceptedAt,
		bytes,
		GeoClassification{Kind: "reserved"},
	)
}

func createFinalizedAnalyticsConnectionForScope(
	t *testing.T,
	database *Store,
	id string,
	clientID string,
	proxyID string,
	protocol string,
	ip string,
	acceptedAt int64,
	bytes int64,
	geo GeoClassification,
) {
	t.Helper()
	closedAt := acceptedAt + 100
	connection := NetworkConnection{
		ActivityID: id, Protocol: protocol, State: "accepted",
		ClientID: clientID, ProxyID: proxyID,
		ExternalAddr: ip + ":1000", ExternalIP: ip,
		AcceptedAt: acceptedAt, LastActivityAt: closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	connection.State = "closed"
	connection.ClosedAt = &closedAt
	connection.UpBytes = bytes
	if _, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		geo,
		closedAt,
	); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkAnalyticsQueryPlanUsesRollupIndexes(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	for index := range 200 {
		clientID := "other"
		if index == 0 {
			clientID = "client"
		}
		if _, err := database.db.ExecContext(t.Context(), `INSERT INTO network_history_ip_rollups (
			resolution_seconds, bucket_at, client_id, proxy_id, protocol, source_ip,
			geo_kind, first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, 'tcp', ?, 'reserved', ?, ?)`,
			AnalyticsHourlyResolutionSeconds,
			now+int64(index)*time.Hour.Milliseconds(),
			clientID,
			"proxy",
			"192.0.2.1",
			now,
			now,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.db.ExecContext(t.Context(), "ANALYZE"); err != nil {
		t.Fatal(err)
	}
	rows, err := database.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN
		SELECT source_ip, SUM(connections), SUM(up_bytes+down_bytes)
		FROM network_history_ip_rollups
		WHERE resolution_seconds=? AND client_id=? AND bucket_at>=? AND bucket_at<?
		GROUP BY source_ip`,
		AnalyticsHourlyResolutionSeconds,
		"client",
		now,
		now+(201*time.Hour).Milliseconds(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	value := plan.String()
	if !strings.Contains(value, "idx_history_ip_rollup_client_bucket") ||
		strings.Contains(value, "network_connections") {
		t.Fatalf("analytics query did not use the client rollup index:\n%s", value)
	}
}
