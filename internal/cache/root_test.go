package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredRootUsesFixedLayoutAndPersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root.Mode() != "configured" || root.DatabasePath() != filepath.Join(dir, "portpilot.db") {
		t.Fatalf("unexpected root: mode=%s db=%s", root.Mode(), root.DatabasePath())
	}
	for _, path := range []string{root.GeoIPDir(), root.QuarantineDir()} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("missing layout directory %s: %v", path, statErr)
		}
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("configured cache removed: %v", err)
	}
}

func TestTemporaryRootIsRemovedOnClose(t *testing.T) {
	root, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	path := root.Path()
	if root.Mode() != "temporary" {
		t.Fatalf("mode = %s", root.Mode())
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary cache still exists: %v", err)
	}
}

func TestRootRejectsSymlinksAndFiles(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("expected root symlink rejection")
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file); err == nil {
		t.Fatal("expected non-directory rejection")
	}
}

func TestRootRejectsManagedFileSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, geoIPDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, quarantineDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, databaseName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("expected managed file symlink rejection")
	}
}
