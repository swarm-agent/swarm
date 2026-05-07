package appstorage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

const (
	WorkspacesDir   = "workspaces"
	PrivateDirPerm  = 0o700
	PrivateFilePerm = 0o600

	workspaceHashBytes    = 10
	workspaceSlugMaxRunes = 48
	workspaceSlugFallback = "workspace"
)

var nonWorkspaceSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

// DataDir returns a private Swarm data directory under the canonical daemon data root.
func DataDir(parts ...string) (string, error) {
	root, err := storagecontract.ResolveRoot(storagecontract.RootData, storagecontract.Options{})
	if err != nil {
		return "", err
	}
	path, err := joinRootPath(root, parts...)
	if err != nil {
		return "", err
	}
	return ensurePrivateAppDir(root, path)
}

// CacheDir returns a private Swarm cache directory under the canonical daemon cache root.
func CacheDir(parts ...string) (string, error) {
	root, err := storagecontract.ResolveRoot(storagecontract.RootCache, storagecontract.Options{})
	if err != nil {
		return "", err
	}
	path, err := joinRootPath(root, parts...)
	if err != nil {
		return "", err
	}
	return ensurePrivateAppDir(root, path)
}

// StateDir returns a private Swarm state directory under the canonical daemon runtime root.
func StateDir(parts ...string) (string, error) {
	root, err := storagecontract.ResolveRoot(storagecontract.RootRuntime, storagecontract.Options{})
	if err != nil {
		return "", err
	}
	path, err := joinRootPath(root, parts...)
	if err != nil {
		return "", err
	}
	return ensurePrivateAppDir(root, path)
}

// WorkspaceDataDir returns a private data directory for artifacts owned by a workspace.
func WorkspaceDataDir(workspacePath string, parts ...string) (string, error) {
	bucket, err := WorkspaceBucketName(workspacePath)
	if err != nil {
		return "", err
	}
	return DataDir(append([]string{WorkspacesDir, bucket}, parts...)...)
}

// WorkspaceCacheDir returns a private cache directory for disposable artifacts owned by a workspace.
func WorkspaceCacheDir(workspacePath string, parts ...string) (string, error) {
	bucket, err := WorkspaceBucketName(workspacePath)
	if err != nil {
		return "", err
	}
	return CacheDir(append([]string{WorkspacesDir, bucket}, parts...)...)
}

// WorkspaceStateDir returns a private state directory for state owned by a workspace.
func WorkspaceStateDir(workspacePath string, parts ...string) (string, error) {
	bucket, err := WorkspaceBucketName(workspacePath)
	if err != nil {
		return "", err
	}
	return StateDir(append([]string{WorkspacesDir, bucket}, parts...)...)
}

// WorkspaceBucketName maps a workspace path to a deterministic, safe, non-leaky bucket name.
func WorkspaceBucketName(workspacePath string) (string, error) {
	identity, err := WorkspaceIdentity(workspacePath)
	if err != nil {
		return "", err
	}
	slug := workspaceSlug(identity)
	sum := sha256.Sum256([]byte(identity))
	hash := hex.EncodeToString(sum[:workspaceHashBytes])
	return slug + "-" + hash, nil
}

// WorkspaceIdentity returns the normalized path identity used for workspace bucket hashing.
func WorkspaceIdentity(workspacePath string) (string, error) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", fmt.Errorf("workspace path is required")
	}
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		abs = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

// WritePrivateFile writes a generated Swarm file with private 0600 permissions.
func WritePrivateFile(path string, data []byte) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return fmt.Errorf("file path is required")
	}
	if _, err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, PrivateFilePerm); err != nil {
		return err
	}
	return os.Chmod(path, PrivateFilePerm)
}

func ensurePrivateDir(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	if err := os.MkdirAll(path, PrivateDirPerm); err != nil {
		return "", err
	}
	if err := os.Chmod(path, PrivateDirPerm); err != nil {
		return "", err
	}
	return path, nil
}

func ensurePrivateAppDir(appRoot, path string) (string, error) {
	appRoot = filepath.Clean(strings.TrimSpace(appRoot))
	path = filepath.Clean(strings.TrimSpace(path))
	if appRoot == "." || appRoot == "" {
		return "", fmt.Errorf("app root directory is required")
	}
	if path == "." || path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	if !pathWithinRoot(appRoot, path) {
		return "", fmt.Errorf("app storage path %q escapes app root %q", path, appRoot)
	}
	if err := os.MkdirAll(path, PrivateDirPerm); err != nil {
		return "", err
	}

	dirs := []string{path}
	for dir := path; dir != appRoot; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("app storage path %q escapes app root %q", path, appRoot)
		}
		dirs = append(dirs, parent)
		dir = parent
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], PrivateDirPerm); err != nil {
			return "", err
		}
	}
	return path, nil
}

func joinRootPath(root string, parts ...string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return "", fmt.Errorf("root directory is required")
	}
	joined := root
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if filepath.IsAbs(part) {
			return "", fmt.Errorf("app storage path part %q must be relative", part)
		}
		cleaned := filepath.Clean(part)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("app storage path part %q escapes app directory", part)
		}
		joined = filepath.Join(joined, cleaned)
	}
	return joined, nil
}

func pathWithinRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func workspaceSlug(identity string) string {
	base := strings.ToLower(filepath.Base(identity))
	base = nonWorkspaceSlugRune.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = workspaceSlugFallback
	}
	runes := []rune(base)
	if len(runes) > workspaceSlugMaxRunes {
		base = strings.Trim(string(runes[:workspaceSlugMaxRunes]), "-")
	}
	if base == "" {
		return workspaceSlugFallback
	}
	return base
}
