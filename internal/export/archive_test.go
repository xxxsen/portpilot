package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestBuildCreatesOnlyDatasetAndManifestWithMatchingDigest(t *testing.T) {
	exportedAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	data := []byte("id,name\r\n1,alpha\r\n")
	archive, err := Build(
		"network_connections", "csv", exportedAt,
		map[string]any{"scope": "history"},
		func(output io.Writer) (int, error) {
			_, err := output.Write(data)
			return 1, err
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if _, err := archive.WriteTo(&encoded); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 ||
		reader.File[0].Name != "network_connections.csv" ||
		reader.File[1].Name != "manifest.json" {
		t.Fatalf("entries=%#v", reader.File)
	}
	dataset, err := readZipEntry(reader.File[0])
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := readZipEntry(reader.File[1])
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(dataset)
	if manifest.RowCount != 1 || manifest.UncompressedBytes != int64(len(dataset)) ||
		manifest.DataSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("manifest=%#v", manifest)
	}
}

func TestBuildEnforcesUncompressedRowsAndArchiveLimits(t *testing.T) {
	accepted, err := Build(
		"audit_events", "json", time.Now(), nil,
		func(io.Writer) (int, error) { return MaxRows, nil },
	)
	if err != nil {
		t.Fatalf("maximum row count rejected: %v", err)
	}
	accepted.Release()

	_, err = Build(
		"audit_events", "json", time.Now(), nil,
		func(io.Writer) (int, error) { return MaxRows + 1, nil },
	)
	if !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("row error=%v", err)
	}
	_, err = Build(
		"audit_events", "json", time.Now(), nil,
		func(output io.Writer) (int, error) {
			_, err := output.Write(make([]byte, MaxUncompressedBytes+1))
			return 1, err
		},
	)
	if !errors.Is(err, ErrUncompressedTooLarge) {
		t.Fatalf("uncompressed error=%v", err)
	}
	buffer := newChunkedBuffer(3)
	if _, err := buffer.Write([]byte("four")); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("archive error=%v", err)
	}
}

func TestArchiveReleaseAndCanceledWrite(t *testing.T) {
	archive, err := Build(
		"audit_events", "json", time.Now(), nil,
		func(output io.Writer) (int, error) {
			_, err := output.Write([]byte(`{"items":[]}`))
			return 0, err
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := archive.WriteToContext(ctx, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write error=%v", err)
	}
	archive.Release()
	if archive.Size() != 0 {
		t.Fatalf("released archive size=%d", archive.Size())
	}
	if _, err := archive.WriteTo(io.Discard); err == nil {
		t.Fatal("released archive remained writable")
	}
}

func readZipEntry(file *zip.File) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open ZIP entry: %w", err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read ZIP entry: %w", err)
	}
	return data, nil
}
