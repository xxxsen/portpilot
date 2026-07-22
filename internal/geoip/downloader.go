package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oschwald/maxminddb-golang/v2"
	"go.uber.org/zap"
)

const (
	remoteCheckInterval = 7 * 24 * time.Hour
	remoteStaleAfter    = 8 * 24 * time.Hour
	maxRemoteDownload   = 64 << 20
	maxExtractedMMDB    = 32 << 20
)

var errUpdateInProgress = errors.New("geoip update already in progress")

type ManagedOptions struct {
	HTTPClient       *http.Client
	Now              func() time.Time
	DisableScheduler bool
}

type remoteMetadata struct {
	Version             int    `json:"version"`
	SourceSHA256        string `json:"source_sha256"`
	ETag                string `json:"etag"`
	LastModified        string `json:"last_modified"`
	LastCheckedAt       int64  `json:"last_checked_at"`
	LastDownloadedAt    int64  `json:"last_downloaded_at"`
	DatabaseFingerprint string `json:"database_fingerprint"`
}

type updateFailure struct {
	code       string
	retryAfter time.Duration
	err        error
}

func (e *updateFailure) Error() string { return e.err.Error() }
func (e *updateFailure) Unwrap() error { return e.err }

type updater struct {
	source       Source
	databasePath string
	metadataPath string
	resolver     *Resolver
	client       *http.Client
	now          func() time.Time

	mu       sync.Mutex
	running  bool
	retries  int
	metadata remoteMetadata
}

func NewManaged(
	sourceValue string,
	databasePath string,
	metadataPath string,
	logger *zap.Logger,
	options ManagedOptions,
) (*Resolver, error) {
	source, err := ParseSource(sourceValue)
	if err != nil {
		return nil, fmt.Errorf("create geoip HTTP request: %w", err)
	}
	switch source.Type {
	case SourceDisabled:
		return New("", time.Hour, logger)
	case SourceLocal:
		return New(source.Local, time.Hour, logger)
	case SourceRemote:
	default:
		return nil, errors.New("unsupported geoip source type")
	}
	if databasePath == "" || metadataPath == "" {
		return nil, errors.New("remote geoip source requires managed cache paths")
	}
	resolver, err := New("", time.Hour, logger)
	if err != nil {
		return nil, fmt.Errorf("create geoip HTTP request: %w", err)
	}
	resolver.path = databasePath
	resolver.sourceType = SourceRemote
	resolver.setUpdateState("idle")
	if _, statErr := os.Stat(databasePath); statErr == nil {
		if reloadErr := resolver.reload(true); reloadErr != nil {
			resolver.setErrorCode("cached_database_invalid", reloadErr)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		resolver.Close()
		return nil, fmt.Errorf("inspect cached geoip database: %w", statErr)
	} else {
		resolver.setErrorCode("database_missing", errors.New("cached geoip database is unavailable"))
	}
	update, err := newUpdater(source, databasePath, metadataPath, resolver, options)
	if err != nil {
		resolver.Close()
		return nil, fmt.Errorf("create geoip HTTP request: %w", err)
	}
	resolver.updater = update
	update.publishMetadata()
	if !options.DisableScheduler {
		resolver.wg.Add(1)
		go update.run(resolver.ctx)
	}
	return resolver, nil
}

func newUpdater(
	source Source,
	databasePath string,
	metadataPath string,
	resolver *Resolver,
	options ManagedOptions,
) (*updater, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	client := secureHTTPClient(options.HTTPClient)
	update := &updater{
		source: source, databasePath: databasePath, metadataPath: metadataPath,
		resolver: resolver, client: client, now: now,
		metadata: remoteMetadata{Version: 1, SourceSHA256: source.Hash()},
	}
	if err := update.loadMetadata(); err != nil {
		resolver.logger.Warn("geoip metadata ignored", zap.String("reason_code", "metadata_invalid"))
	}
	if update.metadata.SourceSHA256 != source.Hash() {
		update.metadata = remoteMetadata{Version: 1, SourceSHA256: source.Hash()}
	}
	if err := cleanupTemporaryFiles(filepath.Dir(databasePath), now()); err != nil {
		return nil, fmt.Errorf("create geoip HTTP request: %w", err)
	}
	return update, nil
}

func secureHTTPClient(base *http.Client) *http.Client {
	var client http.Client
	if base != nil {
		client = *base
	} else {
		client.Transport = &http.Transport{
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext:           (&netDialer).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			DisableCompression:    true,
		}
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("geoip redirect limit exceeded")
		}
		if request.URL.Scheme != "https" {
			return errors.New("geoip redirect must remain HTTPS")
		}
		if len(via) > 0 && !strings.EqualFold(via[len(via)-1].URL.Host, request.URL.Host) {
			request.Header.Del("Authorization")
			request.Header.Del("Cookie")
			for name := range request.Header {
				if strings.HasPrefix(strings.ToLower(name), "x-") {
					request.Header.Del(name)
				}
			}
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		return nil
	}
	return &client
}

var netDialer = struct {
	DialContext func(context.Context, string, string) (net.Conn, error)
}{
	DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
}

func (r *Resolver) CheckForUpdate(ctx context.Context) error {
	if r == nil || r.updater == nil {
		return errors.New("geoip source is not remote")
	}
	return r.updater.checkAndSchedule(ctx)
}

func (u *updater) run(ctx context.Context) {
	defer u.resolver.wg.Done()
	for {
		next := u.nextCheck()
		u.resolver.nextCheckAt.Store(next.UnixMilli())
		timer := time.NewTimer(maxDuration(0, next.Sub(u.now())))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := u.checkAndSchedule(ctx); err != nil {
				u.resolver.logger.Warn(
					"geoip remote update failed",
					zap.String("reason_code", failureCode(err)),
				)
			}
		}
	}
}

func (u *updater) nextCheck() time.Time {
	if next := u.resolver.nextCheckAt.Load(); next > 0 {
		return time.UnixMilli(next)
	}
	if u.resolver.reader.Load() == nil || u.metadata.LastCheckedAt == 0 {
		return u.now()
	}
	return time.UnixMilli(u.metadata.LastCheckedAt).Add(remoteCheckInterval)
}

func (u *updater) checkAndSchedule(ctx context.Context) error {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return errUpdateInProgress
	}
	u.running = true
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.running = false
		u.mu.Unlock()
	}()

	err := u.performCheck(ctx)
	now := u.now()
	if err == nil {
		u.retries = 0
		u.resolver.nextCheckAt.Store(now.Add(remoteCheckInterval).UnixMilli())
		u.resolver.lastError.Store(nil)
		u.resolver.lastErrorCode.Store(nil)
		return nil
	}
	u.retries++
	u.resolver.retryCount.Add(1)
	delay := retryDelay(u.retries)
	var failure *updateFailure
	if errors.As(err, &failure) && failure.retryAfter >= time.Hour && failure.retryAfter <= 24*time.Hour {
		delay = failure.retryAfter
	}
	u.resolver.nextCheckAt.Store(now.Add(delay).UnixMilli())
	u.resolver.setErrorCode(failureCode(err), sanitizeRemoteError(err))
	return err
}

func (u *updater) performCheck(ctx context.Context) error {
	u.resolver.checkCount.Add(1)
	u.resolver.setUpdateState("checking")
	defer u.resolver.setUpdateState("idle")

	headCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	head, err := u.request(headCtx, http.MethodHead)
	cancel()
	if err != nil {
		return &updateFailure{code: "remote_check_failed", err: err}
	}
	defer func() { _ = head.Body.Close() }()
	switch {
	case head.StatusCode == http.StatusNotModified:
		return u.recordNotModified()
	case head.StatusCode == http.StatusMethodNotAllowed || head.StatusCode == http.StatusNotImplemented:
		return u.download(ctx)
	case head.StatusCode >= 200 && head.StatusCode < 300:
		if sameRemoteVersion(u.metadata, head.Header) {
			return u.recordNotModified()
		}
		return u.download(ctx)
	default:
		return responseFailure("remote_check_failed", head)
	}
}

func (u *updater) request(ctx context.Context, method string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, u.source.Remote.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create geoip HTTP request: %w", err)
	}
	request.Header.Set("Accept-Encoding", "identity")
	if u.metadata.ETag != "" {
		request.Header.Set("If-None-Match", u.metadata.ETag)
	}
	if u.metadata.LastModified != "" {
		request.Header.Set("If-Modified-Since", u.metadata.LastModified)
	}
	response, err := u.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("perform geoip HTTP request: %w", err)
	}
	return response, nil
}

func (u *updater) download(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	u.resolver.setUpdateState("downloading")
	response, err := u.request(ctx, http.MethodGet)
	if err != nil {
		return &updateFailure{code: "remote_download_failed", err: err}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified {
		return u.recordNotModified()
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseFailure("remote_download_failed", response)
	}
	if length := response.ContentLength; length > maxRemoteDownload {
		return &updateFailure{code: "download_too_large", err: errors.New("geoip download exceeds 64 MiB")}
	}

	dir := filepath.Dir(u.databasePath)
	download, err := os.CreateTemp(dir, ".country-*.download")
	if err != nil {
		return &updateFailure{code: "cache_write_failed", err: err}
	}
	downloadPath := download.Name()
	defer func() { _ = os.Remove(downloadPath) }()
	if err := os.Chmod(downloadPath, 0o600); err != nil {
		_ = download.Close()
		return &updateFailure{code: "cache_write_failed", err: err}
	}
	written, copyErr := io.Copy(download, io.LimitReader(response.Body, maxRemoteDownload+1))
	if copyErr == nil {
		copyErr = download.Sync()
	}
	closeErr := download.Close()
	if copyErr != nil {
		return &updateFailure{code: "remote_download_failed", err: copyErr}
	}
	if closeErr != nil {
		return &updateFailure{code: "cache_write_failed", err: closeErr}
	}
	if written > maxRemoteDownload {
		return &updateFailure{code: "download_too_large", err: errors.New("geoip download exceeds 64 MiB")}
	}
	u.resolver.downloadCount.Add(1)
	//nolint:gosec // io.Copy returns a non-negative byte count.
	u.resolver.downloadBytes.Add(uint64(written))

	u.resolver.setUpdateState("validating")
	candidatePath, fingerprint, err := prepareCandidate(downloadPath, dir)
	if err != nil {
		code := failureCode(err)
		if code == "unsupported_geoip_format" {
			u.resolver.formatRejected.Add(1)
		} else {
			u.resolver.validationFailure.Add(1)
		}
		return err
	}
	defer func() { _ = os.Remove(candidatePath) }()

	if err := os.Rename(candidatePath, u.databasePath); err != nil {
		return &updateFailure{code: "install_failed", err: err}
	}
	if err := syncDirectory(dir); err != nil {
		return &updateFailure{code: "install_failed", err: err}
	}
	now := u.now().UTC().UnixMilli()
	u.metadata = remoteMetadata{
		Version: 1, SourceSHA256: u.source.Hash(),
		ETag: response.Header.Get("ETag"), LastModified: response.Header.Get("Last-Modified"),
		LastCheckedAt: now, LastDownloadedAt: now, DatabaseFingerprint: fingerprint,
	}
	if err := u.saveMetadata(); err != nil {
		return &updateFailure{code: "metadata_write_failed", err: err}
	}
	if err := u.resolver.reload(false); err != nil {
		return &updateFailure{code: "installed_database_invalid", err: err}
	}
	u.publishMetadata()
	u.resolver.installSuccess.Add(1)
	return nil
}

//nolint:gocognit,gocyclo // Format validation intentionally keeps raw MMDB and tar.gz cleanup in one bounded transaction.
func prepareCandidate(downloadPath, dir string) (string, string, error) {
	//nolint:gosec // downloadPath is created by os.CreateTemp in the managed GeoIP cache.
	download, err := os.Open(downloadPath)
	if err != nil {
		return "", "", &updateFailure{code: "cache_read_failed", err: err}
	}
	defer func() { _ = download.Close() }()
	prefix := make([]byte, 512)
	count, readErr := io.ReadFull(download, prefix)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", "", &updateFailure{code: "cache_read_failed", err: readErr}
	}
	prefix = prefix[:count]
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return "", "", &updateFailure{code: "cache_read_failed", err: err}
	}
	if bytes.HasPrefix(prefix, []byte("PK\x03\x04")) || looksLikeCSV(prefix) {
		return "", "", &updateFailure{
			code: "unsupported_geoip_format",
			err:  errors.New("geoip ZIP and CSV sources are unsupported; use MMDB or tar.gz"),
		}
	}

	candidate, err := os.CreateTemp(dir, ".country-*.mmdb")
	if err != nil {
		return "", "", &updateFailure{code: "cache_write_failed", err: err}
	}
	candidatePath := candidate.Name()
	success := false
	defer func() {
		if !success {
			_ = candidate.Close()
			_ = os.Remove(candidatePath)
		}
	}()
	if err := os.Chmod(candidatePath, 0o600); err != nil {
		return "", "", &updateFailure{code: "cache_write_failed", err: err}
	}

	//nolint:nestif // Tar iteration must keep all archive validation and cleanup paths explicit.
	if bytes.HasPrefix(prefix, []byte{0x1f, 0x8b}) {
		gzipReader, err := gzip.NewReader(download)
		if err != nil {
			return "", "", &updateFailure{code: "unsupported_geoip_format", err: err}
		}
		tarReader := tar.NewReader(gzipReader)
		found := 0
		for {
			header, nextErr := tarReader.Next()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				_ = gzipReader.Close()
				return "", "", &updateFailure{code: "unsupported_geoip_format", err: nextErr}
			}
			if header.Typeflag != tar.TypeReg || !strings.HasSuffix(strings.ToLower(filepath.Base(header.Name)), ".mmdb") {
				continue
			}
			found++
			if found > 1 {
				_ = gzipReader.Close()
				return "", "", &updateFailure{code: "multiple_mmdb_files", err: errors.New("geoip archive contains multiple MMDB files")}
			}
			written, copyErr := io.Copy(candidate, io.LimitReader(tarReader, maxExtractedMMDB+1))
			if copyErr != nil {
				_ = gzipReader.Close()
				return "", "", &updateFailure{code: "extract_failed", err: copyErr}
			}
			if written > maxExtractedMMDB {
				_ = gzipReader.Close()
				return "", "", &updateFailure{code: "mmdb_too_large", err: errors.New("extracted MMDB exceeds 32 MiB")}
			}
		}
		if err := gzipReader.Close(); err != nil {
			return "", "", &updateFailure{code: "extract_failed", err: err}
		}
		if found != 1 {
			return "", "", &updateFailure{code: "mmdb_missing", err: errors.New("geoip archive does not contain exactly one MMDB file")}
		}
	} else {
		written, copyErr := io.Copy(candidate, io.LimitReader(download, maxExtractedMMDB+1))
		if copyErr != nil {
			return "", "", &updateFailure{code: "cache_read_failed", err: copyErr}
		}
		if written > maxExtractedMMDB {
			return "", "", &updateFailure{code: "mmdb_too_large", err: errors.New("MMDB exceeds 32 MiB")}
		}
	}
	if err := candidate.Sync(); err != nil {
		return "", "", &updateFailure{code: "cache_write_failed", err: err}
	}
	if err := candidate.Close(); err != nil {
		return "", "", &updateFailure{code: "cache_write_failed", err: err}
	}
	fingerprint, err := validateCandidate(candidatePath)
	if err != nil {
		return "", "", err
	}
	success = true
	return candidatePath, fingerprint, nil
}

func validateCandidate(path string) (string, error) {
	reader, err := maxminddb.Open(path)
	if err != nil {
		return "", &updateFailure{code: "invalid_mmdb", err: err}
	}
	databaseType := strings.ToLower(reader.Metadata.DatabaseType)
	if !strings.Contains(databaseType, "country") && !strings.Contains(databaseType, "city") {
		_ = reader.Close()
		return "", &updateFailure{code: "invalid_database_type", err: errors.New("MMDB does not provide country data")}
	}
	if err := reader.Close(); err != nil {
		return "", &updateFailure{code: "invalid_mmdb", err: err}
	}
	fingerprint, err := fingerprintFile(path)
	if err != nil {
		return "", &updateFailure{code: "fingerprint_failed", err: err}
	}
	return fingerprint, nil
}

func looksLikeCSV(prefix []byte) bool {
	if len(prefix) == 0 || !utf8.Valid(prefix) || !bytes.Contains(prefix, []byte(",")) {
		return false
	}
	printable := 0
	for _, value := range prefix {
		if value == '\n' || value == '\r' || value == '\t' || value >= 0x20 {
			printable++
		}
	}
	return printable == len(prefix)
}

func (u *updater) recordNotModified() error {
	now := u.now().UTC().UnixMilli()
	u.metadata.Version = 1
	u.metadata.SourceSHA256 = u.source.Hash()
	u.metadata.LastCheckedAt = now
	if err := u.saveMetadata(); err != nil {
		return &updateFailure{code: "metadata_write_failed", err: err}
	}
	u.publishMetadata()
	u.resolver.notModified.Add(1)
	return nil
}

func (u *updater) loadMetadata() error {
	data, err := os.ReadFile(u.metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read geoip metadata: %w", err)
	}
	var value remoteMetadata
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode geoip metadata: %w", err)
	}
	if value.Version != 1 {
		return errors.New("unsupported geoip metadata version")
	}
	u.metadata = value
	return nil
}

func (u *updater) saveMetadata() error {
	dir := filepath.Dir(u.metadataPath)
	temporary, err := os.CreateTemp(dir, ".metadata-*.json")
	if err != nil {
		return fmt.Errorf("create geoip metadata file: %w", err)
	}
	path := temporary.Name()
	defer func() { _ = os.Remove(path) }()
	if err := os.Chmod(path, 0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set geoip metadata permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(u.metadata); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode geoip metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync geoip metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, u.metadataPath); err != nil {
		return fmt.Errorf("install geoip metadata: %w", err)
	}
	return syncDirectory(dir)
}

func (u *updater) publishMetadata() {
	u.resolver.lastCheckedAt.Store(u.metadata.LastCheckedAt)
	u.resolver.lastDownloadedAt.Store(u.metadata.LastDownloadedAt)
	if u.metadata.LastCheckedAt > 0 {
		u.resolver.nextCheckAt.Store(time.UnixMilli(u.metadata.LastCheckedAt).Add(remoteCheckInterval).UnixMilli())
	}
}

func cleanupTemporaryFiles(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read geoip cache directory: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".country-") ||
			(!strings.HasSuffix(name, ".download") && !strings.HasSuffix(name, ".mmdb")) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect temporary geoip file: %w", err)
		}
		if now.Sub(info.ModTime()) <= time.Hour {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("remove stale geoip file: %w", err)
		}
	}
	return nil
}

func sameRemoteVersion(metadata remoteMetadata, header http.Header) bool {
	if metadata.ETag != "" && header.Get("ETag") != "" {
		return metadata.ETag == header.Get("ETag")
	}
	if metadata.LastModified != "" && header.Get("Last-Modified") != "" {
		return metadata.LastModified == header.Get("Last-Modified")
	}
	return false
}

func responseFailure(code string, response *http.Response) error {
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		code = "remote_auth_failed"
	}
	return &updateFailure{
		code: code, retryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
		err: fmt.Errorf("geoip endpoint returned HTTP %d", response.StatusCode),
	}
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func failureCode(err error) string {
	var failure *updateFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "update_failed"
}

func sanitizeRemoteError(err error) error {
	return errors.New(failureCode(err))
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Hour << min(attempt-1, 4)
	if delay > 24*time.Hour {
		return 24 * time.Hour
	}
	return delay
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func syncDirectory(path string) error {
	//nolint:gosec // path is a validated managed cache directory.
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open cache directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync cache directory: %w", err)
	}
	return nil
}
