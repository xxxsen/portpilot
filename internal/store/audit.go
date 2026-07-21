package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultPageSize = 25
	maxSearchLength = 128
	maxDetailBytes  = 64 * 1024
)

type AuditEvent struct {
	ID            string         `json:"id"`
	OccurredAt    int64          `json:"occurred_at"`
	Category      string         `json:"category"`
	EventType     string         `json:"event_type"`
	Outcome       string         `json:"outcome"`
	ActorType     string         `json:"actor_type"`
	ActorID       string         `json:"actor_id"`
	SourceAddr    string         `json:"source_addr"`
	ClientID      string         `json:"client_id"`
	ProxyID       string         `json:"proxy_id"`
	ConnectionID  string         `json:"connection_id"`
	Summary       string         `json:"summary"`
	ReasonCode    string         `json:"reason_code"`
	Reason        string         `json:"reason"`
	Details       map[string]any `json:"details,omitempty"`
	ConfigChanges []ConfigChange `json:"config_changes,omitempty"`
}

type ConfigChange struct {
	ID         string         `json:"id"`
	EventID    string         `json:"event_id"`
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	Operation  string         `json:"operation"`
	Before     map[string]any `json:"before,omitempty"`
	After      map[string]any `json:"after,omitempty"`
}

type AuditQuery struct {
	Page      int
	PageSize  int
	Query     string
	Category  string
	EventType string
	Outcome   string
	DateFrom  int64
	DateTo    int64
}

type AuditPage struct {
	Items      []AuditEvent `json:"items"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
	Total      int64        `json:"total"`
	TotalPages int          `json:"total_pages"`
}

var (
	runtimeLogEventTypes = []string{
		"external_access_denied", "udp_drop", "proxy_protocol_write_failed", "security",
	}
	auditCategories = map[string]struct{}{
		"auth": {}, "config": {}, "client": {}, "security": {}, "system": {},
	}
	auditOutcomes = map[string]struct{}{
		"success": {}, "failure": {}, "denied": {}, "pending": {}, "unknown": {},
	}
	auditActorTypes = map[string]struct{}{
		"admin": {}, "client": {}, "system": {}, "external": {},
	}
	auditEventTypes = map[string]struct{}{
		"admin_login": {}, "admin_logout": {}, "config_update": {},
		"client_created": {}, "client_updated": {}, "client_token_rotated": {},
		"admin_client_disconnect": {}, "client_session": {},
		"client_agent_invalid": {}, "database_rebuilt": {},
		"system_start": {}, "system_stop": {},
		"config_metadata_migrated": {},
	}
)

func (s *Store) WriteAudit(ctx context.Context, event AuditEvent) (AuditEvent, error) {
	if s == nil || s.db == nil {
		return AuditEvent{}, errors.New("audit store unavailable")
	}
	normalized, detailsJSON, err := normalizeAuditEvent(event)
	if err != nil {
		return AuditEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.degraded.Store(true)
		return AuditEvent{}, fmt.Errorf("begin audit event: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (
		id, occurred_at, category, event_type, outcome, actor_type,
		actor_id, source_addr, client_id, proxy_id, connection_id,
		summary, reason_code, reason, details_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, normalized.OccurredAt, normalized.Category, normalized.EventType,
		normalized.Outcome, normalized.ActorType, normalized.ActorID, normalized.SourceAddr,
		normalized.ClientID, normalized.ProxyID, normalized.ConnectionID,
		normalized.Summary, normalized.ReasonCode, normalized.Reason, detailsJSON,
	); err != nil {
		s.degraded.Store(true)
		return AuditEvent{}, fmt.Errorf("insert audit event: %w", err)
	}
	for index := range normalized.ConfigChanges {
		change := &normalized.ConfigChanges[index]
		change.EventID = normalized.ID
		if change.ID == "" {
			change.ID, err = randomID()
			if err != nil {
				return AuditEvent{}, fmt.Errorf("create config change id: %w", err)
			}
		}
		beforeJSON, err := marshalOptionalObject(change.Before)
		if err != nil {
			return AuditEvent{}, fmt.Errorf("encode config change before: %w", err)
		}
		afterJSON, err := marshalOptionalObject(change.After)
		if err != nil {
			return AuditEvent{}, fmt.Errorf("encode config change after: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_config_changes (
			id, event_id, entity_type, entity_id, operation, before_json, after_json
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			change.ID, change.EventID, change.EntityType, change.EntityID,
			change.Operation, beforeJSON, afterJSON,
		); err != nil {
			s.degraded.Store(true)
			return AuditEvent{}, fmt.Errorf("insert config audit change: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		s.degraded.Store(true)
		return AuditEvent{}, fmt.Errorf("commit audit event: %w", err)
	}
	committed = true
	s.degraded.Store(false)
	return normalized, nil
}

func normalizeAuditEvent(event AuditEvent) (AuditEvent, string, error) {
	if event.ID == "" {
		id, err := randomID()
		if err != nil {
			return AuditEvent{}, "", fmt.Errorf("create audit event id: %w", err)
		}
		event.ID = id
	}
	if event.OccurredAt == 0 {
		event.OccurredAt = time.Now().UTC().UnixMilli()
	}
	if _, ok := auditCategories[event.Category]; !ok {
		return AuditEvent{}, "", fmt.Errorf("invalid audit category %q", event.Category)
	}
	if _, ok := auditEventTypes[event.EventType]; !ok {
		return AuditEvent{}, "", fmt.Errorf("invalid audit event_type %q", event.EventType)
	}
	if _, ok := auditOutcomes[event.Outcome]; !ok {
		return AuditEvent{}, "", fmt.Errorf("invalid audit outcome %q", event.Outcome)
	}
	if _, ok := auditActorTypes[event.ActorType]; !ok {
		return AuditEvent{}, "", fmt.Errorf("invalid audit actor_type %q", event.ActorType)
	}
	if len(event.Summary) > 500 || len(event.ReasonCode) > 100 || len(event.Reason) > 2000 {
		return AuditEvent{}, "", errors.New("audit text field exceeds its size limit")
	}
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	details, err := json.Marshal(event.Details)
	if err != nil {
		return AuditEvent{}, "", fmt.Errorf("encode audit details: %w", err)
	}
	if len(details) > maxDetailBytes {
		return AuditEvent{}, "", fmt.Errorf("audit details exceed %d bytes", maxDetailBytes)
	}
	for _, change := range event.ConfigChanges {
		if change.EntityType != "client" && change.EntityType != "proxy" {
			return AuditEvent{}, "", fmt.Errorf("invalid config change entity_type %q", change.EntityType)
		}
		if change.Operation != "create" && change.Operation != "update" && change.Operation != "delete" {
			return AuditEvent{}, "", fmt.Errorf("invalid config change operation %q", change.Operation)
		}
		if change.EntityID == "" {
			return AuditEvent{}, "", errors.New("config change entity_id is required")
		}
	}
	return event, string(details), nil
}

func marshalOptionalObject(value map[string]any) (sql.NullString, error) {
	if value == nil {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode optional audit object: %w", err)
	}
	if len(data) > maxDetailBytes {
		return sql.NullString{}, fmt.Errorf("object exceeds %d bytes", maxDetailBytes)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

func (s *Store) UpdateAuditOutcome(
	ctx context.Context,
	eventID, outcome, reasonCode, reason string,
	details map[string]any,
) error {
	if _, ok := auditOutcomes[outcome]; !ok {
		return fmt.Errorf("invalid audit outcome %q", outcome)
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit outcome details: %w", err)
	}
	if len(detailsJSON) > maxDetailBytes {
		return fmt.Errorf("audit details exceed %d bytes", maxDetailBytes)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE audit_events
		SET outcome=?, reason_code=?, reason=?, details_json=?
		WHERE id=?`, outcome, reasonCode, reason, string(detailsJSON), eventID)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("update audit outcome: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read audit update result: %w", err)
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	s.degraded.Store(false)
	return nil
}

func (s *Store) ListAudit(ctx context.Context, query AuditQuery) (AuditPage, error) {
	query, err := normalizeAuditQuery(query)
	if err != nil {
		return AuditPage{}, err
	}
	where, arguments := auditWhere(query)
	var total int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM audit_events`+where, arguments...,
	).Scan(&total); err != nil {
		return AuditPage{}, fmt.Errorf("count audit events: %w", err)
	}
	arguments = append(arguments, query.PageSize, (query.Page-1)*query.PageSize)
	//nolint:gosec // auditWhere only emits fixed clauses with bound values.
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, occurred_at, category, event_type, outcome, actor_type, actor_id,
		source_addr, client_id, proxy_id, connection_id, summary, reason_code, reason
		FROM audit_events`+where+`
		ORDER BY occurred_at DESC, id DESC LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return AuditPage{}, fmt.Errorf("query audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]AuditEvent, 0, query.PageSize)
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(
			&event.ID, &event.OccurredAt, &event.Category, &event.EventType,
			&event.Outcome, &event.ActorType, &event.ActorID, &event.SourceAddr,
			&event.ClientID, &event.ProxyID, &event.ConnectionID, &event.Summary,
			&event.ReasonCode, &event.Reason,
		); err != nil {
			return AuditPage{}, fmt.Errorf("scan audit event: %w", err)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, fmt.Errorf("iterate audit events: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}
	return AuditPage{
		Items: items, Page: query.Page, PageSize: query.PageSize,
		Total: total, TotalPages: totalPages,
	}, nil
}

func normalizeAuditQuery(query AuditQuery) (AuditQuery, error) {
	if query.Page <= 0 {
		query.Page = 1
	}
	switch query.PageSize {
	case 0:
		query.PageSize = defaultPageSize
	case 25, 50, 100:
	default:
		return AuditQuery{}, errors.New("page_size must be 25, 50, or 100")
	}
	query.Query = strings.TrimSpace(query.Query)
	if len(query.Query) > maxSearchLength {
		return AuditQuery{}, fmt.Errorf("query exceeds %d characters", maxSearchLength)
	}
	if query.Category != "" {
		if _, ok := auditCategories[query.Category]; !ok {
			return AuditQuery{}, fmt.Errorf("invalid category %q", query.Category)
		}
	}
	if query.Outcome != "" {
		if _, ok := auditOutcomes[query.Outcome]; !ok {
			return AuditQuery{}, fmt.Errorf("invalid outcome %q", query.Outcome)
		}
	}
	if query.EventType != "" {
		if _, ok := auditEventTypes[query.EventType]; !ok {
			return AuditQuery{}, fmt.Errorf("invalid event_type %q", query.EventType)
		}
	}
	if query.DateFrom > 0 && query.DateTo > 0 && query.DateFrom >= query.DateTo {
		return AuditQuery{}, errors.New("date_from must be before date_to")
	}
	return query, nil
}

func auditWhere(query AuditQuery) (string, []any) {
	clauses := []string{"event_type NOT IN (?, ?, ?, ?)"}
	arguments := make([]any, 0, len(runtimeLogEventTypes)+8)
	for _, eventType := range runtimeLogEventTypes {
		arguments = append(arguments, eventType)
	}
	if query.Query != "" {
		value := "%" + escapeLike(query.Query) + "%"
		clauses = append(clauses, `(summary LIKE ? ESCAPE '\' OR actor_id LIKE ? ESCAPE '\'
			OR client_id LIKE ? ESCAPE '\' OR proxy_id LIKE ? ESCAPE '\'
			OR connection_id LIKE ? ESCAPE '\' OR reason_code LIKE ? ESCAPE '\')`)
		for range 6 {
			arguments = append(arguments, value)
		}
	}
	if query.Category != "" {
		clauses = append(clauses, "category=?")
		arguments = append(arguments, query.Category)
	}
	if query.EventType != "" {
		clauses = append(clauses, "event_type=?")
		arguments = append(arguments, query.EventType)
	}
	if query.Outcome != "" {
		clauses = append(clauses, "outcome=?")
		arguments = append(arguments, query.Outcome)
	}
	if query.DateFrom > 0 {
		clauses = append(clauses, "occurred_at>=?")
		arguments = append(arguments, query.DateFrom)
	}
	if query.DateTo > 0 {
		clauses = append(clauses, "occurred_at<?")
		arguments = append(arguments, query.DateTo)
	}
	return " WHERE " + strings.Join(clauses, " AND "), arguments
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func (s *Store) AuditDetail(ctx context.Context, id string) (AuditEvent, error) {
	if len(id) != 32 {
		return AuditEvent{}, sql.ErrNoRows
	}
	var event AuditEvent
	var detailsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT
		id, occurred_at, category, event_type, outcome, actor_type, actor_id,
		source_addr, client_id, proxy_id, connection_id, summary, reason_code,
		reason, details_json
		FROM audit_events WHERE id=? AND event_type NOT IN (?, ?, ?, ?)`,
		id,
		runtimeLogEventTypes[0], runtimeLogEventTypes[1],
		runtimeLogEventTypes[2], runtimeLogEventTypes[3],
	).Scan(
		&event.ID, &event.OccurredAt, &event.Category, &event.EventType,
		&event.Outcome, &event.ActorType, &event.ActorID, &event.SourceAddr,
		&event.ClientID, &event.ProxyID, &event.ConnectionID, &event.Summary,
		&event.ReasonCode, &event.Reason, &detailsJSON,
	)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("query audit event detail: %w", err)
	}
	if err := json.Unmarshal([]byte(detailsJSON), &event.Details); err != nil {
		return AuditEvent{}, fmt.Errorf("decode audit details: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, event_id, entity_type, entity_id, operation, before_json, after_json
		FROM audit_config_changes WHERE event_id=? ORDER BY id`, id)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("query config audit changes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var change ConfigChange
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(
			&change.ID, &change.EventID, &change.EntityType, &change.EntityID,
			&change.Operation, &beforeJSON, &afterJSON,
		); err != nil {
			return AuditEvent{}, fmt.Errorf("scan config audit change: %w", err)
		}
		if beforeJSON.Valid {
			if err := json.Unmarshal([]byte(beforeJSON.String), &change.Before); err != nil {
				return AuditEvent{}, fmt.Errorf("decode config audit before: %w", err)
			}
		}
		if afterJSON.Valid {
			if err := json.Unmarshal([]byte(afterJSON.String), &change.After); err != nil {
				return AuditEvent{}, fmt.Errorf("decode config audit after: %w", err)
			}
		}
		event.ConfigChanges = append(event.ConfigChanges, change)
	}
	if err := rows.Err(); err != nil {
		return AuditEvent{}, fmt.Errorf("iterate config audit changes: %w", err)
	}
	return event, nil
}

func (s *Store) ResolvePendingAudits(ctx context.Context, currentRevision uint64) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, details_json
		FROM audit_events WHERE outcome='pending' ORDER BY occurred_at`)
	if err != nil {
		return 0, fmt.Errorf("query pending audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type pendingEvent struct {
		id      string
		details map[string]any
	}
	var pending []pendingEvent
	for rows.Next() {
		var id, detailsJSON string
		if err := rows.Scan(&id, &detailsJSON); err != nil {
			return 0, fmt.Errorf("scan pending audit event: %w", err)
		}
		details := map[string]any{}
		if err := json.Unmarshal([]byte(detailsJSON), &details); err != nil {
			return 0, fmt.Errorf("decode pending audit details: %w", err)
		}
		pending = append(pending, pendingEvent{id: id, details: details})
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate pending audit events: %w", err)
	}
	for _, event := range pending {
		target, ok := numericUint64(event.details["target_revision"])
		outcome := "unknown"
		reasonCode := "server_interrupted"
		reason := "server stopped before the configuration audit was finalized"
		if ok && target <= currentRevision {
			outcome = "success"
			reasonCode = "recovered_after_restart"
			reason = "configuration revision was committed before server restart"
		} else if ok && target > currentRevision {
			outcome = "failure"
			reasonCode = "not_committed_before_restart"
			reason = "configuration revision was not committed before server restart"
		}
		event.details["recovered_at_revision"] = currentRevision
		event.details["recovered_after_restart"] = true
		if err := s.UpdateAuditOutcome(
			ctx, event.id, outcome, reasonCode, reason, event.details,
		); err != nil {
			return 0, fmt.Errorf("resolve pending audit event %s: %w", event.id, err)
		}
	}
	return len(pending), nil
}

func numericUint64(value any) (uint64, bool) {
	switch number := value.(type) {
	case float64:
		if number < 0 || number != float64(uint64(number)) {
			return 0, false
		}
		return uint64(number), true
	case uint64:
		return number, true
	case int:
		if number < 0 {
			return 0, false
		}
		return uint64(number), true
	default:
		return 0, false
	}
}
