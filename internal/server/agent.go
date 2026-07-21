package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

const (
	maxAgentFieldBytes   = 64
	maxAgentHostname     = 255
	maxAgentCapabilities = 32
)

func normalizeAgent(value *protocol.Agent, now time.Time) (protocol.Agent, string, error) {
	if value == nil {
		return protocol.Agent{}, "legacy", nil
	}
	agent := *value
	agent.Capabilities = append([]string(nil), value.Capabilities...)
	for _, field := range []string{
		agent.Version, agent.Commit, agent.BuildTime, agent.GoVersion, agent.OS, agent.Arch,
	} {
		if !utf8.ValidString(field) || len(field) > maxAgentFieldBytes {
			return protocol.Agent{}, "unavailable", errors.New("invalid agent text field")
		}
	}
	if !utf8.ValidString(agent.Hostname) || len(agent.Hostname) > maxAgentHostname {
		return protocol.Agent{}, "unavailable", errors.New("invalid agent hostname")
	}
	if agent.ProtocolVersion <= 0 {
		return protocol.Agent{}, "unavailable", errors.New("invalid agent protocol version")
	}
	if len(agent.Capabilities) > maxAgentCapabilities {
		return protocol.Agent{}, "unavailable", errors.New("too many agent capabilities")
	}
	seen := make(map[string]struct{}, len(agent.Capabilities))
	for index := range agent.Capabilities {
		capability := strings.TrimSpace(agent.Capabilities[index])
		if capability == "" || !utf8.ValidString(capability) || len(capability) > maxAgentFieldBytes {
			return protocol.Agent{}, "unavailable", errors.New("invalid agent capability")
		}
		if _, ok := seen[capability]; ok {
			return protocol.Agent{}, "unavailable", errors.New("duplicate agent capability")
		}
		seen[capability] = struct{}{}
		agent.Capabilities[index] = capability
	}
	if agent.StartedAt < 0 ||
		agent.StartedAt > now.Add(24*time.Hour).UTC().UnixMilli() {
		agent.StartedAt = 0
	}
	return agent, "available", nil
}

func agentSnapshot(session *Session, offlineAt *int64) store.ClientAgentSnapshot {
	capabilities, _ := json.Marshal(session.Agent.Capabilities)
	return store.ClientAgentSnapshot{
		ClientID: session.ClientID, AgentStatus: session.AgentStatus,
		AgentVersion: session.Agent.Version, AgentCommit: session.Agent.Commit,
		AgentBuildTime: session.Agent.BuildTime, GoVersion: session.Agent.GoVersion,
		OS: session.Agent.OS, Arch: session.Agent.Arch, Hostname: session.Agent.Hostname,
		ProcessStartedAt: session.Agent.StartedAt,
		ProtocolVersion:  session.Agent.ProtocolVersion,
		CapabilitiesJSON: string(capabilities),
		SourceAddr:       session.SourceAddr, SourceIP: session.SourceIP,
		FirstSeenAt: session.ConnectedAt, LastSeenAt: session.lastSeenAt.Load(),
		LastOnlineAt: session.ConnectedAt, LastOfflineAt: offlineAt,
		LastOfflineReason: session.CloseReason(), LastSessionEpoch: session.Epoch,
	}
}

func (a *App) startAgentWriter() {
	a.agentSnapshots = make(chan store.ClientAgentSnapshot, 256)
	a.agentWriterWG.Add(1)
	go func() {
		defer a.agentWriterWG.Done()
		for snapshot := range a.agentSnapshots {
			if err := a.Events.UpsertClientAgentSnapshot(context.Background(), snapshot); err != nil {
				a.storeError.Store(true)
				a.Logger.Error("persist client agent snapshot", zap.Error(err))
			}
		}
	}()
}

func (a *App) enqueueAgentSnapshot(snapshot store.ClientAgentSnapshot) {
	if a.agentSnapshots == nil {
		return
	}
	select {
	case a.agentSnapshots <- snapshot:
	default:
		a.storeError.Store(true)
	}
}

func (a *App) closeAgentWriter() {
	a.agentWriterClose.Do(func() {
		if a.agentSnapshots != nil {
			close(a.agentSnapshots)
			a.agentWriterWG.Wait()
		}
	})
}
