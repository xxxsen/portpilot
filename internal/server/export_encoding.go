package server

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

type exportRowIterator func(func(any) error) (int, error)

type dimensionExportRow struct {
	store.AnalyticsDimension
	RangeFrom     int64  `json:"range_from"`
	RangeTo       int64  `json:"range_to"`
	ClientScope   string `json:"scope_client_id"`
	ProxyScope    string `json:"scope_proxy_id"`
	ProtocolScope string `json:"scope_protocol"`
}

func encodeExportRows(
	output io.Writer,
	format string,
	dataset string,
	exportedAt time.Time,
	filters map[string]any,
	iterate exportRowIterator,
) (int, error) {
	if format == "json" {
		return encodeJSONExport(output, exportedAt, filters, iterate)
	}
	return encodeCSVExport(output, dataset, iterate)
}

func encodeJSONExport(
	output io.Writer,
	exportedAt time.Time,
	filters map[string]any,
	iterate exportRowIterator,
) (int, error) {
	header, err := json.Marshal(map[string]any{
		"exported_at": exportedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		"filters":     filters,
	})
	if err != nil {
		return 0, fmt.Errorf("encode export JSON header: %w", err)
	}
	header = header[:len(header)-1]
	if _, err := output.Write(append(header, []byte(`,"items":[`)...)); err != nil {
		return 0, err
	}
	first := true
	count, err := iterate(func(value any) error {
		value = sanitizeExportRow(value)
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode export JSON row: %w", err)
		}
		if !first {
			if _, err := output.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		_, err = output.Write(encoded)
		return err
	})
	if err != nil {
		return count, err
	}
	if _, err := output.Write([]byte("]}")); err != nil {
		return count, err
	}
	return count, nil
}

func encodeCSVExport(
	output io.Writer,
	dataset string,
	iterate exportRowIterator,
) (int, error) {
	writer := csv.NewWriter(output)
	writer.UseCRLF = true
	header := exportCSVHeader(dataset)
	if header == nil {
		return 0, fmt.Errorf("unsupported CSV dataset %q", dataset)
	}
	if err := writer.Write(header); err != nil {
		return 0, err
	}
	count, err := iterate(func(value any) error {
		row, err := exportCSVRow(dataset, value)
		if err != nil {
			return err
		}
		for _, index := range exportCSVTextColumns(dataset) {
			if index < len(row) {
				row[index] = csvSafe(row[index])
			}
		}
		return writer.Write(row)
	})
	writer.Flush()
	if err == nil {
		err = writer.Error()
	}
	if err != nil {
		return count, fmt.Errorf("flush export CSV: %w", err)
	}
	return count, nil
}

func exportCSVHeader(dataset string) []string {
	switch dataset {
	case "network_connections":
		return []string{
			"activity_id", "protocol", "state", "client_id", "client_remark",
			"proxy_id", "proxy_remark", "external_addr", "external_ip",
			"server_bind", "target_addr", "transport", "session_epoch",
			"accepted_at", "connected_at", "last_activity_at", "closed_at",
			"up_bytes", "down_bytes", "up_packets", "down_packets",
			"up_bytes_per_second", "down_bytes_per_second", "rate_sampled_at",
			"rate_window_millis", "up_limit_bytes_per_second",
			"down_limit_bytes_per_second", "up_throttled_millis",
			"down_throttled_millis", "up_rate_limited_packets",
			"down_rate_limited_packets", "up_rate_limited_bytes",
			"down_rate_limited_bytes", "proxy_protocol_enabled",
			"proxy_protocol_version", "proxy_protocol_header_sent",
			"close_reason_code", "close_reason",
		}
	case "network_dimensions":
		return []string{
			"client_id", "client_remark", "proxy_id", "proxy_remark", "protocol",
			"connections", "failures", "unique_ips", "countries",
			"up_bytes", "down_bytes", "top_country_code",
			"first_seen_at", "last_seen_at", "range_from", "range_to",
			"scope_client_id", "scope_proxy_id", "scope_protocol",
		}
	case "audit_events":
		return []string{
			"id", "occurred_at", "category", "event_type", "outcome",
			"actor_type", "actor_id", "source_addr", "client_id", "proxy_id",
			"connection_id", "summary", "reason_code", "reason",
			"details", "config_changes",
		}
	default:
		return nil
	}
}

func exportCSVTextColumns(dataset string) []int {
	switch dataset {
	case "network_connections":
		return []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 36, 37}
	case "network_dimensions":
		return []int{0, 1, 2, 3, 4, 11, 16, 17, 18}
	case "audit_events":
		return []int{0, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	default:
		return nil
	}
}

func exportCSVRow(dataset string, value any) ([]string, error) {
	switch dataset {
	case "network_connections":
		item, ok := value.(store.NetworkConnection)
		if !ok {
			return nil, errors.New("invalid network connection export row")
		}
		return []string{
			item.ActivityID, item.Protocol, item.State, item.ClientID, item.ClientRemark,
			item.ProxyID, item.ProxyRemark, item.ExternalAddr, item.ExternalIP,
			item.ServerBind, item.TargetAddr, item.Transport, strconv.FormatUint(item.SessionEpoch, 10),
			exportTime(item.AcceptedAt), exportOptionalTime(item.ConnectedAt),
			exportTime(item.LastActivityAt), exportOptionalTime(item.ClosedAt),
			strconv.FormatInt(item.UpBytes, 10), strconv.FormatInt(item.DownBytes, 10),
			strconv.FormatInt(item.UpPackets, 10), strconv.FormatInt(item.DownPackets, 10),
			strconv.FormatInt(item.UpBytesPerSecond, 10), strconv.FormatInt(item.DownBytesPerSecond, 10),
			exportTimeOrEmpty(item.RateSampledAt), strconv.FormatInt(item.RateWindowMillis, 10),
			strconv.FormatInt(item.UpLimitBytesPerSecond, 10),
			strconv.FormatInt(item.DownLimitBytesPerSecond, 10),
			strconv.FormatInt(item.UpThrottledMillis, 10), strconv.FormatInt(item.DownThrottledMillis, 10),
			strconv.FormatInt(item.UpRateLimitedPackets, 10),
			strconv.FormatInt(item.DownRateLimitedPackets, 10),
			strconv.FormatInt(item.UpRateLimitedBytes, 10),
			strconv.FormatInt(item.DownRateLimitedBytes, 10),
			strconv.FormatBool(item.ProxyProtocolEnabled),
			strconv.Itoa(item.ProxyProtocolVersion),
			strconv.FormatBool(item.ProxyProtocolHeaderSent),
			item.CloseReasonCode, item.CloseReason,
		}, nil
	case "network_dimensions":
		item, ok := value.(dimensionExportRow)
		if !ok {
			return nil, errors.New("invalid network dimension export row")
		}
		return []string{
			item.ClientID, item.ClientRemark, item.ProxyID, item.ProxyRemark, item.Protocol,
			strconv.FormatInt(item.Connections, 10), strconv.FormatInt(item.Failures, 10),
			strconv.FormatInt(item.UniqueIPs, 10), strconv.FormatInt(item.Countries, 10),
			strconv.FormatInt(item.UpBytes, 10), strconv.FormatInt(item.DownBytes, 10),
			item.TopCountryCode, exportTime(item.FirstSeenAt), exportTime(item.LastSeenAt),
			exportTime(item.RangeFrom), exportTime(item.RangeTo),
			item.ClientScope, item.ProxyScope, item.ProtocolScope,
		}, nil
	case "audit_events":
		item, ok := value.(store.AuditEvent)
		if !ok {
			return nil, errors.New("invalid audit event export row")
		}
		details, err := compactJSON(sanitizeExportValue(item.Details))
		if err != nil {
			return nil, err
		}
		changes, err := compactJSON(sanitizeExportValue(item.ConfigChanges))
		if err != nil {
			return nil, err
		}
		return []string{
			item.ID, exportTime(item.OccurredAt), item.Category, item.EventType,
			item.Outcome, item.ActorType, item.ActorID, item.SourceAddr,
			item.ClientID, item.ProxyID, item.ConnectionID, item.Summary,
			item.ReasonCode, item.Reason, details, changes,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported export dataset %q", dataset)
	}
}

func sanitizeExportRow(value any) any {
	event, ok := value.(store.AuditEvent)
	if !ok {
		return value
	}
	return map[string]any{
		"id": event.ID, "occurred_at": exportTime(event.OccurredAt),
		"category": event.Category, "event_type": event.EventType, "outcome": event.Outcome,
		"actor_type": event.ActorType, "actor_id": event.ActorID,
		"source_addr": event.SourceAddr, "client_id": event.ClientID,
		"proxy_id": event.ProxyID, "connection_id": event.ConnectionID,
		"summary": event.Summary, "reason_code": event.ReasonCode, "reason": event.Reason,
		"details":        sanitizeExportValue(event.Details),
		"config_changes": sanitizeExportValue(event.ConfigChanges),
	}
}

func sanitizeExportValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if secretExportKey(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = sanitizeExportValue(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeExportValue(item)
		}
		return result
	case []store.ConfigChange:
		result := make([]map[string]any, 0, len(typed))
		for _, change := range typed {
			result = append(result, map[string]any{
				"id": change.ID, "event_id": change.EventID,
				"entity_type": change.EntityType, "entity_id": change.EntityID,
				"operation": change.Operation,
				"before":    sanitizeExportValue(change.Before),
				"after":     sanitizeExportValue(change.After),
			})
		}
		return result
	default:
		return value
	}
}

func secretExportKey(value string) bool {
	value = strings.ToLower(value)
	for _, fragment := range []string{
		"token", "password", "cookie", "csrf", "ticket", "signature", "secret",
	} {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

func csvSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}

func compactJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func exportTime(value int64) string {
	return time.UnixMilli(value).UTC().Format("2006-01-02T15:04:05.000Z")
}

func exportTimeOrEmpty(value int64) string {
	if value <= 0 {
		return ""
	}
	return exportTime(value)
}

func exportOptionalTime(value *int64) string {
	if value == nil {
		return ""
	}
	return exportTime(*value)
}
