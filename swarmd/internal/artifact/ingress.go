package artifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// canonicalArtifactBytes validates and bounds an artifact before it enters Git.
// It never writes managed artifact bytes to an application-owned content path.
func artifactByteLimit(limits Limits, mediaType, kind string) int64 {
	if isVideoMediaTypeOrKind(mediaType, kind) { return limits.MaxVideoArtifactBytes }
	return limits.MaxArtifactBytes
}

func canonicalArtifactBytes(ctx context.Context, limits Limits, variant pebblestore.SessionArtifactVariant, body []byte) ([]byte, string, int64, error) {
	limits = normalizeLimits(limits)
	if err := ctx.Err(); err != nil { return nil, "", 0, err }
	if strings.TrimSpace(variant.Filename) == "" || filepath.Base(variant.Filename) != variant.Filename || strings.ContainsAny(variant.Filename, `/\\`) { return nil, "", 0, errors.New("artifact filename is unsafe") }
	if strings.TrimSpace(variant.MediaType) == "" { return nil, "", 0, errors.New("artifact media type is required") }
	limit := limits.MaxArtifactBytes
	if isVideoMediaTypeOrKind(variant.MediaType, variant.Presentation.Kind) { limit = limits.MaxVideoArtifactBytes }
	if len(body) == 0 || int64(len(body)) > limit { return nil, "", 0, ErrQuotaExceeded }
	if variant.MediaType == "application/zip" || variant.Presentation.Kind == "package" {
		if variant.MediaType != "application/zip" { return nil, "", 0, errors.New("artifact package has an invalid media type") }
		if _, _, err := readPackageBytes(limits, body, "", limits.MaxPackageEntryBytes); err != nil { return nil, "", 0, err }
	}
	if int64(len(body)) > limits.MaxSessionBytes { return nil, "", 0, ErrQuotaExceeded }
	copyBody := append([]byte(nil), body...)
	digest := sha256.Sum256(copyBody)
	return copyBody, hex.EncodeToString(digest[:]), int64(len(copyBody)), nil
}

func readArtifactFile(ctx context.Context, limits Limits, variant pebblestore.SessionArtifactVariant, sourcePath string) ([]byte, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(sourcePath)); if err != nil { return nil, err }
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute); if err != nil { return nil, fmt.Errorf("inspect artifact import: %w", err) }
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() { return nil, errors.New("artifact import source must be a regular non-symlink file") }
	resolved, err := filepath.EvalSymlinks(absolute); if err != nil || filepath.Clean(resolved) != absolute { return nil, errors.New("artifact import source path contains a symlink") }
	limit := normalizeLimits(limits).MaxArtifactBytes; if isVideoMediaTypeOrKind(variant.MediaType, variant.Presentation.Kind) { limit = normalizeLimits(limits).MaxVideoArtifactBytes }
	if info.Size() > limit { return nil, ErrQuotaExceeded }
	file, err := os.Open(absolute); if err != nil { return nil, err }; defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, limit+1)); if err != nil { return nil, err }
	if int64(len(body)) != info.Size() || int64(len(body)) > limit { return nil, errors.New("artifact import changed while reading") }
	return body, ctx.Err()
}

func canonicalPackageEntries(ctx context.Context, limits Limits, entries []PackageEntry) ([]byte, error) {
	limits = normalizeLimits(limits)
	if len(entries) == 0 || len(entries) > limits.MaxPackageFiles { return nil, ErrQuotaExceeded }
	copyEntries := append([]PackageEntry(nil), entries...)
	sort.Slice(copyEntries, func(i, j int) bool { return copyEntries[i].Name < copyEntries[j].Name })
	seen := map[string]bool{}; var expanded int64
	var out bytes.Buffer; archive := zip.NewWriter(&out)
	for _, item := range copyEntries {
		if err := ctx.Err(); err != nil { _ = archive.Close(); return nil, err }
		name := strings.TrimSpace(item.Name)
		if name == "" || name != item.Name || len(name) > 1024 || strings.Contains(name, "\\") || !safePackageEntryName(name) || seen[strings.ToLower(name)] { _ = archive.Close(); return nil, errors.New("artifact package contains an unsafe or duplicate entry") }
		seen[strings.ToLower(name)] = true; expanded += int64(len(item.Data))
		if int64(len(item.Data)) > limits.MaxPackageEntryBytes || expanded > limits.MaxPackageBytes { _ = archive.Close(); return nil, ErrQuotaExceeded }
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}; header.SetMode(0o600)
		writer, err := archive.CreateHeader(header); if err != nil { _ = archive.Close(); return nil, err }
		if _, err = writer.Write(item.Data); err != nil { _ = archive.Close(); return nil, err }
	}
	if err := archive.Close(); err != nil { return nil, err }
	if int64(out.Len()) > limits.MaxArtifactBytes { return nil, ErrQuotaExceeded }
	return out.Bytes(), nil
}

func packageDirectoryEntries(sourceDir string, limits Limits) ([]PackageEntry, error) {
	limits = normalizeLimits(limits); absolute, err := filepath.Abs(strings.TrimSpace(sourceDir)); if err != nil { return nil, err }; absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute); if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() { return nil, errors.New("artifact package source must be a non-symlink directory") }
	resolved, err := filepath.EvalSymlinks(absolute); if err != nil || filepath.Clean(resolved) != absolute { return nil, errors.New("artifact package directory contains a symlink") }
	entries := []PackageEntry{}; var total int64
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil { return walkErr }; if path == absolute || entry.IsDir() { return nil }
		if entry.Type()&os.ModeSymlink != 0 { return errors.New("artifact package contains a symlink") }
		item, err := entry.Info(); if err != nil || !item.Mode().IsRegular() { return errors.New("artifact package contains a non-regular file") }
		total += item.Size(); if len(entries)+1 > limits.MaxPackageFiles || item.Size() > limits.MaxPackageEntryBytes || total > limits.MaxPackageBytes { return ErrQuotaExceeded }
		rel, err := filepath.Rel(absolute, path); if err != nil || !safeRelativePath(rel) { return errors.New("artifact package entry escapes its directory") }
		file, err := os.Open(path); if err != nil { return err }; data, readErr := io.ReadAll(io.LimitReader(file, limits.MaxPackageEntryBytes+1)); closeErr := file.Close()
		if readErr != nil { return readErr }; if closeErr != nil { return closeErr }; if int64(len(data)) != item.Size() { return errors.New("artifact package file changed while reading") }
		entries = append(entries, PackageEntry{Name: filepath.ToSlash(rel), Data: data}); return nil
	})
	return entries, err
}

func readPackageBytes(limits Limits, body []byte, entryName string, maxBytes int64) ([]PackageManifestEntry, []byte, error) {
	limits = normalizeLimits(limits); if maxBytes <= 0 { return nil, nil, errors.New("artifact package read limit is required") }
	trimmed := strings.TrimSpace(entryName); if entryName != trimmed || (trimmed != "" && !safePackageEntryName(trimmed)) { return nil, nil, errors.New("artifact package entry name is unsafe") }
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); if err != nil { return nil, nil, errors.New("artifact package is not a valid zip archive") }
	if len(archive.File) == 0 || len(archive.File) > limits.MaxPackageFiles { return nil, nil, ErrQuotaExceeded }
	manifest := []PackageManifestEntry{}; names := map[string]bool{}; var selected *zip.File; var total int64
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() || entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() || !safePackageEntryName(entry.Name) { return nil, nil, errors.New("artifact package contains an unsafe entry") }
		folded := strings.ToLower(entry.Name); if names[folded] { return nil, nil, errors.New("artifact package contains duplicate or ambiguous entries") }; names[folded] = true
		size := int64(entry.UncompressedSize64); total += size; if size > limits.MaxPackageEntryBytes || total > limits.MaxPackageBytes { return nil, nil, ErrQuotaExceeded }
		manifest = append(manifest, PackageManifestEntry{Name: entry.Name, Size: size}); if entry.Name == trimmed { selected = entry }
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Name < manifest[j].Name })
	if trimmed == "" { return manifest, nil, nil }; if selected == nil { return nil, nil, fmt.Errorf("artifact package entry %q was not found", trimmed) }
	if int64(selected.UncompressedSize64) > maxBytes { return nil, nil, ErrQuotaExceeded }
	reader, err := selected.Open(); if err != nil { return nil, nil, err }; defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1)); if err != nil || int64(len(data)) > maxBytes || int64(len(data)) != int64(selected.UncompressedSize64) { return nil, nil, ErrQuotaExceeded }
	return nil, data, nil
}
