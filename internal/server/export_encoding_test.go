package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

func TestCSVExportProtectsTextWithoutChangingNumericValues(t *testing.T) {
	connection := store.NetworkConnection{
		ActivityID: "=HYPERLINK(\"https://example.invalid\")",
		Protocol:   "tcp", State: "closed", ClientID: "+client", ProxyID: "@proxy",
		AcceptedAt: 1, LastActivityAt: 1, UpBytes: -5,
	}
	var output bytes.Buffer
	count, err := encodeCSVExport(&output, "network_connections", func(yield func(any) error) (int, error) {
		return 1, yield(connection)
	})
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if bytes.Contains(output.Bytes(), []byte("\n")) && !bytes.Contains(output.Bytes(), []byte("\r\n")) {
		t.Fatalf("CSV does not use CRLF: %q", output.Bytes())
	}
	reader := csv.NewReader(strings.NewReader(output.String()))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := records[1][0]; got != "'=HYPERLINK(\"https://example.invalid\")" {
		t.Fatalf("activity formula cell=%q", got)
	}
	if got := records[1][3]; got != "'+client" {
		t.Fatalf("client formula cell=%q", got)
	}
	if got := records[1][17]; got != "-5" {
		t.Fatalf("numeric value was rewritten: %q", got)
	}
}

func TestJSONExportPreservesOriginalText(t *testing.T) {
	connection := store.NetworkConnection{
		ActivityID: "=formula", Protocol: "tcp", State: "closed",
		AcceptedAt: 1, LastActivityAt: 1,
	}
	var output bytes.Buffer
	_, err := encodeJSONExport(&output, time.UnixMilli(1), map[string]any{"scope": "history"}, func(yield func(any) error) (int, error) {
		return 1, yield(connection)
	})
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Items []store.NetworkConnection `json:"items"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Items) != 1 || document.Items[0].ActivityID != "=formula" {
		t.Fatalf("JSON text changed: %#v", document.Items)
	}
}
