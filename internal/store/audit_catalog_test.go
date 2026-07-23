package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type auditCatalogEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func TestAuditEventCatalogMatchesAllowedEventTypes(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve audit catalog test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		t.Fatalf("open repository root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close repository root: %v", closeErr)
		}
	})

	data, err := root.ReadFile("web/lib/audit-event-catalog.json")
	if err != nil {
		t.Fatalf("read audit event catalog: %v", err)
	}
	var entries []auditCatalogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode audit event catalog: %v", err)
	}

	catalog := make(map[string]string, len(entries))
	for _, entry := range entries {
		if _, exists := catalog[entry.Value]; exists {
			t.Errorf("audit event catalog contains duplicate event %q", entry.Value)
		}
		if strings.TrimSpace(entry.Label) == "" || entry.Label == entry.Value {
			t.Errorf("audit event %q does not have a user-facing label", entry.Value)
		}
		catalog[entry.Value] = entry.Label
	}

	for eventType := range auditEventTypes {
		if _, exists := catalog[eventType]; !exists {
			t.Errorf("audit event %q is missing from the UI catalog", eventType)
		}
	}
	for eventType := range catalog {
		if _, exists := auditEventTypes[eventType]; !exists {
			t.Errorf("UI audit event %q is not allowed by the backend", eventType)
		}
	}
}
