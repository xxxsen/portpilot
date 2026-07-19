package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

var ErrRevisionConflict = errors.New("configuration revision conflict")

type Manager struct {
	path         string
	adminAddr    string
	serverAddr   string
	targetPolicy TargetPolicyConfig
	writeMu      sync.Mutex
	current      atomic.Pointer[ClientFile]
}

func NewManager(path, adminAddr, serverAddr string) (*Manager, error) {
	return NewManagerWithPolicy(path, adminAddr, serverAddr, TargetPolicyConfig{})
}

func NewManagerWithPolicy(path, adminAddr, serverAddr string, targetPolicy TargetPolicyConfig) (*Manager, error) {
	file, err := LoadClientFile(path)
	if err != nil {
		return nil, err
	}
	if err := file.Validate(adminAddr, serverAddr); err != nil {
		return nil, err
	}
	if err := ValidateTargets(file, targetPolicy); err != nil {
		return nil, err
	}
	manager := &Manager{
		path: path, adminAddr: adminAddr, serverAddr: serverAddr,
		targetPolicy: targetPolicy,
	}
	manager.current.Store(&file)
	return manager, nil
}

func (m *Manager) Snapshot() ClientFile {
	value := m.current.Load()
	if value == nil {
		return ClientFile{}
	}
	return cloneClientFile(*value)
}

type PrepareFunc func(ClientFile) (commit func(), abort func(), err error)

func (m *Manager) Replace(expectedRevision uint64, next ClientFile, prepare PrepareFunc) (ClientFile, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return ClientFile{}, fmt.Errorf("create config directory: %w", err)
	}
	lock, err := os.OpenFile(m.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return ClientFile{}, fmt.Errorf("open config lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return ClientFile{}, fmt.Errorf("lock client configuration: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	latest, err := LoadClientFile(m.path)
	if err != nil {
		return ClientFile{}, err
	}
	if latest.Revision != expectedRevision {
		return ClientFile{}, ErrRevisionConflict
	}
	if next.Revision != 0 && next.Revision != expectedRevision {
		return ClientFile{}, ErrRevisionConflict
	}
	preserveRedactionMarkers(&next, latest)
	next.Revision = expectedRevision + 1
	if err := next.Validate(m.adminAddr, m.serverAddr); err != nil {
		return ClientFile{}, err
	}
	if err := ValidateTargets(next, m.targetPolicy); err != nil {
		return ClientFile{}, err
	}
	commit := func() {}
	abort := func() {}
	if prepare != nil {
		commit, abort, err = prepare(next)
		if err != nil {
			return ClientFile{}, err
		}
	}
	if err := writeAtomic(m.path, next); err != nil {
		abort()
		return ClientFile{}, err
	}
	commit()
	snapshot := cloneClientFile(next)
	m.current.Store(&snapshot)
	return cloneClientFile(snapshot), nil
}

func (m *Manager) Reload() (ClientFile, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	file, err := LoadClientFile(m.path)
	if err != nil {
		return ClientFile{}, err
	}
	if err := file.Validate(m.adminAddr, m.serverAddr); err != nil {
		return ClientFile{}, err
	}
	if err := ValidateTargets(file, m.targetPolicy); err != nil {
		return ClientFile{}, err
	}
	snapshot := cloneClientFile(file)
	m.current.Store(&snapshot)
	return cloneClientFile(snapshot), nil
}

func Redacted(file ClientFile) ClientFile {
	copy := cloneClientFile(file)
	for i := range copy.ClientConfig {
		if copy.ClientConfig[i].ClientToken != "" {
			copy.ClientConfig[i].ClientToken = RedactionMarker
		}
	}
	return copy
}

func preserveRedactionMarkers(next *ClientFile, current ClientFile) {
	tokens := make(map[string]string, len(current.ClientConfig))
	for _, client := range current.ClientConfig {
		tokens[client.ClientID] = client.ClientToken
	}
	for i := range next.ClientConfig {
		if next.ClientConfig[i].ClientToken == RedactionMarker {
			next.ClientConfig[i].ClientToken = tokens[next.ClientConfig[i].ClientID]
		}
	}
}

func writeAtomic(path string, value ClientFile) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode client configuration: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".portpilot-clients-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary client configuration: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set temporary client configuration permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary client configuration: %w", err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace client configuration: %w", err)
	}
	directory, err := os.Open(dir) //nolint:gosec // The directory is derived from the operator-configured destination path.
	if err != nil {
		return fmt.Errorf("open client configuration directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

func cloneClientFile(value ClientFile) ClientFile {
	data, _ := json.Marshal(value)
	var clone ClientFile
	_ = json.Unmarshal(data, &clone)
	return clone
}
