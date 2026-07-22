package export

import (
	"archive/zip"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"
)

const (
	ChunkSize            = 256 << 10
	MaxArchiveBytes      = 32 << 20
	MaxUncompressedBytes = 50 << 20
	MaxRows              = 10_000
)

var (
	ErrArchiveTooLarge      = errors.New("export archive exceeds size limit")
	ErrUncompressedTooLarge = errors.New("export data exceeds uncompressed size limit")
	ErrTooManyRows          = errors.New("export contains too many rows")
)

type Manifest struct {
	Version           int            `json:"version"`
	Dataset           string         `json:"dataset"`
	Format            string         `json:"format"`
	ExportedAt        int64          `json:"exported_at"`
	Filters           map[string]any `json:"filters"`
	RowCount          int            `json:"row_count"`
	UncompressedBytes int64          `json:"uncompressed_bytes"`
	DataSHA256        string         `json:"data_sha256"`
}

type Archive struct {
	buffer   *chunkedBuffer
	Manifest Manifest
}

type Encoder func(io.Writer) (int, error)

func Build(
	dataset string,
	format string,
	exportedAt time.Time,
	filters map[string]any,
	encode Encoder,
) (*Archive, error) {
	if !validDataset(dataset) || (format != "csv" && format != "json") {
		return nil, errors.New("invalid export dataset or format")
	}
	buffer := newChunkedBuffer(MaxArchiveBytes)
	succeeded := false
	defer func() {
		if !succeeded {
			buffer.release()
		}
	}()
	zipWriter := zip.NewWriter(buffer)
	zipWriter.RegisterCompressor(zip.Deflate, func(output io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(output, flate.BestSpeed)
	})
	header := &zip.FileHeader{
		Name: dataset + "." + format, Method: zip.Deflate,
		Modified: exportedAt.UTC(),
	}
	entry, err := zipWriter.CreateHeader(header)
	if err != nil {
		return nil, normalizeSizeError(err)
	}
	counter := &countingHashWriter{
		output: entry, hash: sha256.New(), limit: MaxUncompressedBytes,
	}
	rowCount, err := encode(counter)
	if err != nil {
		return nil, normalizeSizeError(err)
	}
	if rowCount > MaxRows {
		return nil, ErrTooManyRows
	}
	manifest := Manifest{
		Version: 1, Dataset: dataset, Format: format,
		ExportedAt: exportedAt.UTC().UnixMilli(), Filters: filters,
		RowCount: rowCount, UncompressedBytes: counter.written,
		DataSHA256: hex.EncodeToString(counter.hash.Sum(nil)),
	}
	manifestHeader := &zip.FileHeader{
		Name: "manifest.json", Method: zip.Deflate,
		Modified: exportedAt.UTC(),
	}
	manifestEntry, err := zipWriter.CreateHeader(manifestHeader)
	if err != nil {
		return nil, normalizeSizeError(err)
	}
	manifestEncoder := json.NewEncoder(manifestEntry)
	manifestEncoder.SetEscapeHTML(false)
	if err := manifestEncoder.Encode(manifest); err != nil {
		return nil, normalizeSizeError(err)
	}
	if err := zipWriter.Close(); err != nil {
		return nil, normalizeSizeError(err)
	}
	succeeded = true
	return &Archive{buffer: buffer, Manifest: manifest}, nil
}

func (a *Archive) Size() int64 {
	if a == nil || a.buffer == nil {
		return 0
	}
	return a.buffer.size
}

func (a *Archive) WriteTo(output io.Writer) (int64, error) {
	return a.WriteToContext(context.Background(), output)
}

func (a *Archive) WriteToContext(ctx context.Context, output io.Writer) (int64, error) {
	if a == nil || a.buffer == nil {
		return 0, errors.New("export archive is unavailable")
	}
	var written int64
	remaining := a.buffer.size
	for _, chunk := range a.buffer.chunks {
		if err := ctx.Err(); err != nil {
			return written, fmt.Errorf("archive write canceled: %w", err)
		}
		if remaining == 0 {
			break
		}
		length := min(int64(len(chunk)), remaining)
		count, err := output.Write(chunk[:length])
		written += int64(count)
		remaining -= int64(count)
		if err != nil {
			return written, fmt.Errorf("archive write canceled: %w", err)
		}
		if int64(count) != length {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (a *Archive) Release() {
	if a == nil || a.buffer == nil {
		return
	}
	a.buffer.release()
	a.buffer = nil
}

type chunkedBuffer struct {
	chunks [][]byte
	size   int64
	limit  int64
}

func newChunkedBuffer(limit int64) *chunkedBuffer {
	return &chunkedBuffer{limit: limit}
}

func (b *chunkedBuffer) release() {
	for index := range b.chunks {
		b.chunks[index] = nil
	}
	b.chunks = nil
	b.size = 0
}

func (b *chunkedBuffer) Write(value []byte) (int, error) {
	if int64(len(value)) > b.limit-b.size {
		return 0, ErrArchiveTooLarge
	}
	written := 0
	for len(value) > 0 {
		index := int(b.size / ChunkSize)
		offset := int(b.size % ChunkSize)
		if index == len(b.chunks) {
			b.chunks = append(b.chunks, make([]byte, ChunkSize))
		}
		count := copy(b.chunks[index][offset:], value)
		value = value[count:]
		written += count
		b.size += int64(count)
	}
	return written, nil
}

type countingHashWriter struct {
	output  io.Writer
	hash    hash.Hash
	written int64
	limit   int64
}

func (w *countingHashWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > w.limit-w.written {
		return 0, ErrUncompressedTooLarge
	}
	count, err := w.output.Write(value)
	if count > 0 {
		_, _ = w.hash.Write(value[:count])
		w.written += int64(count)
	}
	return count, err
}

func normalizeSizeError(err error) error {
	switch {
	case errors.Is(err, ErrArchiveTooLarge):
		return ErrArchiveTooLarge
	case errors.Is(err, ErrUncompressedTooLarge):
		return ErrUncompressedTooLarge
	default:
		return fmt.Errorf("build export archive: %w", err)
	}
}

func validDataset(value string) bool {
	switch value {
	case "network_connections", "network_dimensions", "audit_events":
		return true
	default:
		return false
	}
}
