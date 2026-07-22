package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	databaseName      = "portpilot.db"
	geoIPDirName      = "geoip"
	quarantineDirName = "quarantine"
)

// Root owns PortPilot's disposable server-side cache layout.
type Root struct {
	path      string
	temporary bool
	closeOnce sync.Once
	closeErr  error
}

func Open(configured string) (*Root, error) {
	if configured == "" {
		path, err := os.MkdirTemp(os.TempDir(), "portpilot-server-")
		if err != nil {
			return nil, fmt.Errorf("create temporary cache directory: %w", err)
		}
		//nolint:gosec // A private directory requires the owner execute bit.
		if err := os.Chmod(path, 0o700); err != nil {
			_ = os.RemoveAll(path)
			return nil, fmt.Errorf("set temporary cache permissions: %w", err)
		}
		root := &Root{path: path, temporary: true}
		if err := root.prepare(); err != nil {
			_ = os.RemoveAll(path)
			return nil, err
		}
		return root, nil
	}

	path, err := filepath.Abs(configured)
	if err != nil {
		return nil, fmt.Errorf("resolve cache directory: %w", err)
	}
	if err := ensureRoot(path); err != nil {
		return nil, err
	}
	root := &Root{path: path}
	if err := root.prepare(); err != nil {
		return nil, err
	}
	return root, nil
}

func ensureRoot(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cache directory must not be a symlink")
		}
		if !info.IsDir() {
			return errors.New("cache path must be a directory")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create cache directory: %w", err)
		}
	default:
		return fmt.Errorf("inspect cache directory: %w", err)
	}
	//nolint:gosec // path is the validated cache root; the fixed probe name cannot escape it.
	probe, err := os.OpenFile(filepath.Join(path, ".portpilot-write-test"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cache directory is not writable: %w", err)
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(probe.Name())
		return fmt.Errorf("close cache write test: %w", err)
	}
	if err := os.Remove(probe.Name()); err != nil {
		return fmt.Errorf("remove cache write test: %w", err)
	}
	return nil
}

func (r *Root) prepare() error {
	for _, name := range []string{geoIPDirName, quarantineDirName} {
		if err := ensureSubdirectory(filepath.Join(r.path, name)); err != nil {
			return err
		}
	}
	for _, path := range []string{r.DatabasePath(), r.GeoIPDatabasePath(), r.GeoIPMetadataPath()} {
		if err := rejectSymlink(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureSubdirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("cache subdirectory %q must be a real directory", filepath.Base(path))
		}
		//nolint:gosec // A private directory requires the owner execute bit.
		//nolint:gosec // A private directory requires the owner execute bit.
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("set cache subdirectory permissions: %w", err)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(path, 0o700); err != nil {
			return fmt.Errorf("create cache subdirectory %q: %w", filepath.Base(path), err)
		}
		return nil
	default:
		return fmt.Errorf("inspect cache subdirectory %q: %w", filepath.Base(path), err)
	}
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect cache file %q: %w", filepath.Base(path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache file %q must not be a symlink", filepath.Base(path))
	}
	return nil
}

func (r *Root) Path() string { return r.path }

func (r *Root) Mode() string {
	if r.temporary {
		return "temporary"
	}
	return "configured"
}

func (r *Root) Temporary() bool { return r.temporary }

func (r *Root) DatabasePath() string { return filepath.Join(r.path, databaseName) }

func (r *Root) GeoIPDir() string { return filepath.Join(r.path, geoIPDirName) }

func (r *Root) GeoIPDatabasePath() string {
	return filepath.Join(r.GeoIPDir(), "country.mmdb")
}

func (r *Root) GeoIPMetadataPath() string {
	return filepath.Join(r.GeoIPDir(), "metadata.json")
}

func (r *Root) QuarantineDir() string {
	return filepath.Join(r.path, quarantineDirName)
}

func (r *Root) Close() error {
	r.closeOnce.Do(func() {
		if r.temporary {
			r.closeErr = os.RemoveAll(r.path)
		}
	})
	return r.closeErr
}

func IsManagedTemporaryName(name string) bool {
	return strings.HasPrefix(name, ".country-") &&
		(strings.HasSuffix(name, ".download") || strings.HasSuffix(name, ".mmdb"))
}
