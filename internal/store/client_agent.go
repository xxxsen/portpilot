package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type ClientAgentSnapshot struct {
	ClientID          string   `json:"client_id"`
	AgentStatus       string   `json:"agent_status"`
	AgentVersion      string   `json:"version"`
	AgentCommit       string   `json:"commit"`
	AgentBuildTime    string   `json:"build_time"`
	GoVersion         string   `json:"go_version"`
	OS                string   `json:"os"`
	Arch              string   `json:"arch"`
	Hostname          string   `json:"hostname"`
	ProcessStartedAt  int64    `json:"started_at"`
	ProtocolVersion   int      `json:"protocol_version"`
	CapabilitiesJSON  string   `json:"-"`
	Capabilities      []string `json:"capabilities"`
	SourceAddr        string   `json:"source_addr"`
	SourceIP          string   `json:"source_ip"`
	FirstSeenAt       int64    `json:"first_seen_at"`
	LastSeenAt        int64    `json:"last_seen_at"`
	LastOnlineAt      int64    `json:"last_online_at"`
	LastOfflineAt     *int64   `json:"last_offline_at,omitempty"`
	LastOfflineReason string   `json:"last_offline_reason"`
	LastSessionEpoch  uint64   `json:"last_session_epoch"`
}

func (s *Store) UpsertClientAgentSnapshot(
	ctx context.Context,
	snapshot ClientAgentSnapshot,
) error {
	if snapshot.ClientID == "" || snapshot.FirstSeenAt <= 0 ||
		snapshot.LastSeenAt <= 0 || snapshot.LastOnlineAt <= 0 {
		return errors.New("client agent snapshot identity and timestamps are required")
	}
	if snapshot.CapabilitiesJSON == "" {
		snapshot.CapabilitiesJSON = "[]"
	}
	if !json.Valid([]byte(snapshot.CapabilitiesJSON)) {
		return errors.New("client agent capabilities must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO client_agent_snapshots (
		client_id, agent_status, agent_version, agent_commit, agent_build_time, go_version,
		os, arch, hostname, process_started_at, protocol_version,
		capabilities_json, source_addr, source_ip, first_seen_at, last_seen_at,
		last_online_at, last_offline_at, last_offline_reason, last_session_epoch
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(client_id) DO UPDATE SET
		agent_status=excluded.agent_status,
		agent_version=excluded.agent_version,
		agent_commit=excluded.agent_commit,
		agent_build_time=excluded.agent_build_time,
		go_version=excluded.go_version,
		os=excluded.os,
		arch=excluded.arch,
		hostname=excluded.hostname,
		process_started_at=excluded.process_started_at,
		protocol_version=excluded.protocol_version,
		capabilities_json=excluded.capabilities_json,
		source_addr=excluded.source_addr,
		source_ip=excluded.source_ip,
		first_seen_at=MIN(first_seen_at, excluded.first_seen_at),
		last_seen_at=excluded.last_seen_at,
		last_online_at=excluded.last_online_at,
		last_offline_at=COALESCE(excluded.last_offline_at, last_offline_at),
		last_offline_reason=CASE
			WHEN excluded.last_offline_at IS NULL THEN last_offline_reason
			ELSE excluded.last_offline_reason
		END,
		last_session_epoch=excluded.last_session_epoch`,
		snapshot.ClientID, snapshot.AgentStatus, snapshot.AgentVersion, snapshot.AgentCommit,
		snapshot.AgentBuildTime, snapshot.GoVersion, snapshot.OS, snapshot.Arch,
		snapshot.Hostname, snapshot.ProcessStartedAt, snapshot.ProtocolVersion,
		snapshot.CapabilitiesJSON, snapshot.SourceAddr, snapshot.SourceIP,
		snapshot.FirstSeenAt, snapshot.LastSeenAt, snapshot.LastOnlineAt,
		snapshot.LastOfflineAt, snapshot.LastOfflineReason, snapshot.LastSessionEpoch,
	)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("upsert client agent snapshot: %w", err)
	}
	return nil
}

func (s *Store) ClientAgentSnapshot(
	ctx context.Context,
	clientID string,
) (ClientAgentSnapshot, error) {
	return scanClientAgent(s.db.QueryRowContext(
		ctx,
		`SELECT client_id, agent_status, agent_version, agent_commit, agent_build_time,
			go_version, os, arch, hostname, process_started_at, protocol_version,
			capabilities_json, source_addr, source_ip, first_seen_at, last_seen_at,
			last_online_at, last_offline_at, last_offline_reason, last_session_epoch
		FROM client_agent_snapshots WHERE client_id=?`,
		clientID,
	))
}

func (s *Store) ListClientAgentSnapshots(
	ctx context.Context,
) (map[string]ClientAgentSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		client_id, agent_status, agent_version, agent_commit, agent_build_time,
		go_version, os, arch, hostname, process_started_at, protocol_version,
		capabilities_json, source_addr, source_ip, first_seen_at, last_seen_at,
		last_online_at, last_offline_at, last_offline_reason, last_session_epoch
		FROM client_agent_snapshots`)
	if err != nil {
		return nil, fmt.Errorf("list client agent snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]ClientAgentSnapshot)
	for rows.Next() {
		snapshot, scanErr := scanClientAgent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result[snapshot.ClientID] = snapshot
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client agent snapshots: %w", err)
	}
	return result, nil
}

func scanClientAgent(scanner rowScanner) (ClientAgentSnapshot, error) {
	var snapshot ClientAgentSnapshot
	var offline sql.NullInt64
	if err := scanner.Scan(
		&snapshot.ClientID, &snapshot.AgentStatus, &snapshot.AgentVersion, &snapshot.AgentCommit,
		&snapshot.AgentBuildTime, &snapshot.GoVersion, &snapshot.OS, &snapshot.Arch,
		&snapshot.Hostname, &snapshot.ProcessStartedAt, &snapshot.ProtocolVersion,
		&snapshot.CapabilitiesJSON, &snapshot.SourceAddr, &snapshot.SourceIP,
		&snapshot.FirstSeenAt, &snapshot.LastSeenAt, &snapshot.LastOnlineAt,
		&offline, &snapshot.LastOfflineReason, &snapshot.LastSessionEpoch,
	); err != nil {
		return ClientAgentSnapshot{}, err
	}
	if offline.Valid {
		snapshot.LastOfflineAt = &offline.Int64
	}
	if err := json.Unmarshal([]byte(snapshot.CapabilitiesJSON), &snapshot.Capabilities); err != nil {
		return ClientAgentSnapshot{}, fmt.Errorf("decode client capabilities: %w", err)
	}
	return snapshot, nil
}
