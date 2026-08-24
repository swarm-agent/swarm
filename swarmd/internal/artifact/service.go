// Package artifact validates, projects, and materializes Git-authoritative artifacts.
package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultMaxArtifactBytes = int64(64 << 20)
	DefaultMaxVideoArtifactBytes = int64(512 << 20)
	DefaultMaxSessionBytes = int64(2048 << 20)
	DefaultMaxPackageFiles = 512
	DefaultMaxPackageEntryBytes = int64(16 << 20)
	DefaultMaxPackageBytes = int64(128 << 20)
)

var (
	ErrQuotaExceeded = errors.New("artifact storage quota exceeded")
	ErrNotReady = errors.New("artifact bytes are not ready")
)

type Limits struct {
	MaxArtifactBytes int64
	MaxVideoArtifactBytes int64
	MaxSessionBytes int64
	MaxPackageFiles int
	MaxPackageEntryBytes int64
	MaxPackageBytes int64
}

type PackageEntry struct { Name string; Data []byte }
type PackageManifestEntry struct { Name string; Size int64 }
func normalizeLimits(l Limits) Limits {
	if l.MaxArtifactBytes <= 0 { l.MaxArtifactBytes = DefaultMaxArtifactBytes }
	if l.MaxVideoArtifactBytes <= 0 { l.MaxVideoArtifactBytes = DefaultMaxVideoArtifactBytes }
	if l.MaxSessionBytes <= 0 { l.MaxSessionBytes = DefaultMaxSessionBytes }
	if l.MaxPackageFiles <= 0 { l.MaxPackageFiles = DefaultMaxPackageFiles }
	if l.MaxPackageEntryBytes <= 0 { l.MaxPackageEntryBytes = DefaultMaxPackageEntryBytes }
	if l.MaxPackageBytes <= 0 { l.MaxPackageBytes = DefaultMaxPackageBytes }
	return l
}

func isVideoMediaTypeOrKind(mediaType, kind string) bool {
	mediaType, kind = strings.ToLower(strings.TrimSpace(mediaType)), strings.ToLower(strings.TrimSpace(kind))
	return kind == "video" || mediaType == "video/mp4" || strings.HasPrefix(mediaType, "video/")
}

func safePackageEntryName(value string) bool {
	if !safeRelativePath(filepath.FromSlash(value)) || strings.HasSuffix(value, "/") { return false }
	for _, part := range strings.Split(value, "/") { if part == "" || part == "." || part == ".." { return false } }
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value
}
func safeRelativePath(value string) bool {
	if value == "" || value == "." || filepath.IsAbs(value) { return false }
	clean := filepath.Clean(value); return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func requireRealDirectory(path string) error { info, err := os.Lstat(path); if err != nil { return err }; if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() { return errors.New("path is not a real directory") }; return nil }

func copyBounded(ctx context.Context, dst io.Writer, src io.Reader, limit int64) ([]byte, error) {
	hash := sha256.New(); writer := &boundedWriter{writer: io.MultiWriter(dst, hash), limit: limit}; buffer := make([]byte, 32<<10)
	for { if err := ctx.Err(); err != nil { return nil, err }; n, readErr := src.Read(buffer); if n > 0 { if _, err := writer.Write(buffer[:n]); err != nil { return nil, err } }; if errors.Is(readErr, io.EOF) { break }; if readErr != nil { return nil, readErr } }
	return hash.Sum(nil), nil
}
type boundedWriter struct { writer io.Writer; limit, wrote int64 }
func (w *boundedWriter) Write(p []byte) (int, error) { if int64(len(p)) > w.limit-w.wrote { return 0, ErrQuotaExceeded }; n, err := w.writer.Write(p); w.wrote += int64(n); return n, err }
func randomToken() (string, error) { var value [16]byte; if _, err := rand.Read(value[:]); err != nil { return "", err }; return hex.EncodeToString(value[:]), nil }

func removeDirectoryTree(root string) error {
	root = filepath.Clean(root); info, err := os.Lstat(root); if err != nil { return err }; if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() { return errors.New("artifact removal target is not a real directory") }
	entries, err := os.ReadDir(root); if err != nil { return err }
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name()); current, err := os.Lstat(path); if err != nil { return err }
		if current.Mode()&os.ModeSymlink != 0 { return errors.New("artifact removal target contains a symlink") }
		if current.IsDir() { if err := removeDirectoryTree(path); err != nil { return err }; continue }
		if !current.Mode().IsRegular() { return errors.New("artifact removal target contains a special file") }
		if err := os.Remove(path); err != nil { return err }
	}
	return os.Remove(root)
}
