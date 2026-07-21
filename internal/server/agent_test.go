package server

import (
	"strings"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
)

func TestNormalizeAgent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	agent := &protocol.Agent{
		Version: "v1", Commit: "abc", BuildTime: "now", GoVersion: "go1",
		OS: "linux", Arch: "amd64", Hostname: "gateway",
		StartedAt: now.UnixMilli(), ProtocolVersion: 1,
		Capabilities: []string{"tcp-smux", "tcp-direct"},
	}
	normalized, status, err := normalizeAgent(agent, now)
	if err != nil || status != "available" ||
		len(normalized.Capabilities) != 2 {
		t.Fatalf("agent = %#v/%q/%v", normalized, status, err)
	}
	agent.StartedAt = now.Add(25 * time.Hour).UnixMilli()
	normalized, status, err = normalizeAgent(agent, now)
	if err != nil || status != "available" || normalized.StartedAt != 0 {
		t.Fatalf("future started_at was not normalized: %#v/%q/%v", normalized, status, err)
	}
}

func TestNormalizeAgentRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	base := protocol.Agent{ProtocolVersion: 1}
	tooLong := base
	tooLong.Version = strings.Repeat("x", 65)
	if _, status, err := normalizeAgent(&tooLong, time.Now()); err == nil || status != "unavailable" {
		t.Fatal("oversized version was accepted")
	}
	duplicate := base
	duplicate.Capabilities = []string{"tcp-smux", "tcp-smux"}
	if _, status, err := normalizeAgent(&duplicate, time.Now()); err == nil || status != "unavailable" {
		t.Fatal("duplicate capability was accepted")
	}
	if _, status, err := normalizeAgent(nil, time.Now()); err != nil || status != "legacy" {
		t.Fatal("legacy agent was rejected")
	}
}

func TestSessionCloseReasonKeepsFirstCause(t *testing.T) {
	t.Parallel()
	session := &Session{}
	session.setCloseReason("admin_disconnect")
	session.setCloseReason("control_read_failed")
	if got := session.CloseReason(); got != "admin_disconnect" {
		t.Fatalf("close reason = %q, want admin_disconnect", got)
	}
}
