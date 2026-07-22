package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	exportarchive "github.com/portpilot/portpilot/internal/export"
	"github.com/portpilot/portpilot/internal/store"
)

const exportRetryAfterSeconds = 2

type exportRequest struct {
	Format  string          `json:"format"`
	Filters json.RawMessage `json:"filters"`
}

type networkExportFilters struct {
	Scope    string `json:"scope"`
	Query    string `json:"query"`
	ClientID string `json:"client_id"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	DateFrom string `json:"date_from"`
	DateTo   string `json:"date_to"`
	Sort     string `json:"sort"`
}

type dimensionExportFilters struct {
	Range    string `json:"range"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	ClientID string `json:"client_id"`
	ProxyID  string `json:"proxy_id"`
	Protocol string `json:"protocol"`
	Query    string `json:"query"`
	Sort     string `json:"sort"`
}

type auditExportFilters struct {
	Query     string `json:"query"`
	Category  string `json:"category"`
	EventType string `json:"event_type"`
	Outcome   string `json:"outcome"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
}

type preparedExport struct {
	dataset      string
	format       string
	exportedAt   time.Time
	filters      map[string]any
	iterate      func(context.Context, func(any) error) (int, error)
	auditDetails map[string]any
	matchedRows  *int64
}

func (a *App) handleNetworkConnectionsExport(w http.ResponseWriter, r *http.Request) {
	a.handleExport(w, r, "network_connections")
}

func (a *App) handleNetworkDimensionsExport(w http.ResponseWriter, r *http.Request) {
	a.handleExport(w, r, "network_dimensions")
}

func (a *App) handleAuditEventsExport(w http.ResponseWriter, r *http.Request) {
	a.handleExport(w, r, "audit_events")
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request, dataset string) {
	session := adminFromContext(r)
	if session == nil || a.Events == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "export_unavailable"})
		return
	}
	frozen := time.Now().UTC()
	prepared, err := a.prepareExport(r, dataset, frozen)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_export_query", "detail": err.Error(),
		})
		return
	}
	filterHash, err := exportFilterHash(prepared.filters)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_export_query"})
		return
	}
	prepared.auditDetails["filters_sha256"] = filterHash
	event, err := a.Events.WriteAudit(r.Context(), store.AuditEvent{
		OccurredAt: frozen.UnixMilli(), Category: "admin", EventType: "data_export",
		Outcome: "pending", ActorType: "admin", ActorID: session.UserName,
		SourceAddr: a.requestSource(r), Summary: "导出管理数据",
		Details: cloneExportDetails(prepared.auditDetails),
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
		return
	}

	manager := a.exportManager()
	release, acquireReason := manager.acquire(session.ID)
	if release == nil {
		a.finalizeExportAudit(r.Context(), event.ID, "failure", "export_"+acquireReason, prepared.auditDetails)
		w.Header().Set("Retry-After", strconv.Itoa(exportRetryAfterSeconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "export_" + acquireReason})
		return
	}
	defer release()

	ctx, cancel := manager.operationContext(r.Context())
	defer cancel()
	started := time.Now()
	archive, buildErr := exportarchive.Build(
		prepared.dataset, prepared.format, prepared.exportedAt, prepared.filters,
		func(output io.Writer) (int, error) {
			return encodeExportRows(
				output, prepared.format, prepared.dataset, prepared.exportedAt,
				prepared.filters,
				func(yield func(any) error) (int, error) {
					return prepared.iterate(ctx, func(value any) error {
						if err := ctx.Err(); err != nil {
							return fmt.Errorf("export context: %w", err)
						}
						return yield(value)
					})
				},
			)
		},
	)
	//nolint:gosec // A monotonic elapsed duration is non-negative.
	manager.metrics.GeneratedNanos.Add(uint64(time.Since(started)))
	defer archiveRelease(archive)
	if buildErr != nil {
		manager.metrics.Failed.Add(1)
		a.handleExportBuildError(ctx, w, event.ID, prepared, buildErr)
		return
	}
	details := cloneExportDetails(prepared.auditDetails)
	details["row_count"] = archive.Manifest.RowCount
	details["uncompressed_bytes"] = archive.Manifest.UncompressedBytes
	details["archive_bytes"] = archive.Size()

	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="portpilot-%s-%s.zip"`,
		prepared.dataset, prepared.exportedAt.Format("20060102T150405Z"),
	))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(archive.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	written, writeErr := archive.WriteToContext(ctx, w)
	if writeErr != nil || written != archive.Size() {
		if errors.Is(writeErr, context.Canceled) || errors.Is(writeErr, context.DeadlineExceeded) {
			manager.metrics.Canceled.Add(1)
		}
		manager.metrics.Failed.Add(1)
		details["archive_bytes_sent"] = written
		a.finalizeExportAudit(ctx, event.ID, "failure", "response_write_failed", details)
		return
	}
	manager.metrics.Succeeded.Add(1)
	//nolint:gosec // Archive row and byte limits guarantee non-negative bounded values.
	manager.metrics.Rows.Add(uint64(archive.Manifest.RowCount))
	//nolint:gosec // Archive row and byte limits guarantee non-negative bounded values.
	manager.metrics.UncompressedBytes.Add(uint64(archive.Manifest.UncompressedBytes))
	//nolint:gosec // Archive row and byte limits guarantee non-negative bounded values.
	manager.metrics.ArchiveBytes.Add(uint64(archive.Size()))
	a.finalizeExportAudit(ctx, event.ID, "success", "", details)
}

func archiveRelease(archive *exportarchive.Archive) {
	if archive != nil {
		archive.Release()
	}
}

func (a *App) prepareExport(r *http.Request, dataset string, frozen time.Time) (preparedExport, error) {
	var request exportRequest
	if err := decodeJSONRequest(r, &request); err != nil {
		return preparedExport{}, err
	}
	if request.Format != "csv" && request.Format != "json" {
		return preparedExport{}, errors.New("format must be csv or json")
	}
	if len(request.Filters) == 0 || bytes.Equal(bytes.TrimSpace(request.Filters), []byte("null")) {
		return preparedExport{}, errors.New("filters must be an object")
	}
	prepared := preparedExport{dataset: dataset, format: request.Format, exportedAt: frozen}
	var err error
	switch dataset {
	case "network_connections":
		err = a.prepareNetworkConnectionExport(&prepared, request.Filters, frozen)
	case "network_dimensions":
		err = a.prepareNetworkDimensionExport(&prepared, request.Filters, frozen)
	case "audit_events":
		err = a.prepareAuditExport(&prepared, request.Filters, frozen)
	default:
		err = errors.New("unknown export dataset")
	}
	return prepared, err
}

func (a *App) prepareNetworkConnectionExport(
	prepared *preparedExport,
	raw json.RawMessage,
	frozen time.Time,
) error {
	var filters networkExportFilters
	if err := decodeStrictExportFilters(raw, &filters); err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	from, err := rfc3339Millis(filters.DateFrom)
	if err != nil {
		return fmt.Errorf("date_from: %w", err)
	}
	to, err := rfc3339Millis(filters.DateTo)
	if err != nil {
		return fmt.Errorf("date_to: %w", err)
	}
	if to == 0 {
		to = frozen.UnixMilli()
	}
	states, err := exportStates(filters.State)
	if err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	query, err := store.NormalizeNetworkQuery(store.NetworkQuery{
		Page: 1, PageSize: 100, Scope: strings.TrimSpace(filters.Scope),
		Query: filters.Query, ClientID: strings.TrimSpace(filters.ClientID),
		Protocol: strings.TrimSpace(filters.Protocol), States: states,
		DateFrom: from, DateTo: to, Sort: strings.TrimSpace(filters.Sort),
	})
	if err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	if err := validateNetworkScope(query); err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	prepared.filters = networkExportFilterMap(query)
	matchedRows := int64(-1)
	prepared.matchedRows = &matchedRows
	prepared.auditDetails = exportAuditDetails(prepared.dataset, prepared.format, prepared.filters)
	prepared.iterate = func(ctx context.Context, yield func(any) error) (int, error) {
		items := make(map[string]store.NetworkConnection)
		if query.Scope != "active" {
			dbQuery := query
			if query.Scope == "history" && len(dbQuery.States) == 0 {
				dbQuery.States = []string{"closed", "failed", "interrupted"}
			}
			total, exportErr := a.Events.ExportNetworkConnections(
				ctx, dbQuery, exportarchive.MaxRows,
				func(item store.NetworkConnection) error {
					items[item.ActivityID] = item
					return nil
				},
			)
			*prepared.matchedRows = total
			if exportErr != nil {
				return 0, fmt.Errorf("export network connections: %w", exportErr)
			}
		}
		if query.Scope != "history" {
			for _, item := range a.Activity.Snapshot() {
				if matchesNetworkQuery(item, query) {
					items[item.ActivityID] = item
				}
			}
		}
		if len(items) > exportarchive.MaxRows {
			total := int64(len(items))
			*prepared.matchedRows = total
			return 0, exportarchive.ErrTooManyRows
		}
		values := make([]store.NetworkConnection, 0, len(items))
		for _, item := range items {
			values = append(values, item)
		}
		sortNetworkConnections(values, query.Sort)
		total := int64(len(values))
		*prepared.matchedRows = total
		for _, item := range values {
			if err := yield(item); err != nil {
				return 0, err
			}
		}
		return len(values), nil
	}
	return nil
}

func (a *App) prepareNetworkDimensionExport(
	prepared *preparedExport,
	raw json.RawMessage,
	frozen time.Time,
) error {
	var filters dimensionExportFilters
	if err := decodeStrictExportFilters(raw, &filters); err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	values := url.Values{}
	values.Set("range", filters.Range)
	values.Set("client_id", filters.ClientID)
	values.Set("proxy_id", filters.ProxyID)
	values.Set("protocol", filters.Protocol)
	if filters.From != 0 {
		values.Set("from", strconv.FormatInt(filters.From, 10))
	}
	if filters.To != 0 {
		values.Set("to", strconv.FormatInt(filters.To, 10))
	}
	fake := &http.Request{URL: &url.URL{RawQuery: values.Encode()}}
	query, metadata, err := a.analyticsQuery(fake)
	if err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	if metadata.Range.Requested != "custom" {
		duration := query.To - query.From
		query.To = frozen.UnixMilli()
		query.From = query.To - duration
		metadata.Range.EffectiveFrom = query.From
		metadata.Range.EffectiveTo = query.To
	}
	query.Search = filters.Query
	query.Sort = filters.Sort
	query.Page = 1
	query.PageSize = 100
	query, err = store.NormalizeAnalyticsQuery(query)
	if err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	prepared.filters = dimensionExportFilterMap(query, metadata.Range.Requested)
	matchedRows := int64(-1)
	prepared.matchedRows = &matchedRows
	prepared.auditDetails = exportAuditDetails(prepared.dataset, prepared.format, prepared.filters)
	prepared.iterate = func(ctx context.Context, yield func(any) error) (int, error) {
		total, exportErr := a.Events.ExportNetworkDimensions(
			ctx, query, exportarchive.MaxRows,
			func(item store.AnalyticsDimension) error {
				return yield(dimensionExportRow{
					AnalyticsDimension: item, RangeFrom: query.From, RangeTo: query.To,
					ClientScope: query.ClientID, ProxyScope: query.ProxyID,
					ProtocolScope: query.Protocol,
				})
			},
		)
		*prepared.matchedRows = total
		return int(total), exportErr
	}
	return nil
}

func (a *App) prepareAuditExport(
	prepared *preparedExport,
	raw json.RawMessage,
	frozen time.Time,
) error {
	var filters auditExportFilters
	if err := decodeStrictExportFilters(raw, &filters); err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	from, err := rfc3339Millis(filters.DateFrom)
	if err != nil {
		return fmt.Errorf("date_from: %w", err)
	}
	to, err := rfc3339Millis(filters.DateTo)
	if err != nil {
		return fmt.Errorf("date_to: %w", err)
	}
	if to == 0 {
		to = frozen.UnixMilli()
	}
	query, err := store.NormalizeAuditQuery(store.AuditQuery{
		Page: 1, PageSize: 100, Query: filters.Query,
		Category:  strings.TrimSpace(filters.Category),
		EventType: strings.TrimSpace(filters.EventType), Outcome: strings.TrimSpace(filters.Outcome),
		DateFrom: from, DateTo: to,
	})
	if err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	prepared.filters = auditExportFilterMap(query)
	matchedRows := int64(-1)
	prepared.matchedRows = &matchedRows
	prepared.auditDetails = exportAuditDetails(prepared.dataset, prepared.format, prepared.filters)
	prepared.iterate = func(ctx context.Context, yield func(any) error) (int, error) {
		total, exportErr := a.Events.ExportAuditEvents(
			ctx, query, exportarchive.MaxRows,
			func(item store.AuditEvent) error { return yield(item) },
		)
		*prepared.matchedRows = total
		return int(total), exportErr
	}
	return nil
}

func (a *App) handleExportBuildError(
	parent context.Context,
	w http.ResponseWriter,
	eventID string,
	prepared preparedExport,
	err error,
) {
	reasonCode := "export_failed"
	status := http.StatusServiceUnavailable
	payload := map[string]any{"error": "export_failed"}
	switch {
	case errors.Is(err, exportarchive.ErrTooManyRows), errors.Is(err, store.ErrExportTooManyRows):
		reasonCode, status = "row_limit_exceeded", http.StatusRequestEntityTooLarge
		payload = map[string]any{"error": "export_too_large", "max_rows": exportarchive.MaxRows}
		if prepared.matchedRows != nil && *prepared.matchedRows >= 0 {
			payload["matched_rows"] = *prepared.matchedRows
		}
	case errors.Is(err, exportarchive.ErrUncompressedTooLarge):
		a.exportManager().metrics.UncompressedTooLarge.Add(1)
		reasonCode, status = "uncompressed_limit_exceeded", http.StatusRequestEntityTooLarge
		payload = map[string]any{
			"error":                  "export_too_large",
			"max_uncompressed_bytes": exportarchive.MaxUncompressedBytes,
		}
	case errors.Is(err, exportarchive.ErrArchiveTooLarge):
		a.exportManager().metrics.ArchiveTooLarge.Add(1)
		reasonCode, status = "archive_limit_exceeded", http.StatusRequestEntityTooLarge
		payload = map[string]any{
			"error": "export_too_large", "max_archive_bytes": exportarchive.MaxArchiveBytes,
		}
	case errors.Is(err, context.DeadlineExceeded):
		reasonCode = "generation_timeout"
	case errors.Is(err, context.Canceled):
		a.exportManager().metrics.Canceled.Add(1)
		reasonCode = "request_canceled"
	}
	details := cloneExportDetails(prepared.auditDetails)
	if prepared.matchedRows != nil && *prepared.matchedRows >= 0 {
		details["row_count"] = *prepared.matchedRows
	}
	a.finalizeExportAudit(parent, eventID, "failure", reasonCode, details)
	writeJSON(w, status, payload)
}

func (a *App) finalizeExportAudit(
	parent context.Context,
	eventID string,
	outcome string,
	reasonCode string,
	details map[string]any,
) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancel()
	reason := ""
	if reasonCode != "" {
		reason = "export did not complete"
	}
	if err := a.Events.UpdateAuditOutcome(
		ctx, eventID, outcome, reasonCode, reason, details,
	); err != nil {
		a.storeError.Store(true)
	}
}

func (a *App) exportManager() *exportManager {
	a.exportOnce.Do(func() { a.exports = newExportManager() })
	return a.exports
}

func decodeStrictExportFilters(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("export context: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("invalid filters object")
	}
	return nil
}

func exportStates(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	result := make([]string, 0)
	for _, state := range strings.Split(value, ",") {
		state = strings.TrimSpace(state)
		if state == "" {
			return nil, errors.New("state contains an empty value")
		}
		result = append(result, state)
	}
	return result, nil
}

func networkExportFilterMap(query store.NetworkQuery) map[string]any {
	return map[string]any{
		"scope": query.Scope, "query": query.Query, "client_id": query.ClientID,
		"protocol": query.Protocol, "state": strings.Join(query.States, ","),
		"date_from": exportFilterTime(query.DateFrom), "date_to": exportFilterTime(query.DateTo),
		"sort": query.Sort,
	}
}

func dimensionExportFilterMap(query store.AnalyticsQuery, rangeName string) map[string]any {
	return map[string]any{
		"range": rangeName, "from": query.From, "to": query.To,
		"client_id": query.ClientID, "proxy_id": query.ProxyID, "protocol": query.Protocol,
		"query": query.Search, "sort": query.Sort,
	}
}

func auditExportFilterMap(query store.AuditQuery) map[string]any {
	return map[string]any{
		"query": query.Query, "category": query.Category, "event_type": query.EventType,
		"outcome": query.Outcome, "date_from": exportFilterTime(query.DateFrom),
		"date_to": exportFilterTime(query.DateTo),
	}
}

func exportFilterTime(value int64) string {
	if value == 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}

func exportFilterHash(filters map[string]any) (string, error) {
	encoded, err := json.Marshal(filters)
	if err != nil {
		return "", fmt.Errorf("encode normalized export filters: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func exportAuditDetails(dataset, format string, filters map[string]any) map[string]any {
	details := map[string]any{"dataset": dataset, "format": format}
	for _, key := range []string{"client_id", "proxy_id", "protocol"} {
		if value, ok := filters[key]; ok && value != "" {
			details[key] = value
		}
	}
	from, fromOK := filters["from"]
	to, toOK := filters["to"]
	if fromOK && toOK {
		details["range"] = map[string]any{"from": from, "to": to}
	} else {
		details["range"] = map[string]any{
			"from": filters["date_from"], "to": filters["date_to"],
		}
	}
	return details
}

func cloneExportDetails(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+4)
	for key, value := range source {
		result[key] = value
	}
	return result
}
