package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commonlogger "github.com/xxxsen/common/logger"
	"github.com/xxxsen/common/logutil"
)

func TestInitCreatesDirectoryAndDefaultsEmptyLevelToDebug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "portpilot.log")
	logger, err := Init(commonlogger.LogConfig{
		File: path, FileSize: 1048576, FileCount: 2, KeepDays: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	logutil.GetLogger(t.Context()).Debug("debug logging enabled")
	Sync(logger)
	data, err := os.ReadFile(path) //nolint:gosec // The path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "debug logging enabled") ||
		!strings.Contains(string(data), "DEBUG") {
		t.Fatalf("unexpected log output: %q", data)
	}
}

func TestInitRejectsUncreatableLogDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, err := Init(commonlogger.LogConfig{
		File:  filepath.Join(parent, "portpilot.log"),
		Level: "debug", FileSize: 1048576,
	})
	if err == nil || logger != nil {
		t.Fatal("logging initialized beneath a regular file")
	}
}

func TestInitRejectsInvalidConfiguration(t *testing.T) {
	tests := []commonlogger.LogConfig{
		{Level: "verbose", Console: true},
		{Level: "debug"},
		{File: filepath.Join(t.TempDir(), "portpilot.log"), Level: "debug"},
	}
	for _, config := range tests {
		if logger, err := Init(config); err == nil || logger != nil {
			t.Fatalf("Init(%+v) succeeded", config)
		}
	}
}
