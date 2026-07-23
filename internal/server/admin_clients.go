package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

type configMutation struct {
	eventType   string
	summary     string
	clientID    string
	changes     []store.ConfigChange
	afterCommit func() map[string]any
}

func (a *App) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		ClientID         string `json:"client_id"`
		Remark           string `json:"remark"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	token, err := auth.RandomToken(32)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "entropy_unavailable"})
		return
	}
	current := a.ConfigManager.Snapshot()
	for _, client := range current.ClientConfig {
		if client.ClientID == request.ClientID {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "client_exists", "revision": current.Revision,
			})
			return
		}
	}
	next := current
	next.ClientConfig = append(next.ClientConfig, config.ClientConfig{
		ClientID: request.ClientID, ClientToken: token, Remark: request.Remark,
		Enabled: true, ProxyList: []config.ProxyEnvelope{},
	})
	updated, ok := a.commitConfigMutation(w, r, request.ExpectedRevision, next, configMutation{
		eventType: "client_created", summary: "创建客户端", clientID: request.ClientID,
		changes: []store.ConfigChange{{
			EntityType: "client", EntityID: request.ClientID, Operation: "create",
			After: sanitizedClient(next.ClientConfig[len(next.ClientConfig)-1]),
		}},
	})
	if !ok {
		return
	}
	client := findClient(updated, request.ClientID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"revision": updated.Revision,
		"client": map[string]any{
			"client_id": client.ClientID, "remark": client.Remark, "enabled": client.Enabled,
			"created_at": client.CreatedAt, "updated_at": client.UpdatedAt,
		},
		"one_time_token": token,
	})
}

func (a *App) handleClientDetail(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("client_id")
	file := a.ConfigManager.Snapshot()
	client := findClient(file, clientID)
	if client.ClientID == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client_not_found"})
		return
	}
	status := SessionStatus{ClientID: clientID, Epoch: a.Sessions.Epoch(clientID)}
	online := false
	if current := a.Sessions.Current(clientID); current != nil {
		status = current.Status()
		online = true
	}
	snapshot, err := a.Events.ClientAgentSnapshot(r.Context(), clientID)
	if errors.Is(err, sql.ErrNoRows) {
		snapshot = store.ClientAgentSnapshot{}
	} else if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "event_store_unavailable",
		})
		return
	}
	agent, agentStatus, session := observedClient(status, online, snapshot)
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id": client.ClientID, "remark": client.Remark,
		"enabled": client.Enabled, "online": online,
		"created_at": client.CreatedAt, "updated_at": client.UpdatedAt,
		"agent": agent, "agent_status": agentStatus, "session": session,
		"session_epoch": status.Epoch, "mux_sessions": status.MuxSessions,
		"active_streams": status.ActiveStreams, "proxies": client.ProxyList,
	})
}

func (a *App) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		Remark           string `json:"remark"`
		Enabled          bool   `json:"enabled"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	clientID := r.PathValue("client_id")
	current := a.ConfigManager.Snapshot()
	next := current
	index := clientIndex(next, clientID)
	if index < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client_not_found"})
		return
	}
	before := next.ClientConfig[index]
	next.ClientConfig[index].Remark = request.Remark
	next.ClientConfig[index].Enabled = request.Enabled
	updated, ok := a.commitConfigMutation(w, r, request.ExpectedRevision, next, configMutation{
		eventType: "client_updated", summary: "更新客户端", clientID: clientID,
		changes: []store.ConfigChange{{
			EntityType: "client", EntityID: clientID, Operation: "update",
			Before: sanitizedClient(before), After: sanitizedClient(next.ClientConfig[index]),
		}},
	})
	if !ok {
		return
	}
	client := findClient(updated, clientID)
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": updated.Revision, "client": sanitizedClient(client),
	})
}

func (a *App) handleRotateClientToken(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision uint64 `json:"expected_revision"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	clientID := r.PathValue("client_id")
	token, err := auth.RandomToken(32)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "entropy_unavailable"})
		return
	}
	current := a.ConfigManager.Snapshot()
	next := current
	index := clientIndex(next, clientID)
	if index < 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client_not_found"})
		return
	}
	next.ClientConfig[index].ClientToken = token
	sessionClosed := false
	updated, ok := a.commitConfigMutation(w, r, request.ExpectedRevision, next, configMutation{
		eventType: "client_token_rotated", summary: "轮换客户端密钥", clientID: clientID,
		changes: []store.ConfigChange{{
			EntityType: "client", EntityID: clientID, Operation: "update",
			Before: map[string]any{"token_configured": true},
			After:  map[string]any{"token_configured": true},
		}},
		afterCommit: func() map[string]any {
			sessionClosed = a.Sessions.Kick(clientID, "token_rotated")
			return map[string]any{"session_closed": sessionClosed}
		},
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": updated.Revision, "one_time_token": token,
		"session_closed": sessionClosed,
	})
}

func (a *App) commitConfigMutation(
	w http.ResponseWriter,
	r *http.Request,
	expectedRevision uint64,
	next config.ClientFile,
	mutation configMutation,
) (config.ClientFile, bool) {
	current := a.ConfigManager.Snapshot()
	if current.Revision != expectedRevision {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "revision_conflict", "revision": current.Revision,
		})
		return config.ClientFile{}, false
	}
	session := adminFromContext(r)
	event, err := a.Events.WriteAudit(r.Context(), store.AuditEvent{
		Category: "config", EventType: mutation.eventType, Outcome: "pending",
		ActorType: "admin", ActorID: session.UserName, SourceAddr: a.requestSource(r),
		ClientID: mutation.clientID, Summary: mutation.summary,
		Details: map[string]any{
			"old_revision": current.Revision, "target_revision": current.Revision + 1,
			"change_count": len(mutation.changes),
		},
		ConfigChanges: mutation.changes,
	})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
		return config.ClientFile{}, false
	}
	updated, err := a.ConfigManager.Replace(expectedRevision, next, a.prepareClientConfig)
	if err != nil {
		if auditErr := a.finishConfigAudit(r.Context(), event.ID, "failure", current.Revision, err, nil); auditErr != nil {
			a.storeError.Store(true)
			a.Logger.Error(
				"finalize failed configuration audit",
				zap.String("event_id", event.ID),
				zap.Error(auditErr),
			)
		}
		if errors.Is(err, config.ErrRevisionConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "revision_conflict", "revision": a.ConfigManager.Snapshot().Revision,
			})
			return config.ClientFile{}, false
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid_config", "detail": err.Error(),
		})
		return config.ClientFile{}, false
	}
	a.Sessions.ReconcileClients(updated)
	a.Tunnels.Reconcile()
	a.UDP.Reconcile()
	var details map[string]any
	if mutation.afterCommit != nil {
		details = mutation.afterCommit()
	}
	if err := a.finishConfigAudit(r.Context(), event.ID, "success", updated.Revision, nil, details); err != nil {
		a.storeError.Store(true)
		a.Logger.Error(
			"finalize configuration audit failed",
			zap.String("event_id", event.ID),
			zap.Error(err),
		)
	}
	return updated, true
}

func (a *App) finishConfigAudit(
	ctx context.Context,
	eventID, outcome string,
	revision uint64,
	mutationErr error,
	extraDetails map[string]any,
) error {
	reasonCode := ""
	reason := ""
	if mutationErr != nil {
		reasonCode = "config_write_failed"
		reason = mutationErr.Error()
		if len(reason) > 1000 {
			reason = reason[:1000]
		}
	}
	details := map[string]any{"revision": revision}
	if event, err := a.Events.AuditDetail(ctx, eventID); err == nil {
		for key, value := range event.Details {
			details[key] = value
		}
		details["revision"] = revision
	}
	for key, value := range extraDetails {
		details[key] = value
	}
	if err := a.Events.UpdateAuditOutcome(ctx, eventID, outcome, reasonCode, reason, details); err != nil {
		return fmt.Errorf("update configuration audit outcome: %w", err)
	}
	return nil
}

func clientIndex(file config.ClientFile, clientID string) int {
	for index := range file.ClientConfig {
		if file.ClientConfig[index].ClientID == clientID {
			return index
		}
	}
	return -1
}

func findClient(file config.ClientFile, clientID string) config.ClientConfig {
	index := clientIndex(file, clientID)
	if index < 0 {
		return config.ClientConfig{}
	}
	return file.ClientConfig[index]
}

func sanitizedClient(client config.ClientConfig) map[string]any {
	return map[string]any{
		"client_id": client.ClientID, "remark": strings.TrimSpace(client.Remark),
		"enabled": client.Enabled, "created_at": client.CreatedAt, "updated_at": client.UpdatedAt,
		"token_configured": client.ClientToken != "",
	}
}

func sanitizedProxy(proxy config.ProxyEnvelope, clientID string) map[string]any {
	var data any
	if len(proxy.Data) > 0 {
		_ = json.Unmarshal(proxy.Data, &data)
	}
	return map[string]any{
		"client_id": clientID, "proxy_id": proxy.ProxyID, "remark": proxy.Remark,
		"kind": proxy.Kind, "enabled": proxy.Enabled, "data": data,
	}
}

func diffConfig(before, after config.ClientFile) []store.ConfigChange {
	changes := make([]store.ConfigChange, 0)
	beforeClients := make(map[string]config.ClientConfig, len(before.ClientConfig))
	afterClients := make(map[string]config.ClientConfig, len(after.ClientConfig))
	for _, client := range before.ClientConfig {
		beforeClients[client.ClientID] = client
	}
	for _, client := range after.ClientConfig {
		afterClients[client.ClientID] = client
	}
	for clientID, oldClient := range beforeClients {
		newClient, exists := afterClients[clientID]
		if !exists {
			changes = append(changes, store.ConfigChange{
				EntityType: "client", EntityID: clientID, Operation: "delete",
				Before: sanitizedClient(oldClient),
			})
			for _, proxy := range oldClient.ProxyList {
				changes = append(changes, store.ConfigChange{
					EntityType: "proxy", EntityID: proxy.ProxyID, Operation: "delete",
					Before: sanitizedProxy(proxy, clientID),
				})
			}
			continue
		}
		if !clientAuditEqual(oldClient, newClient) {
			changes = append(changes, store.ConfigChange{
				EntityType: "client", EntityID: clientID, Operation: "update",
				Before: sanitizedClient(oldClient), After: sanitizedClient(newClient),
			})
		}
		changes = append(changes, diffProxies(clientID, oldClient.ProxyList, newClient.ProxyList)...)
	}
	for clientID, client := range afterClients {
		if _, exists := beforeClients[clientID]; exists {
			continue
		}
		changes = append(changes, store.ConfigChange{
			EntityType: "client", EntityID: clientID, Operation: "create",
			After: sanitizedClient(client),
		})
		for _, proxy := range client.ProxyList {
			changes = append(changes, store.ConfigChange{
				EntityType: "proxy", EntityID: proxy.ProxyID, Operation: "create",
				After: sanitizedProxy(proxy, clientID),
			})
		}
	}
	return changes
}

func clientAuditEqual(left, right config.ClientConfig) bool {
	return left.ClientID == right.ClientID && left.Remark == right.Remark &&
		left.Enabled == right.Enabled && (left.ClientToken != "") == (right.ClientToken != "")
}

func diffProxies(clientID string, before, after []config.ProxyEnvelope) []store.ConfigChange {
	oldItems := make(map[string]config.ProxyEnvelope, len(before))
	newItems := make(map[string]config.ProxyEnvelope, len(after))
	for _, proxy := range before {
		oldItems[proxy.ProxyID] = proxy
	}
	for _, proxy := range after {
		newItems[proxy.ProxyID] = proxy
	}
	var changes []store.ConfigChange
	for proxyID, oldProxy := range oldItems {
		newProxy, exists := newItems[proxyID]
		operation := "update"
		if !exists {
			operation = "delete"
		}
		if !exists || !reflect.DeepEqual(oldProxy, newProxy) {
			change := store.ConfigChange{
				EntityType: "proxy", EntityID: proxyID, Operation: operation,
				Before: sanitizedProxy(oldProxy, clientID),
			}
			if exists {
				change.After = sanitizedProxy(newProxy, clientID)
			}
			changes = append(changes, change)
		}
	}
	for proxyID, newProxy := range newItems {
		if _, exists := oldItems[proxyID]; !exists {
			changes = append(changes, store.ConfigChange{
				EntityType: "proxy", EntityID: proxyID, Operation: "create",
				After: sanitizedProxy(newProxy, clientID),
			})
		}
	}
	return changes
}
