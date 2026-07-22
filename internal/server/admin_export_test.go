package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/config"
	exportarchive "github.com/portpilot/portpilot/internal/export"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

func TestAuditExportReturnsCompleteZIPAndFinalizesAudit(t *testing.T) {
	exportTemp := t.TempDir()
	t.Setenv("TMPDIR", exportTemp)
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Config: config.DefaultServerConfig(), Events: database,
		Admin: NewAdminAuth(nil, 10), Logger: zap.NewNop(),
	}
	admin := &adminSession{
		ID: "export-session", UserName: "admin", CSRF: "csrf-token",
		AuthenticatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), SessionTTL: "1h",
	}
	app.Admin.sessions[admin.ID] = admin
	t.Cleanup(func() {
		if app.exports != nil {
			_ = app.exports.close(context.Background())
		}
		_ = database.Close()
	})
	seed, err := database.WriteAudit(t.Context(), store.AuditEvent{
		OccurredAt: time.Now().Add(-time.Minute).UnixMilli(),
		Category:   "system", EventType: "system_start", Outcome: "success",
		ActorType: "system", Summary: "needle event",
		Details: map[string]any{
			"client_token": "must-not-leak",
			"nested":       map[string]any{"password": "also-secret", "visible": "kept"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/exports/audit-events",
		bytes.NewBufferString(`{"format":"json","filters":{"query":"needle"}}`),
	)
	request.RemoteAddr = "198.51.100.7:1234"
	request.AddCookie(testAdminCookie(admin.ID))
	request.Header.Set("X-CSRF-Token", admin.CSRF)
	response := httptest.NewRecorder()
	app.adminMux().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/zip" ||
		response.Header().Get("Content-Encoding") != "" ||
		!strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("unexpected export headers: %#v", response.Header())
	}
	entries, err := os.ReadDir(exportTemp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("export wrote files into TMPDIR: %#v", entries)
	}
	if response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
		t.Fatalf("content length=%q body=%d", response.Header().Get("Content-Length"), response.Body.Len())
	}
	reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 ||
		reader.File[0].Name != "audit_events.json" ||
		reader.File[1].Name != "manifest.json" {
		t.Fatalf("unexpected ZIP entries: %#v", reader.File)
	}
	data, err := readServerZipEntry(reader.File[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-leak")) ||
		bytes.Contains(data, []byte("also-secret")) ||
		!bytes.Contains(data, []byte("[REDACTED]")) {
		t.Fatalf("export secret redaction failed: %s", data)
	}
	var document struct {
		Filters map[string]any    `json:"filters"`
		Items   []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.Filters["query"] != "needle" || len(document.Items) != 1 {
		t.Fatalf("unexpected dataset: %#v", document)
	}
	var exported struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(document.Items[0], &exported); err != nil {
		t.Fatal(err)
	}
	if exported.ID != seed.ID {
		t.Fatalf("exported event=%q want=%q", exported.ID, seed.ID)
	}
	manifestData, err := readServerZipEntry(reader.File[1])
	if err != nil {
		t.Fatal(err)
	}
	var manifest exportarchive.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dataset != "audit_events" || manifest.Format != "json" ||
		manifest.RowCount != 1 || manifest.UncompressedBytes != int64(len(data)) {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}

	page, err := database.ListAudit(t.Context(), store.AuditQuery{
		Page: 1, PageSize: 25, EventType: "data_export",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Outcome != "success" ||
		page.Items[0].SourceAddr != "198.51.100.7" {
		t.Fatalf("export audit not finalized: %#v", page)
	}
	detail, err := database.AuditDetail(t.Context(), page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	encodedDetail, err := json.Marshal(detail.Details)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedDetail, []byte("needle")) ||
		detail.Details["dataset"] != "audit_events" ||
		detail.Details["row_count"] != float64(1) && detail.Details["row_count"] != 1 {
		t.Fatalf("export audit leaked filters or missed counts: %s", encodedDetail)
	}
}

func TestExportRequiresCSRFAndRejectsUnknownFilters(t *testing.T) {
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	app := &App{
		Config: config.DefaultServerConfig(), Events: database,
		Admin: NewAdminAuth(nil, 10), Logger: zap.NewNop(),
	}
	admin := &adminSession{
		ID: "session", UserName: "admin", CSRF: "csrf",
		AuthenticatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), SessionTTL: "1h",
	}
	app.Admin.sessions[admin.ID] = admin

	noCSRF := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/exports/audit-events",
		bytes.NewBufferString(`{"format":"csv","filters":{}}`),
	)
	noCSRF.AddCookie(testAdminCookie(admin.ID))
	noCSRFResponse := httptest.NewRecorder()
	app.adminMux().ServeHTTP(noCSRFResponse, noCSRF)
	if noCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", noCSRFResponse.Code)
	}

	unknown := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/exports/audit-events",
		bytes.NewBufferString(`{"format":"csv","filters":{"page":1}}`),
	)
	unknown.AddCookie(testAdminCookie(admin.ID))
	unknown.Header.Set("X-CSRF-Token", admin.CSRF)
	unknownResponse := httptest.NewRecorder()
	app.adminMux().ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusBadRequest ||
		!strings.Contains(unknownResponse.Body.String(), "invalid_export_query") {
		t.Fatalf("unknown filter status=%d body=%s", unknownResponse.Code, unknownResponse.Body.String())
	}
	page, err := database.ListAudit(t.Context(), store.AuditQuery{
		Page: 1, PageSize: 25, EventType: "data_export",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("invalid export created an audit: %#v", page)
	}
}

func TestExportManagerEnforcesConcurrencyAndRate(t *testing.T) {
	manager := newExportManager()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	t.Cleanup(func() { _ = manager.close(context.Background()) })

	release, reason := manager.acquire("session-a")
	if release == nil || reason != "" {
		t.Fatalf("first acquire release=%v reason=%q", release != nil, reason)
	}
	if second, reason := manager.acquire("session-b"); second != nil || reason != "busy" {
		t.Fatalf("parallel acquire release=%v reason=%q", second != nil, reason)
	}
	release()
	for index := 0; index < 4; index++ {
		release, reason = manager.acquire("session-b")
		if release == nil || reason != "" {
			t.Fatalf("acquire %d release=%v reason=%q", index, release != nil, reason)
		}
		release()
	}
	if release, reason = manager.acquire("session-b"); release != nil || reason != "rate_limited" {
		t.Fatalf("rate acquire release=%v reason=%q", release != nil, reason)
	}
}

func readServerZipEntry(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open ZIP entry: %w", err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read ZIP entry: %w", err)
	}
	return data, nil
}
