package artifact

import (
	"archive/zip"
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

	"swarm/packages/swarmd/internal/appstorage"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var ErrDestinationConflict = errors.New("artifact materialization destination already exists")

// Materialized describes an explicit copy from managed storage into a trusted
// workspace root. It deliberately contains no managed backing path.
type Materialized struct {
	Destination  string
	Package      bool
	Files        int
	Bytes        int64
	DigestSHA256 string
	MediaType    string
}

// BatchMaterializeInput is one authenticated ready source in an atomic
// destination-directory publication. Source is resolved by Authority and never
// comes from a model-authored path.
type BatchMaterializeInput struct {
	Service *Service
	Variant pebblestore.SessionArtifactVariant
}

const MaxMaterializeBatchItems = 64

// Materialize copies one verified ready artifact into an explicitly supplied
// workspace-relative destination. ZIP packages are safely expanded into a
// destination directory; other artifacts are copied to one destination file.
func (s *Service) Materialize(ctx context.Context, variant pebblestore.SessionArtifactVariant, workspaceRoot, destination string, overwrite bool) (Materialized, error) {
	if err := ctx.Err(); err != nil {
		return Materialized{}, err
	}
	workspaceRoot, destination, err := validateMaterializeDestination(workspaceRoot, destination)
	if err != nil {
		return Materialized{}, err
	}
	file, blob, err := s.Open(ctx, variant)
	if err != nil {
		return Materialized{}, err
	}
	defer file.Close()

	if blob.MediaType == "application/zip" || blob.Presentation.Kind == "package" {
		if blob.MediaType != "application/zip" {
			return Materialized{}, errors.New("artifact package has an invalid media type")
		}
		return s.materializePackage(ctx, file, blob, workspaceRoot, destination, overwrite)
	}
	if err := materializeWorkspaceFile(ctx, workspaceRoot, destination, file, blob.Size, blob.DigestSHA256, s.maxArtifactLimit(blob.MediaType, blob.Presentation.Kind), overwrite); err != nil {
		return Materialized{}, err
	}
	return Materialized{Destination: filepath.ToSlash(destination), Files: 1, Bytes: blob.Size, DigestSHA256: blob.DigestSHA256, MediaType: blob.MediaType}, nil
}

// MaterializeBatch preflights every source and derived destination, writes all
// outputs beneath one private sibling staging directory, and publishes that
// directory with one rename. A failed preflight or staging step leaves no final
// destination and never reports partial success.
func MaterializeBatch(ctx context.Context, inputs []BatchMaterializeInput, workspaceRoot, destination string, overwrite bool) ([]Materialized, error) {
	if len(inputs) == 0 || len(inputs) > MaxMaterializeBatchItems {
		return nil, fmt.Errorf("artifact materialization batch must contain 1 to %d items", MaxMaterializeBatchItems)
	}
	workspaceRoot, destination, err := validateMaterializeDestination(workspaceRoot, destination)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(destination)
	if parent == "." {
		parent = ""
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, parent, true); err != nil {
		return nil, err
	}
	target := filepath.Join(workspaceRoot, destination)
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("artifact materialization batch destination is not a real directory")
		}
		if !overwrite {
			return nil, ErrDestinationConflict
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	type prepared struct {
		input    BatchMaterializeInput
		relative string
	}
	items := make([]prepared, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Service == nil {
			return nil, errors.New("artifact materialization batch source service is not configured")
		}
		filename := strings.TrimSpace(input.Variant.Filename)
		if filename == "" || filename != filepath.Base(filename) || strings.ContainsAny(filename, `/\\`) || len(filename) > 255 {
			return nil, errors.New("artifact materialization batch source filename is unsafe")
		}
		packageItem := input.Variant.MediaType == "application/zip" || input.Variant.Presentation.Kind == "package"
		relative := filename
		if packageItem {
			if input.Variant.MediaType != "application/zip" {
				return nil, errors.New("artifact materialization batch package has an invalid media type")
			}
			relative = strings.TrimSuffix(filename, filepath.Ext(filename))
			if relative == "" || relative == "." {
				return nil, errors.New("artifact materialization batch package filename cannot derive a safe directory")
			}
		}
		file, blob, err := input.Service.Open(ctx, input.Variant)
		if err != nil {
			return nil, fmt.Errorf("preflight artifact materialization batch source: %w", err)
		}
		if packageItem {
			archive, zipErr := zip.NewReader(file, blob.Size)
			if zipErr != nil {
				_ = file.Close()
				return nil, errors.New("artifact package is not a valid zip archive")
			}
			if _, _, zipErr = input.Service.materializePackageEntries(archive.File); zipErr != nil {
				_ = file.Close()
				return nil, zipErr
			}
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close preflight artifact materialization batch source: %w", err)
		}
		baseRelative := relative
		for suffix := 2; ; suffix++ {
			folded := strings.ToLower(relative)
			if _, exists := seen[folded]; !exists {
				seen[folded] = struct{}{}
				break
			}
			extension := filepath.Ext(baseRelative)
			stem := strings.TrimSuffix(baseRelative, extension)
			relative = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		}
		items = append(items, prepared{input: input, relative: relative})
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	stagePath := filepath.Join(filepath.Dir(target), ".swarm-artifact-batch-"+token)
	backupPath := filepath.Join(filepath.Dir(target), ".swarm-artifact-backup-"+token)
	if err := os.Mkdir(stagePath, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact batch staging directory: %w", err)
	}
	defer func() { _ = removeDirectoryTreeIfPresent(stagePath) }()

	results := make([]Materialized, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		materialized, err := item.input.Service.Materialize(ctx, item.input.Variant, stagePath, item.relative, false)
		if err != nil {
			return nil, fmt.Errorf("stage artifact batch destination %q: %w", item.relative, err)
		}
		materialized.Destination = filepath.ToSlash(filepath.Join(destination, item.relative))
		results = append(results, materialized)
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, parent, false); err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		if !overwrite {
			return nil, ErrDestinationConflict
		}
		if err := os.Rename(target, backupPath); err != nil {
			return nil, fmt.Errorf("stage existing artifact batch destination: %w", err)
		}
		if err := os.Rename(stagePath, target); err != nil {
			if rollbackErr := os.Rename(backupPath, target); rollbackErr != nil {
				return nil, fmt.Errorf("publish artifact batch: %v; rollback existing destination: %w", err, rollbackErr)
			}
			return nil, fmt.Errorf("publish artifact batch: %w", err)
		}
		if err := removeDirectoryTreeIfPresent(backupPath); err != nil {
			removeErr := removeDirectoryTreeIfPresent(target)
			rollbackErr := os.Rename(backupPath, target)
			if removeErr != nil || rollbackErr != nil {
				return nil, fmt.Errorf("remove replaced artifact batch destination: %v; remove new destination: %v; rollback existing destination: %v", err, removeErr, rollbackErr)
			}
			return nil, fmt.Errorf("remove replaced artifact batch destination: %w", err)
		}
		return results, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := os.Rename(stagePath, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return nil, ErrDestinationConflict
		}
		return nil, fmt.Errorf("publish artifact batch: %w", err)
	}
	return results, nil
}

func validateMaterializeDestination(workspaceRoot, destination string) (string, string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", "", errors.New("artifact materialization workspace root is required")
	}
	absolute, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve artifact materialization workspace: %w", err)
	}
	workspaceRoot = filepath.Clean(absolute)
	if err := requireRealDirectory(workspaceRoot); err != nil {
		return "", "", errors.New("artifact materialization workspace root is not a real directory")
	}

	destination = strings.TrimSpace(destination)
	if destination == "" || filepath.IsAbs(destination) || strings.Contains(destination, "\\") || filepath.Clean(destination) != destination || !safeRelativePath(destination) {
		return "", "", errors.New("artifact materialization destination must be a canonical workspace-relative path")
	}
	for _, part := range strings.Split(filepath.ToSlash(destination), "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("artifact materialization destination must be a canonical workspace-relative path")
		}
	}
	return workspaceRoot, destination, nil
}

func (s *Service) materializePackage(ctx context.Context, source *os.File, blob Blob, workspaceRoot, destination string, overwrite bool) (result Materialized, resultErr error) {
	archive, err := zip.NewReader(source, blob.Size)
	if err != nil {
		return Materialized{}, errors.New("artifact package is not a valid zip archive")
	}
	entries, total, err := s.materializePackageEntries(archive.File)
	if err != nil {
		return Materialized{}, err
	}

	parent := filepath.Dir(destination)
	if parent == "." {
		parent = ""
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, parent, true); err != nil {
		return Materialized{}, err
	}
	target := filepath.Join(workspaceRoot, destination)
	info, statErr := os.Lstat(target)
	if statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return Materialized{}, errors.New("artifact package destination is not a real directory")
		}
		if !overwrite {
			return Materialized{}, ErrDestinationConflict
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Materialized{}, statErr
	}

	if errors.Is(statErr, os.ErrNotExist) {
		for _, entry := range archive.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return Materialized{}, fmt.Errorf("open artifact package entry: %w", err)
			}
			_, verifyErr := copyBounded(ctx, io.Discard, reader, s.limits.MaxPackageEntryBytes)
			closeErr := reader.Close()
			if verifyErr != nil {
				return Materialized{}, verifyErr
			}
			if closeErr != nil {
				return Materialized{}, closeErr
			}
		}
		if err := os.Mkdir(target, 0o755); err != nil {
			if _, conflictErr := os.Lstat(target); conflictErr == nil {
				return Materialized{}, ErrDestinationConflict
			}
			return Materialized{}, fmt.Errorf("create artifact package destination: %w", err)
		}
		defer func() {
			if resultErr != nil {
				_ = removeDirectoryTree(target)
			}
		}()
		if err := s.extractMaterializedPackage(ctx, entries, target); err != nil {
			return Materialized{}, err
		}
		return Materialized{Destination: filepath.ToSlash(destination), Package: true, Files: len(entries), Bytes: total, DigestSHA256: blob.DigestSHA256, MediaType: blob.MediaType}, nil
	}

	token, err := randomToken()
	if err != nil {
		return Materialized{}, err
	}
	stagePath := filepath.Join(filepath.Dir(target), ".swarm-artifact-package-"+token)
	backupPath := filepath.Join(filepath.Dir(target), ".swarm-artifact-backup-"+token)
	if err := os.Mkdir(stagePath, 0o755); err != nil {
		return Materialized{}, fmt.Errorf("create artifact package staging directory: %w", err)
	}
	defer func() { _ = removeDirectoryTreeIfPresent(stagePath) }()
	if err := s.extractMaterializedPackage(ctx, entries, stagePath); err != nil {
		return Materialized{}, err
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, destination, false); err != nil {
		return Materialized{}, err
	}
	if err := os.Rename(target, backupPath); err != nil {
		return Materialized{}, fmt.Errorf("stage existing artifact package destination: %w", err)
	}
	if err := os.Rename(stagePath, target); err != nil {
		if rollbackErr := os.Rename(backupPath, target); rollbackErr != nil {
			return Materialized{}, fmt.Errorf("publish artifact package: %v; rollback existing destination: %w", err, rollbackErr)
		}
		return Materialized{}, fmt.Errorf("publish artifact package: %w", err)
	}
	if err := removeDirectoryTree(backupPath); err != nil {
		return Materialized{}, fmt.Errorf("remove replaced artifact package destination: %w", err)
	}
	return Materialized{Destination: filepath.ToSlash(destination), Package: true, Files: len(entries), Bytes: total, DigestSHA256: blob.DigestSHA256, MediaType: blob.MediaType}, nil
}

func (s *Service) extractMaterializedPackage(ctx context.Context, entries []*zip.File, root string) error {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open artifact package entry: %w", err)
		}
		err = materializeWorkspaceFile(ctx, root, filepath.FromSlash(entry.Name), reader, int64(entry.UncompressedSize64), "", s.limits.MaxPackageEntryBytes, false)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func removeDirectoryTreeIfPresent(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return removeDirectoryTree(path)
}

func (s *Service) materializePackageEntries(files []*zip.File) ([]*zip.File, int64, error) {
	if len(files) == 0 || len(files) > s.limits.MaxPackageFiles {
		return nil, 0, ErrQuotaExceeded
	}
	entries := make([]*zip.File, 0, len(files))
	fileNames := make(map[string]struct{}, len(files))
	allNames := make(map[string]string, len(files))
	var total int64
	for _, entry := range files {
		name := entry.Name
		trimmed := strings.TrimSuffix(name, "/")
		if name == "" || len(name) > 1024 || strings.Contains(name, "\\") || trimmed == "" || !safeRelativePath(filepath.FromSlash(trimmed)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(trimmed))) != trimmed {
			return nil, 0, errors.New("artifact package contains an unsafe entry")
		}
		folded := strings.ToLower(trimmed)
		if _, exists := allNames[folded]; exists {
			return nil, 0, errors.New("artifact package contains duplicate or ambiguous entries")
		}
		allNames[folded] = name
		if entry.FileInfo().IsDir() {
			if name != trimmed+"/" {
				return nil, 0, errors.New("artifact package contains an unsafe entry")
			}
			continue
		}
		if !safePackageEntryName(name) || entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
			return nil, 0, errors.New("artifact package contains a symlink or special entry")
		}
		size := int64(entry.UncompressedSize64)
		if size < 0 || size > s.limits.MaxPackageEntryBytes || size > s.limits.MaxPackageBytes-total {
			return nil, 0, ErrQuotaExceeded
		}
		total += size
		fileNames[folded] = struct{}{}
		entries = append(entries, entry)
	}
	if len(entries) == 0 || len(entries) > s.limits.MaxPackageFiles {
		return nil, 0, ErrQuotaExceeded
	}
	for name := range fileNames {
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if _, exists := fileNames[parent]; exists {
				return nil, 0, errors.New("artifact package contains duplicate or ambiguous entries")
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, total, nil
}

func materializeWorkspaceFile(ctx context.Context, workspaceRoot, destination string, source io.Reader, expectedSize int64, expectedDigest string, limit int64, overwrite bool) error {
	cleanDestination := filepath.Clean(destination)
	if filepath.IsAbs(destination) || destination == "" || cleanDestination != destination || !safeRelativePath(destination) {
		return errors.New("artifact materialization file destination escapes its workspace")
	}
	parent := filepath.Dir(destination)
	if parent == "." {
		parent = ""
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, parent, true); err != nil {
		return err
	}
	target := filepath.Join(workspaceRoot, destination)
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("artifact materialization file destination is not a regular non-symlink file")
		}
		if !overwrite {
			return ErrDestinationConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	token, err := randomToken()
	if err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(target), ".swarm-artifact-"+token)
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, appstorage.PrivateFilePerm)
	if err != nil {
		return fmt.Errorf("create artifact materialization staging file: %w", err)
	}
	hash := sha256.New()
	_, copyErr := copyBounded(ctx, io.MultiWriter(file, hash), source, limit)
	if syncErr := file.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("copy artifact into workspace: %w", copyErr)
	}
	info, err := os.Lstat(temp)
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		_ = os.Remove(temp)
		return errors.New("artifact materialization byte size changed while copying")
	}
	if expectedDigest != "" && hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(expectedDigest) {
		_ = os.Remove(temp)
		return errors.New("artifact materialization digest changed while copying")
	}
	if err := os.Chmod(temp, 0o644); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := ensureWorkspaceDirectory(workspaceRoot, parent, false); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if overwrite {
		if info, err := os.Lstat(target); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
			_ = os.Remove(temp)
			return errors.New("artifact materialization file destination became unsafe")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(temp)
			return err
		}
		if err := os.Rename(temp, target); err != nil {
			_ = os.Remove(temp)
			return fmt.Errorf("replace artifact materialization destination: %w", err)
		}
		return nil
	}
	if err := os.Link(temp, target); err != nil {
		_ = os.Remove(temp)
		if _, statErr := os.Lstat(target); statErr == nil {
			return ErrDestinationConflict
		}
		return fmt.Errorf("publish artifact materialization destination: %w", err)
	}
	return os.Remove(temp)
}

func ensureWorkspaceDirectory(workspaceRoot, relative string, create bool) error {
	current := workspaceRoot
	if err := requireRealDirectory(current); err != nil {
		return errors.New("artifact materialization workspace root became unsafe")
	}
	if relative == "" || relative == "." {
		return nil
	}
	if filepath.IsAbs(relative) || !safeRelativePath(relative) {
		return errors.New("artifact materialization path escapes its workspace")
	}
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("artifact materialization path contains a symlink or non-directory")
		}
	}
	return nil
}
