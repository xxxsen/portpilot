package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	commonlogger "github.com/xxxsen/common/logger"
	"go.uber.org/zap"
)

func Init(config commonlogger.LogConfig) (*zap.Logger, error) {
	if config == (commonlogger.LogConfig{}) {
		config.Console = true
	}
	config.Level = strings.ToLower(strings.TrimSpace(config.Level))
	if config.Level == "" {
		config.Level = "debug"
	}
	switch config.Level {
	case "panic", "fatal", "error", "warn", "info", "debug":
	default:
		return nil, fmt.Errorf("unsupported log level %q", config.Level)
	}
	if config.File == "" && !config.Console {
		return nil, fmt.Errorf("at least one of file or console must be enabled")
	}
	if config.File != "" && config.FileSize == 0 {
		return nil, fmt.Errorf("file_size must be positive when file is configured")
	}
	maxInt := uint64(^uint(0) >> 1)
	if config.FileSize > maxInt || config.FileCount > maxInt || uint64(config.KeepDays) > maxInt {
		return nil, fmt.Errorf("log rotation value exceeds platform integer range")
	}
	if config.File != "" {
		if err := os.MkdirAll(filepath.Dir(config.File), 0o700); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
	}
	return commonlogger.Init(
		config.File,
		config.Level,
		int(config.FileCount),
		int(config.FileSize),
		int(config.KeepDays),
		config.Console,
	), nil
}

func Sync(logger *zap.Logger) {
	if logger != nil {
		_ = logger.Sync()
	}
}
