// Package artifact owns private, durable artifact bytes. Artifact metadata remains
// authoritative in the V3 Pebble session store; this package never places storage
// paths in session metadata or events.
package artifact

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/appstorage"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	DefaultMaxArtifactBytes      = int64(64 << 20)
	DefaultMaxVideoArtifactBytes = int64(512 << 20)
	DefaultMaxSessionBytes       = int64(2048 << 20)
	DefaultMaxPackageFiles       = 512
	DefaultMaxPackageEntryBytes  = int64(16 << 20)
	DefaultMaxPackageBytes       = int64(128 << 20)
)

var (
	ErrQuotaExceeded = errors.New("artifact storage quota exceeded")
	ErrNotReady      = errors.New("artifact bytes are not ready")
	ErrConflict      = errors.New("artifact variant already has different bytes")
)

// Limits bounds both stored bytes and expanded package input.
type Limits struct {
	MaxArtifactBytes      int64
	MaxVideoArtifactBytes int64
	MaxSessionBytes       int64
	MaxPackageFiles       int
	MaxPackageEntryBytes  int64
	MaxPackageBytes       int64
}

// Service is scoped to one workspace. Its root is app-owned storage, never the
// workspace or a repository checkout.
type Service struct {
	workspacePath string
	root          string
	limits        Limits
	mu            sync.Mutex
}

// Staged is an opaque handle to verified bytes awaiting atomic finalization.
// It deliberately exposes no filesystem path or storage key.
type Staged struct {
	SessionID    string
	CollectionID string
	VariantID    string
	Filename     string
	MediaType    string
	DigestSHA256 string
	Size         int64
	Presentation pebblestore.SessionArtifactPresentation

	token    string
	existing bool
}

// PackageEntry is one in-memory file in a managed package. Name is always a
// relative slash-delimited archive path; Data is copied before staging starts.
type PackageEntry struct {
	Name string
	Data []byte
}

// PackageManifestEntry is bounded public metadata for one regular package file.
// It contains no storage path and is returned in canonical archive-name order.
type PackageManifestEntry struct {
	Name string
	Size int64
}

// Blob describes finalized bytes without exposing their private location.
type Blob struct {
	SessionID    string
	CollectionID string
	VariantID    string
	Filename     string
	MediaType    string
	DigestSHA256 string
	Size         int64
	Presentation pebblestore.SessionArtifactPresentation
}

// ReconcileReport describes restart cleanup. Staging files are never promoted
// by reconciliation because their corresponding metadata transition is unknown.
type ReconcileReport struct {
	RemovedStaging int
	RemovedBytes   int64
}

// New creates a workspace-scoped artifact service under the canonical private
// daemon data root.
func New(workspacePath string, limits Limits) (*Service, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return nil, errors.New("workspace path is required")
	}
	root, err := appstorage.WorkspaceDataDir(workspacePath, "artifacts", "sessions")
	if err != nil {
		return nil, fmt.Errorf("resolve artifact storage root: %w", err)
	}
	if err := requireRealDirectory(root); err != nil {
		return nil, fmt.Errorf("validate artifact storage root: %w", err)
	}
	return &Service{workspacePath: workspacePath, root: root, limits: normalizeLimits(limits)}, nil
}

func normalizeLimits(l Limits) Limits {
	if l.MaxArtifactBytes <= 0 {
		l.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if l.MaxVideoArtifactBytes <= 0 {
		l.MaxVideoArtifactBytes = DefaultMaxVideoArtifactBytes
	}
	if l.MaxSessionBytes <= 0 {
		l.MaxSessionBytes = DefaultMaxSessionBytes
	}
	if l.MaxPackageFiles <= 0 {
		l.MaxPackageFiles = DefaultMaxPackageFiles
	}
	if l.MaxPackageEntryBytes <= 0 {
		l.MaxPackageEntryBytes = DefaultMaxPackageEntryBytes
	}
	if l.MaxPackageBytes <= 0 {
		l.MaxPackageBytes = DefaultMaxPackageBytes
	}
	return l
}

func (s *Service) maxArtifactLimit(mediaType, kind string) int64 {
	if isVideoMediaTypeOrKind(mediaType, kind) {
		return s.limits.MaxVideoArtifactBytes
	}
	return s.limits.MaxArtifactBytes
}

func isVideoMediaTypeOrKind(mediaType, kind string) bool {
	normMedia := strings.ToLower(strings.TrimSpace(mediaType))
	normKind := strings.ToLower(strings.TrimSpace(kind))
	return normKind == "video" || normMedia == "video/mp4" || strings.HasPrefix(normMedia, "video/")
}

// Stage writes and verifies one variant in a same-directory private staging
// file. The supplied variant is a metadata boundary, not a source of ownership
// or storage paths.
func (s *Service) Stage(ctx context.Context, variant pebblestore.SessionArtifactVariant, body io.Reader) (Staged, error) {
	if body == nil {
		return Staged{}, errors.New("artifact body is required")
	}
	staged, err := s.validateVariant(variant, false)
	if err != nil {
		return Staged{}, err
	}
	limit := s.maxArtifactLimit(staged.MediaType, staged.Presentation.Kind)
	return s.stage(ctx, staged, func(dst io.Writer) ([]byte, error) {
		return copyBounded(ctx, dst, body, limit)
	})
}

// ImportFile copies a regular, non-symlink file into managed storage.
func (s *Service) ImportFile(ctx context.Context, variant pebblestore.SessionArtifactVariant, sourcePath string) (Staged, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return Staged{}, fmt.Errorf("resolve artifact import: %w", err)
	}
	sourcePath = filepath.Clean(absolute)
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return Staged{}, fmt.Errorf("inspect artifact import: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Staged{}, errors.New("artifact import source must be a regular non-symlink file")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(sourcePath); resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(sourcePath) {
		return Staged{}, errors.New("artifact import source path contains a symlink")
	}
	limit := s.maxArtifactLimit(variant.MediaType, variant.Presentation.Kind)
	if info.Size() > limit {
		return Staged{}, ErrQuotaExceeded
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return Staged{}, fmt.Errorf("open artifact import: %w", err)
	}
	defer file.Close()
	return s.Stage(ctx, variant, file)
}

// StagePackage creates a bounded ZIP package directly from structured in-memory
// entries. It never materializes caller data in a workspace or scratch directory.
func (s *Service) StagePackage(ctx context.Context, variant pebblestore.SessionArtifactVariant, entries []PackageEntry) (Staged, error) {
	if err := ctx.Err(); err != nil {
		return Staged{}, err
	}
	staged, err := s.validateVariant(variant, true)
	if err != nil {
		return Staged{}, err
	}
	prepared, err := s.preparePackageEntries(entries)
	if err != nil {
		return Staged{}, err
	}
	return s.stage(ctx, staged, func(dst io.Writer) ([]byte, error) {
		return s.writePackage(ctx, dst, prepared)
	})
}

// ImportPackage creates a bounded ZIP package from a directory. Every input
// path is checked for traversal, symlinks, special files, entry count, and
// expanded byte quota before any package is finalized.
func (s *Service) ImportPackage(ctx context.Context, variant pebblestore.SessionArtifactVariant, sourceDir string) (Staged, error) {
	staged, err := s.validateVariant(variant, true)
	if err != nil {
		return Staged{}, err
	}
	files, err := s.packageFiles(sourceDir)
	if err != nil {
		return Staged{}, err
	}
	return s.stage(ctx, staged, func(dst io.Writer) ([]byte, error) {
		hash := sha256.New()
		counting := &boundedWriter{writer: io.MultiWriter(dst, hash), limit: s.limits.MaxArtifactBytes}
		archive := zip.NewWriter(counting)
		for _, item := range files {
			if err := ctx.Err(); err != nil {
				_ = archive.Close()
				return nil, err
			}
			header, err := zip.FileInfoHeader(item.info)
			if err != nil {
				_ = archive.Close()
				return nil, err
			}
			header.Name = filepath.ToSlash(item.relative)
			header.Method = zip.Deflate
			header.SetMode(0o600)
			entry, err := archive.CreateHeader(header)
			if err != nil {
				_ = archive.Close()
				return nil, err
			}
			currentInfo, err := os.Lstat(item.path)
			if err != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || currentInfo.Size() != item.info.Size() {
				_ = archive.Close()
				return nil, errors.New("artifact package file changed or became unsafe")
			}
			file, err := os.Open(item.path)
			if err != nil {
				_ = archive.Close()
				return nil, err
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || openedInfo == nil || !openedInfo.Mode().IsRegular() || !os.SameFile(currentInfo, openedInfo) || !os.SameFile(item.info, openedInfo) {
				_ = file.Close()
				_ = archive.Close()
				return nil, errors.New("artifact package file changed or became unsafe")
			}
			_, copyErr := io.Copy(entry, file)
			closeErr := file.Close()
			if copyErr != nil {
				_ = archive.Close()
				return nil, copyErr
			}
			if closeErr != nil {
				_ = archive.Close()
				return nil, closeErr
			}
		}
		if err := archive.Close(); err != nil {
			return nil, err
		}
		return hash.Sum(nil), nil
	})
}

type preparedPackageEntry struct {
	name string
	data []byte
}

func (s *Service) preparePackageEntries(entries []PackageEntry) ([]preparedPackageEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("artifact package is empty")
	}
	if len(entries) > s.limits.MaxPackageFiles {
		return nil, ErrQuotaExceeded
	}
	prepared := make([]preparedPackageEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	var total int64
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" || len(name) > 1024 || name != entry.Name || strings.Contains(name, "\\") || !safePackageEntryName(name) {
			return nil, errors.New("artifact package contains an unsafe entry")
		}
		if _, ok := seen[name]; ok {
			return nil, errors.New("artifact package contains duplicate entries")
		}
		seen[name] = struct{}{}
		size := int64(len(entry.Data))
		if size > s.limits.MaxPackageEntryBytes || size > s.limits.MaxPackageBytes-total {
			return nil, ErrQuotaExceeded
		}
		total += size
		prepared = append(prepared, preparedPackageEntry{name: name, data: append([]byte(nil), entry.Data...)})
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].name < prepared[j].name })
	return prepared, nil
}

func (s *Service) writePackage(ctx context.Context, dst io.Writer, entries []preparedPackageEntry) ([]byte, error) {
	hash := sha256.New()
	counting := &boundedWriter{writer: io.MultiWriter(dst, hash), limit: s.limits.MaxArtifactBytes}
	archive := zip.NewWriter(counting)
	for _, item := range entries {
		if err := ctx.Err(); err != nil {
			_ = archive.Close()
			return nil, err
		}
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err := entry.Write(item.data); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

type packageFile struct {
	path     string
	relative string
	info     os.FileInfo
}

func (s *Service) packageFiles(sourceDir string) ([]packageFile, error) {
	sourceDir = filepath.Clean(strings.TrimSpace(sourceDir))
	if sourceDir == "." || sourceDir == "" {
		return nil, errors.New("artifact package directory is required")
	}
	absolute, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, err
	}
	sourceDir = filepath.Clean(absolute)
	info, err := os.Lstat(sourceDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("artifact package source must be a non-symlink directory")
	}
	resolved, err := filepath.EvalSymlinks(sourceDir)
	if err != nil || filepath.Clean(resolved) != sourceDir {
		return nil, errors.New("artifact package directory contains a symlink")
	}
	files := make([]packageFile, 0, 16)
	var total int64
	err = filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact package contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		itemInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !itemInfo.Mode().IsRegular() {
			return errors.New("artifact package contains a non-regular file")
		}
		if itemInfo.Size() > s.limits.MaxPackageEntryBytes {
			return ErrQuotaExceeded
		}
		current, err := os.Open(path)
		if err != nil {
			return err
		}
		currentInfo, statErr := current.Stat()
		closeErr := current.Close()
		if statErr != nil || closeErr != nil || currentInfo == nil || !os.SameFile(itemInfo, currentInfo) || !currentInfo.Mode().IsRegular() {
			return errors.New("artifact package file changed or became unsafe")
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil || !safeRelativePath(relative) {
			return errors.New("artifact package entry escapes its directory")
		}
		total += itemInfo.Size()
		if itemInfo.Size() > s.limits.MaxPackageEntryBytes || len(files)+1 > s.limits.MaxPackageFiles || total > s.limits.MaxPackageBytes {
			return ErrQuotaExceeded
		}
		files = append(files, packageFile{path: path, relative: relative, info: itemInfo})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("artifact package is empty")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func (s *Service) stage(ctx context.Context, staged Staged, write func(io.Writer) ([]byte, error)) (Staged, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Staged{}, err
	}
	dir, err := s.variantDir(staged.SessionID, staged.CollectionID, staged.VariantID, true)
	if err != nil {
		return Staged{}, err
	}
	usage, err := sessionUsage(s.sessionDir(staged.SessionID))
	if err != nil {
		return Staged{}, err
	}
	finalPath := filepath.Join(dir, "content")
	if finalInfo, finalErr := os.Lstat(finalPath); finalErr == nil && finalInfo.Mode().IsRegular() {
		usage -= finalInfo.Size()
	}
	token, err := randomToken()
	if err != nil {
		return Staged{}, err
	}
	stagePath := filepath.Join(dir, ".stage-"+token)
	file, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, appstorage.PrivateFilePerm)
	if err != nil {
		return Staged{}, fmt.Errorf("create artifact staging file: %w", err)
	}
	hashBytes, writeErr := write(file)
	if syncErr := file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(stagePath)
		if errors.Is(writeErr, ErrQuotaExceeded) {
			return Staged{}, writeErr
		}
		return Staged{}, fmt.Errorf("stage artifact bytes: %w", writeErr)
	}
	info, err := os.Lstat(stagePath)
	if err != nil || !info.Mode().IsRegular() {
		_ = os.Remove(stagePath)
		return Staged{}, errors.New("artifact staging file is not regular")
	}
	if err := os.Chmod(stagePath, appstorage.PrivateFilePerm); err != nil {
		_ = os.Remove(stagePath)
		return Staged{}, err
	}
	staged.Size = info.Size()
	limit := s.maxArtifactLimit(staged.MediaType, staged.Presentation.Kind)
	if staged.Size <= 0 || staged.Size > limit || usage > s.limits.MaxSessionBytes-staged.Size {
		_ = os.Remove(stagePath)
		return Staged{}, ErrQuotaExceeded
	}
	if len(hashBytes) == 0 {
		hashBytes, err = digestFile(stagePath)
		if err != nil {
			_ = os.Remove(stagePath)
			return Staged{}, err
		}
	}
	staged.DigestSHA256 = hex.EncodeToString(hashBytes)
	if staged.MediaType == "application/zip" {
		if err := s.validateZip(stagePath); err != nil {
			_ = os.Remove(stagePath)
			return Staged{}, err
		}
	} else {
		mediaType, presentation, err := validateContentType(stagePath, staged.MediaType, staged.Presentation)
		if err != nil {
			_ = os.Remove(stagePath)
			return Staged{}, err
		}
		staged.MediaType = mediaType
		staged.Presentation = presentation
	}
	if finalInfo, err := os.Lstat(finalPath); err == nil {
		if !finalInfo.Mode().IsRegular() {
			_ = os.Remove(stagePath)
			return Staged{}, errors.New("artifact final path is not a regular file")
		}
		digest, digestErr := digestFile(finalPath)
		if digestErr != nil {
			_ = os.Remove(stagePath)
			return Staged{}, digestErr
		}
		if finalInfo.Size() != staged.Size || hex.EncodeToString(digest) != staged.DigestSHA256 {
			_ = os.Remove(stagePath)
			return Staged{}, ErrConflict
		}
		_ = os.Remove(stagePath)
		staged.existing = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(stagePath)
		return Staged{}, err
	}
	staged.token = token
	return staged, nil
}

// Finalize verifies caller expectations and atomically renames staged bytes to
// their immutable final name. Repeating the same content is idempotent.
func (s *Service) Finalize(ctx context.Context, staged Staged, expectedDigest string, expectedSize int64) (Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Blob{}, err
	}
	if err := validateID("session", staged.SessionID); err != nil {
		return Blob{}, err
	}
	if err := validateID("collection", staged.CollectionID); err != nil {
		return Blob{}, err
	}
	if err := validateID("variant", staged.VariantID); err != nil {
		return Blob{}, err
	}
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	limit := s.maxArtifactLimit(staged.MediaType, staged.Presentation.Kind)
	if !validDigest(staged.DigestSHA256) || staged.Size <= 0 || staged.Size > limit {
		return Blob{}, errors.New("artifact staging handle has invalid digest or size")
	}
	if expectedDigest != "" && (!validDigest(expectedDigest) || expectedDigest != staged.DigestSHA256) {
		return Blob{}, errors.New("artifact digest does not match finalization expectation")
	}
	if expectedSize > 0 && expectedSize != staged.Size {
		return Blob{}, errors.New("artifact size does not match finalization expectation")
	}
	dir, err := s.variantDir(staged.SessionID, staged.CollectionID, staged.VariantID, false)
	if err != nil {
		return Blob{}, err
	}
	finalPath := filepath.Join(dir, "content")
	if !staged.existing {
		if staged.token == "" {
			return Blob{}, errors.New("artifact staging handle is invalid")
		}
		stagePath := filepath.Join(dir, ".stage-"+staged.token)
		if err := requireRegularFile(stagePath); err != nil {
			return Blob{}, ErrNotReady
		}
		if err := verifyFile(stagePath, staged.DigestSHA256, staged.Size); err != nil {
			return Blob{}, err
		}
		if _, err := os.Lstat(finalPath); err == nil {
			blob := stagedBlob(staged)
			if err := verifyFile(finalPath, blob.DigestSHA256, blob.Size); err != nil {
				return Blob{}, ErrConflict
			}
			_ = os.Remove(stagePath)
			return blob, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return Blob{}, err
		}
		if err := os.Rename(stagePath, finalPath); err != nil {
			return Blob{}, fmt.Errorf("finalize artifact bytes: %w", err)
		}
		if err := os.Chmod(finalPath, appstorage.PrivateFilePerm); err != nil {
			return Blob{}, err
		}
	}
	blob := stagedBlob(staged)
	if err := verifyFile(finalPath, blob.DigestSHA256, blob.Size); err != nil {
		return Blob{}, err
	}
	return blob, nil
}

// Open verifies trusted ready metadata against private bytes before returning a
// seekable file. Callers own the returned file.
func (s *Service) Open(ctx context.Context, variant pebblestore.SessionArtifactVariant) (*os.File, Blob, error) {
	if err := ctx.Err(); err != nil {
		return nil, Blob{}, err
	}
	staged, err := s.validateVariant(variant, variant.MediaType == "application/zip")
	if err != nil {
		return nil, Blob{}, err
	}
	if variant.Status != pebblestore.SessionArtifactStatusReady || !validDigest(variant.DigestSHA256) || variant.Size <= 0 {
		return nil, Blob{}, ErrNotReady
	}
	dir, err := s.variantDir(variant.SessionID, variant.CollectionID, variant.ID, false)
	if err != nil {
		return nil, Blob{}, err
	}
	path := filepath.Join(dir, "content")
	if err := verifyFile(path, strings.ToLower(variant.DigestSHA256), variant.Size); err != nil {
		return nil, Blob{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Blob{}, err
	}
	staged.DigestSHA256 = strings.ToLower(variant.DigestSHA256)
	staged.Size = variant.Size
	return file, stagedBlob(staged), nil
}

// Read returns verified bytes up to maxBytes.
func (s *Service) Read(ctx context.Context, variant pebblestore.SessionArtifactVariant, maxBytes int64) ([]byte, Blob, error) {
	limit := s.maxArtifactLimit(variant.MediaType, variant.Presentation.Kind)
	if maxBytes <= 0 || maxBytes > limit {
		maxBytes = limit
	}
	file, blob, err := s.Open(ctx, variant)
	if err != nil {
		return nil, Blob{}, err
	}
	defer file.Close()
	if blob.Size > maxBytes {
		return nil, Blob{}, ErrQuotaExceeded
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, Blob{}, err
	}
	if int64(len(data)) != blob.Size {
		return nil, Blob{}, errors.New("artifact bytes changed while reading")
	}
	return data, blob, nil
}

// ReadPackage inspects a verified ready ZIP without extracting it. An empty
// entry name returns a bounded manifest; a non-empty name returns exactly one
// regular entry after validating the entire archive.
func (s *Service) ReadPackage(ctx context.Context, variant pebblestore.SessionArtifactVariant, entryName string, maxBytes int64) ([]PackageManifestEntry, []byte, Blob, error) {
	if s == nil {
		return nil, nil, Blob{}, errors.New("artifact service is not configured")
	}
	if strings.ToLower(strings.TrimSpace(strings.SplitN(variant.MediaType, ";", 2)[0])) != "application/zip" {
		return nil, nil, Blob{}, errors.New("artifact is not an application/zip package")
	}
	trimmedEntryName := strings.TrimSpace(entryName)
	if entryName != trimmedEntryName {
		return nil, nil, Blob{}, errors.New("artifact package entry name is unsafe")
	}
	entryName = trimmedEntryName
	if entryName != "" && (len(entryName) > 1024 || strings.Contains(entryName, "\\") || !safePackageEntryName(entryName)) {
		return nil, nil, Blob{}, errors.New("artifact package entry name is unsafe")
	}
	if maxBytes <= 0 {
		return nil, nil, Blob{}, errors.New("artifact package read limit is required")
	}
	if maxBytes > s.limits.MaxPackageEntryBytes {
		maxBytes = s.limits.MaxPackageEntryBytes
	}
	file, blob, err := s.Open(ctx, variant)
	if err != nil {
		return nil, nil, Blob{}, err
	}
	defer file.Close()
	archive, err := zip.NewReader(file, blob.Size)
	if err != nil {
		return nil, nil, Blob{}, errors.New("artifact package is not a valid zip archive")
	}
	entries, _, err := s.materializePackageEntries(archive.File)
	if err != nil {
		return nil, nil, Blob{}, err
	}
	manifest := make([]PackageManifestEntry, 0, len(entries))
	var selected *zip.File
	for _, entry := range entries {
		size := int64(entry.UncompressedSize64)
		manifest = append(manifest, PackageManifestEntry{Name: entry.Name, Size: size})
		if entry.Name == entryName {
			selected = entry
		}
	}
	if entryName == "" {
		return manifest, nil, blob, nil
	}
	if selected == nil {
		return nil, nil, Blob{}, fmt.Errorf("artifact package entry %q was not found", entryName)
	}
	if int64(selected.UncompressedSize64) > maxBytes {
		return nil, nil, Blob{}, ErrQuotaExceeded
	}
	reader, err := selected.Open()
	if err != nil {
		return nil, nil, Blob{}, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, nil, Blob{}, err
	}
	if int64(len(data)) != int64(selected.UncompressedSize64) || int64(len(data)) > maxBytes {
		return nil, nil, Blob{}, ErrQuotaExceeded
	}
	return nil, data, blob, nil
}

// Reconcile removes all incomplete staging files for a session. It never claims
// that staging bytes are ready after a restart.
func (s *Service) Reconcile(sessionID string) (ReconcileReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID("session", sessionID); err != nil {
		return ReconcileReport{}, err
	}
	root := s.sessionDir(sessionID)
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return ReconcileReport{}, nil
	} else if err != nil {
		return ReconcileReport{}, err
	}
	var report ReconcileReport
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact storage contains a symlink")
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".stage-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact staging entry is not regular")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		report.RemovedStaging++
		report.RemovedBytes += info.Size()
		return nil
	})
	return report, err
}

// DeleteVariant removes only one verified private variant directory. Missing
// variants are idempotent; unsafe directory chains fail closed.
func (s *Service) DeleteVariant(sessionID, collectionID, variantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID("session", sessionID); err != nil {
		return err
	}
	if err := validateID("collection", collectionID); err != nil {
		return err
	}
	if err := validateID("variant", variantID); err != nil {
		return err
	}
	path := filepath.Join(s.collectionDir(sessionID, collectionID), opaqueKey("variant", variantID))
	return s.deleteOwnedDirectory(path)
}

// DeleteCollection removes every private variant in one verified collection.
// Missing collections are idempotent; other collections remain untouched.
func (s *Service) DeleteCollection(sessionID, collectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID("session", sessionID); err != nil {
		return err
	}
	if err := validateID("collection", collectionID); err != nil {
		return err
	}
	return s.deleteOwnedDirectory(s.collectionDir(sessionID, collectionID))
}

func (s *Service) deleteOwnedDirectory(path string) error {
	exists, err := requireDirectoryChainIfPresent(s.root, path)
	if err != nil || !exists {
		return err
	}
	return removeDirectoryTree(path)
}

// DeleteSession removes every byte owned by one session.
func (s *Service) DeleteSession(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateID("session", sessionID); err != nil {
		return err
	}
	root := s.sessionDir(sessionID)
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact session path is not a real directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
	return removeDirectoryTree(root)
}

func removeDirectoryTree(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact storage contains a symlink")
		}
		if info.IsDir() {
			if err := removeDirectoryTree(path); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact storage contains a non-regular file")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.Remove(root)
}

func (s *Service) validateVariant(variant pebblestore.SessionArtifactVariant, packageArtifact bool) (Staged, error) {
	if strings.TrimSpace(variant.AccountScopeID) == "" {
		return Staged{}, errors.New("artifact account scope is required")
	}
	if err := validateID("session", variant.SessionID); err != nil {
		return Staged{}, err
	}
	if err := validateID("collection", variant.CollectionID); err != nil {
		return Staged{}, err
	}
	if err := validateID("variant", variant.ID); err != nil {
		return Staged{}, err
	}
	filename := strings.TrimSpace(variant.Filename)
	if filename == "" || filename != filepath.Base(filename) || strings.ContainsAny(filename, `/\\`) || len(filename) > 255 {
		return Staged{}, errors.New("artifact filename must be a bounded basename")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(variant.MediaType))
	if err != nil || mediaType == "" {
		mediaType = "application/octet-stream"
	}
	mediaType = strings.ToLower(mediaType)
	presentation := variant.Presentation
	presentation.Kind = strings.ToLower(strings.TrimSpace(presentation.Kind))
	if len(presentation.Kind) > 64 || len(presentation.Label) > 256 || len(presentation.Description) > 2048 {
		return Staged{}, errors.New("artifact presentation metadata exceeds bounds")
	}
	if packageArtifact {
		mediaType = "application/zip"
		if presentation.Kind == "" {
			presentation.Kind = "package"
		}
	} else if presentation.Kind == "" && (mediaType == "video/mp4" || strings.HasPrefix(mediaType, "video/")) {
		presentation.Kind = "video"
	}
	if err := validatePresentation(mediaType, presentation); err != nil {
		return Staged{}, err
	}
	return Staged{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, Filename: filename, MediaType: mediaType, Presentation: presentation}, nil
}

func validatePresentation(mediaType string, p pebblestore.SessionArtifactPresentation) error {
	allowed := map[string]bool{"": true, "download": true, "text": true, "code": true, "image": true, "html": true, "package": true, "video": true}
	if !allowed[p.Kind] {
		return errors.New("artifact presentation kind is unsupported")
	}
	compatible := p.Kind == "" || p.Kind == "download" ||
		(p.Kind == "image" && strings.HasPrefix(mediaType, "image/")) ||
		((p.Kind == "text" || p.Kind == "code") && (strings.HasPrefix(mediaType, "text/") || mediaType == "application/json")) ||
		(p.Kind == "html" && mediaType == "text/html") ||
		(p.Kind == "package" && mediaType == "application/zip") ||
		(p.Kind == "video" && (mediaType == "video/mp4" || strings.HasPrefix(mediaType, "video/")))
	if !compatible {
		return errors.New("artifact presentation is incompatible with its media type")
	}
	if p.Previewable && (p.Kind == "download" || mediaType == "application/octet-stream" || mediaType == "application/zip") {
		return errors.New("artifact media type is not safely previewable")
	}
	return nil
}

func isMP4Sample(sample []byte) bool {
	if len(sample) < 8 {
		return false
	}
	return string(sample[4:8]) == "ftyp"
}

func validateContentType(path, declared string, presentation pebblestore.SessionArtifactPresentation) (string, pebblestore.SessionArtifactPresentation, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", presentation, err
	}
	var sample [512]byte
	n, readErr := io.ReadFull(file, sample[:])
	_ = file.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return "", presentation, readErr
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(sample[:n]))
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared == "" {
		if isMP4Sample(sample[:n]) {
			declared = "video/mp4"
		} else {
			declared = detected
		}
	}
	if declared == "application/octet-stream" || !previewSafeMediaType(declared) {
		presentation.Kind = "download"
		presentation.Previewable = false
		return declared, presentation, nil
	}
	if declared == "image/svg+xml" {
		if err := validateSVGDocument(path); err != nil {
			return "", presentation, err
		}
	} else if strings.HasPrefix(declared, "image/") && detected != declared {
		return "", presentation, errors.New("artifact image bytes do not match declared media type")
	} else if declared == "video/mp4" && detected != "video/mp4" && !isMP4Sample(sample[:n]) {
		return "", presentation, errors.New("artifact video bytes do not match declared media type")
	}
	if declared == "text/html" && detected != "text/html" && detected != "text/plain" {
		return "", presentation, errors.New("artifact HTML bytes do not match declared media type")
	}
	if (declared == "video/mp4" || strings.HasPrefix(declared, "video/")) && presentation.Kind == "" {
		presentation.Kind = "video"
		presentation.Previewable = true
	}
	if err := validatePresentation(declared, presentation); err != nil {
		return "", presentation, err
	}
	return declared, presentation, nil
}

func previewSafeMediaType(mediaType string) bool {
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml", "video/mp4":
		return true
	default:
		return false
	}
}

func validateSVGDocument(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := xml.NewDecoder(file)
	for {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("artifact SVG is not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "svg" || (start.Name.Space != "" && start.Name.Space != "http://www.w3.org/2000/svg") {
			return errors.New("artifact SVG must have an svg root element")
		}
		return nil
	}
}

func (s *Service) validateZip(path string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return errors.New("artifact package is not a valid zip archive")
	}
	defer archive.Close()
	_, _, err = s.materializePackageEntries(archive.File)
	return err
}

func (s *Service) variantDir(sessionID, collectionID, variantID string, create bool) (string, error) {
	if err := validateID("session", sessionID); err != nil {
		return "", err
	}
	if err := validateID("collection", collectionID); err != nil {
		return "", err
	}
	if err := validateID("variant", variantID); err != nil {
		return "", err
	}
	variantKey := opaqueKey("variant", variantID)
	path := filepath.Join(s.collectionDir(sessionID, collectionID), variantKey)
	if create {
		if err := mkdirPrivateUnder(s.root, path); err != nil {
			return "", err
		}
	} else if err := requireDirectoryChain(s.root, path); err != nil {
		return "", ErrNotReady
	}
	return path, nil
}

func (s *Service) collectionDir(sessionID, collectionID string) string {
	// Callers validate both IDs before using this path for storage operations.
	return filepath.Join(s.sessionDir(sessionID), "variants", opaqueKey("collection", collectionID))
}

func (s *Service) sessionDir(sessionID string) string {
	return filepath.Join(s.root, sessionID)
}

func opaqueKey(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + value))
	return hex.EncodeToString(sum[:20])
}

func validateID(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return fmt.Errorf("artifact %s id is invalid", label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return fmt.Errorf("artifact %s id contains unsupported characters", label)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safePackageEntryName(value string) bool {
	if !safeRelativePath(filepath.FromSlash(value)) || strings.HasSuffix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value
}

func safeRelativePath(value string) bool {
	if value == "" || value == "." || filepath.IsAbs(value) {
		return false
	}
	clean := filepath.Clean(value)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func mkdirPrivateUnder(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	if err != nil || !safeRelativePath(relative) {
		return errors.New("artifact storage path escapes its private root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, appstorage.PrivateDirPerm); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact storage path contains a symlink or non-directory")
		}
		if err := os.Chmod(current, appstorage.PrivateDirPerm); err != nil {
			return err
		}
	}
	return nil
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path is not a real directory")
	}
	return nil
}

func requireDirectoryChain(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	if err != nil || (!safeRelativePath(relative) && relative != ".") {
		return errors.New("artifact storage path escapes its private root")
	}
	if err := requireRealDirectory(root); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := requireRealDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func requireDirectoryChainIfPresent(root, target string) (bool, error) {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	relative, err := filepath.Rel(root, target)
	if err != nil || (!safeRelativePath(relative) && relative != ".") {
		return false, errors.New("artifact storage path escapes its private root")
	}
	current := root
	if err := requireRealDirectory(current); err != nil {
		return false, err
	}
	if relative == "." {
		return true, nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, errors.New("artifact storage path contains a symlink or non-directory")
		}
	}
	return true, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}

func sessionUsage(root string) (int64, error) {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("artifact storage contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("artifact storage contains a non-regular file")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func verifyFile(path, expectedDigest string, expectedSize int64) error {
	if err := requireRegularFile(path); err != nil {
		return ErrNotReady
	}
	info, err := os.Lstat(path)
	if err != nil || info.Size() != expectedSize {
		return errors.New("artifact byte size does not match metadata")
	}
	digest, err := digestFile(path)
	if err != nil {
		return err
	}
	if hex.EncodeToString(digest) != strings.ToLower(expectedDigest) {
		return errors.New("artifact byte digest does not match metadata")
	}
	return nil
}

func digestFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func copyBounded(ctx context.Context, dst io.Writer, src io.Reader, limit int64) ([]byte, error) {
	hash := sha256.New()
	writer := &boundedWriter{writer: io.MultiWriter(dst, hash), limit: limit}
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if _, err := writer.Write(buffer[:n]); err != nil {
				return nil, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return hash.Sum(nil), nil
}

type boundedWriter struct {
	writer io.Writer
	limit  int64
	wrote  int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-w.wrote {
		return 0, ErrQuotaExceeded
	}
	n, err := w.writer.Write(p)
	w.wrote += int64(n)
	return n, err
}

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func stagedBlob(staged Staged) Blob {
	return Blob{SessionID: staged.SessionID, CollectionID: staged.CollectionID, VariantID: staged.VariantID, Filename: staged.Filename, MediaType: staged.MediaType, DigestSHA256: staged.DigestSHA256, Size: staged.Size, Presentation: staged.Presentation}
}
