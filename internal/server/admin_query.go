package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

func auditQueryFromRequest(r *http.Request) (store.AuditQuery, error) {
	page, err := positiveInt(r.URL.Query().Get("page"), 1)
	if err != nil {
		return store.AuditQuery{}, err
	}
	pageSize, err := positiveInt(r.URL.Query().Get("page_size"), 25)
	if err != nil {
		return store.AuditQuery{}, err
	}
	from, err := rfc3339Millis(r.URL.Query().Get("date_from"))
	if err != nil {
		return store.AuditQuery{}, fmt.Errorf("date_from: %w", err)
	}
	to, err := rfc3339Millis(r.URL.Query().Get("date_to"))
	if err != nil {
		return store.AuditQuery{}, fmt.Errorf("date_to: %w", err)
	}
	return store.AuditQuery{
		Page: page, PageSize: pageSize, Query: r.URL.Query().Get("query"),
		Category: r.URL.Query().Get("category"), EventType: r.URL.Query().Get("event_type"),
		Outcome: r.URL.Query().Get("outcome"), DateFrom: from, DateTo: to,
	}, nil
}

func networkQueryFromRequest(r *http.Request) (store.NetworkQuery, error) {
	page, err := positiveInt(r.URL.Query().Get("page"), 1)
	if err != nil {
		return store.NetworkQuery{}, err
	}
	pageSize, err := positiveInt(r.URL.Query().Get("page_size"), 25)
	if err != nil {
		return store.NetworkQuery{}, err
	}
	from, err := rfc3339Millis(r.URL.Query().Get("date_from"))
	if err != nil {
		return store.NetworkQuery{}, fmt.Errorf("date_from: %w", err)
	}
	to, err := rfc3339Millis(r.URL.Query().Get("date_to"))
	if err != nil {
		return store.NetworkQuery{}, fmt.Errorf("date_to: %w", err)
	}
	states := []string(nil)
	if value := strings.TrimSpace(r.URL.Query().Get("state")); value != "" {
		for _, state := range strings.Split(value, ",") {
			state = strings.TrimSpace(state)
			if state == "" {
				return store.NetworkQuery{}, errors.New("state contains an empty value")
			}
			states = append(states, state)
		}
	}
	return store.NetworkQuery{
		Page: page, PageSize: pageSize, Query: r.URL.Query().Get("query"),
		ClientID: r.URL.Query().Get("client_id"), Protocol: r.URL.Query().Get("protocol"),
		Scope: r.URL.Query().Get("scope"), States: states,
		DateFrom: from, DateTo: to, Sort: r.URL.Query().Get("sort"),
	}, nil
}

func positiveInt(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("value must be a positive integer")
	}
	return parsed, nil
}

func rfc3339Millis(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, errors.New("value must be RFC3339")
	}
	return parsed.UTC().UnixMilli(), nil
}
