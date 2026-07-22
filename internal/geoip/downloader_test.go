package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteManagedResolverDownloadsAndConditionallyChecks(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mmdb")
	writeCountryDatabase(t, sourcePath, "GB")
	//nolint:gosec // sourcePath is generated under t.TempDir.
	database, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var gets atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			return
		}
		gets.Add(1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(database)
	}))
	defer server.Close()

	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewManaged(
		server.URL+"/country.tar.gz?license_key=secret",
		filepath.Join(cache, "country.mmdb"),
		filepath.Join(cache, "metadata.json"),
		nil,
		ManagedOptions{HTTPClient: server.Client(), DisableScheduler: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)

	if err := resolver.CheckForUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	result := resolver.Resolve(netip.MustParseAddr("81.2.69.142"))
	if result.Kind != "country" || result.CountryCode != "GB" {
		t.Fatalf("lookup=%#v", result)
	}
	status := resolver.Status()
	if status.Status != "ready" || status.SourceType != "remote" ||
		status.LastCheckedAt == 0 || status.LastDownloadedAt == 0 ||
		status.DatabaseFingerprint == "" || status.LastErrorCode != "" {
		t.Fatalf("status=%#v", status)
	}
	if err := resolver.CheckForUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 {
		t.Fatalf("GET requests=%d, want 1", gets.Load())
	}
	//nolint:gosec // cache is generated under t.TempDir.
	metadata, err := os.ReadFile(filepath.Join(cache, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadata, []byte("secret")) || bytes.Contains(metadata, []byte(server.URL)) {
		t.Fatalf("metadata leaked source credentials: %s", metadata)
	}
}

func TestRemoteManagedResolverAcceptsTarGzAndKeepsOldReaderOnBadUpdate(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.mmdb")
	writeCountryDatabase(t, sourcePath, "US")
	//nolint:gosec // sourcePath is generated under t.TempDir.
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := tarGzMMDB(t, raw)
	var bad atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := `"good"`
		if bad.Load() {
			etag = `"bad"`
		}
		w.Header().Set("ETag", etag)
		if r.Method == http.MethodHead {
			return
		}
		if bad.Load() {
			_, _ = w.Write([]byte("name,network\ninvalid,csv\n"))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	cache := filepath.Join(dir, "geoip")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewManaged(
		server.URL+"/country",
		filepath.Join(cache, "country.mmdb"),
		filepath.Join(cache, "metadata.json"),
		nil,
		ManagedOptions{HTTPClient: server.Client(), DisableScheduler: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	if err := resolver.CheckForUpdate(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := resolver.Resolve(netip.MustParseAddr("81.2.69.142"))
	bad.Store(true)
	err = resolver.CheckForUpdate(t.Context())
	if err == nil || failureCode(err) != "unsupported_geoip_format" {
		t.Fatalf("bad update error=%v code=%s", err, failureCode(err))
	}
	after := resolver.Resolve(netip.MustParseAddr("81.2.69.142"))
	if before.CountryCode != "US" || after.CountryCode != "US" ||
		before.DatabaseFingerprint != after.DatabaseFingerprint {
		t.Fatalf("old reader was not retained: before=%#v after=%#v", before, after)
	}
	status := resolver.Status()
	if status.Status != "degraded" || status.LastErrorCode != "unsupported_geoip_format" {
		t.Fatalf("status=%#v", status)
	}
}

func TestRemoteManagedResolverRejectsHTTPSDowngradeAndUsesRetryBackoff(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://example.test/country.mmdb")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	dir := t.TempDir()
	resolver, err := NewManaged(
		server.URL+"/country",
		filepath.Join(dir, "country.mmdb"),
		filepath.Join(dir, "metadata.json"),
		nil,
		ManagedOptions{HTTPClient: server.Client(), Now: func() time.Time { return now }, DisableScheduler: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	if err := resolver.CheckForUpdate(t.Context()); err == nil {
		t.Fatal("HTTPS downgrade was accepted")
	}
	if got := resolver.Status().NextCheckAt; got != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("first retry=%d", got)
	}
	if err := resolver.CheckForUpdate(t.Context()); err == nil {
		t.Fatal("second HTTPS downgrade was accepted")
	}
	if got := resolver.Status().NextCheckAt; got != now.Add(2*time.Hour).UnixMilli() {
		t.Fatalf("second retry=%d", got)
	}
}

func TestRemoteRedirectDoesNotForwardQueryToLocation(t *testing.T) {
	var query string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/country.mmdb")
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	client := source.Client()
	client.Transport = target.Client().Transport
	dir := t.TempDir()
	resolver, err := NewManaged(
		source.URL+"/country?license_key=secret",
		filepath.Join(dir, "country.mmdb"),
		filepath.Join(dir, "metadata.json"),
		nil,
		ManagedOptions{HTTPClient: client, DisableScheduler: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resolver.Close)
	_ = resolver.CheckForUpdate(context.Background())
	if query != "" {
		t.Fatalf("redirect target received source query %q", query)
	}
}

func TestPrepareCandidateRejectsZipAndOversizedRaw(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "country.download")
	if err := os.WriteFile(zipPath, []byte("PK\x03\x04fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareCandidate(zipPath, dir); failureCode(err) != "unsupported_geoip_format" {
		t.Fatalf("zip error=%v code=%s", err, failureCode(err))
	}

	largePath := filepath.Join(dir, "large.download")
	//nolint:gosec // largePath is generated under t.TempDir.
	file, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxExtractedMMDB + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareCandidate(largePath, dir); failureCode(err) != "mmdb_too_large" {
		t.Fatalf("large error=%v code=%s", err, failureCode(err))
	}
}

func tarGzMMDB(t *testing.T, raw []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "GeoLite2-Country/test.mmdb", Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
