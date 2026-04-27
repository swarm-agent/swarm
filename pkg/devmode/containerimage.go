package devmode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultContainerImageRef           = "localhost/swarm-container-mvp:latest"
	ContainerImageDevModeLabel         = "swarmagent.dev-mode"
	ContainerImageFingerprintLabel     = "swarmagent.dev-fingerprint"
	ContainerImageBaseFingerprintLabel = "swarmagent.dev-base-fingerprint"
)

func ResolveRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("dev root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve dev root %q: %w", root, err)
	}
	absRoot = filepath.Clean(absRoot)
	required := []string{
		filepath.Join(absRoot, "scripts", "rebuild-container.sh"),
		filepath.Join(absRoot, "deploy", "container-mvp", "Containerfile.base"),
		filepath.Join(absRoot, "deploy", "container-mvp", "Containerfile"),
		filepath.Join(absRoot, "deploy", "container-mvp", "entrypoint.sh"),
	}
	for _, path := range required {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("resolve dev root %q: missing required path %s", absRoot, path)
		}
		if info.IsDir() {
			return "", fmt.Errorf("resolve dev root %q: expected file at %s", absRoot, path)
		}
	}
	return absRoot, nil
}

func RebuildScriptPath(root string) (string, error) {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedRoot, "scripts", "rebuild-container.sh"), nil
}

func SyncStagedContainerBinaries(root, sourceBinDir string) error {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return err
	}
	sourceBinDir = strings.TrimSpace(sourceBinDir)
	if sourceBinDir == "" {
		return fmt.Errorf("dev container binary source dir is required")
	}
	sourceBinDir, err = filepath.Abs(sourceBinDir)
	if err != nil {
		return fmt.Errorf("resolve dev container binary source dir %q: %w", sourceBinDir, err)
	}
	sourceBinDir = filepath.Clean(sourceBinDir)
	stageDir := filepath.Join(resolvedRoot, ".bin", "main")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("prepare staged dev container binary dir: %w", err)
	}
	for _, name := range []string{"swarmd", "swarmctl"} {
		sourcePath := filepath.Join(sourceBinDir, name)
		targetPath := filepath.Join(stageDir, name)
		if err := syncStagedContainerBinary(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func syncStagedContainerBinary(sourcePath, targetPath string) error {
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stage dev container binary %q: %w", sourcePath, err)
	}
	if sourceInfo.IsDir() {
		return fmt.Errorf("stage dev container binary %q: expected file, got directory", sourcePath)
	}
	if targetInfo, err := os.Stat(targetPath); err == nil && os.SameFile(sourceInfo, targetInfo) {
		if err := os.Chmod(targetPath, 0o755); err != nil {
			return fmt.Errorf("mark staged dev container binary executable %q: %w", targetPath, err)
		}
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat staged dev container binary %q: %w", targetPath, err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open dev container binary %q: %w", sourcePath, err)
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create staged dev container binary temp file %q: %w", targetPath, err)
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(temp, source); err != nil {
		_ = temp.Close()
		return fmt.Errorf("copy dev container binary %q to %q: %w", sourcePath, targetPath, err)
	}
	if err := temp.Chmod(0o755); err != nil {
		_ = temp.Close()
		return fmt.Errorf("mark staged dev container binary executable %q: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close staged dev container binary temp file %q: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return fmt.Errorf("install staged dev container binary %q: %w", targetPath, err)
	}
	cleanup = false
	return nil
}

func ContainerImageFingerprint(root string) (string, error) {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	inputs := []string{
		filepath.Join(resolvedRoot, "deploy", "container-mvp", "Containerfile.base"),
		filepath.Join(resolvedRoot, "deploy", "container-mvp", "Containerfile"),
		filepath.Join(resolvedRoot, "deploy", "container-mvp", "entrypoint.sh"),
		filepath.Join(resolvedRoot, ".bin", "main", "swarmd"),
		filepath.Join(resolvedRoot, ".bin", "main", "swarmctl"),
		filepath.Join(resolvedRoot, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"),
	}
	for _, path := range inputs {
		if err := hashFileWithPath(h, path, resolvedRoot); err != nil {
			return "", err
		}
	}
	if err := hashDirWithPath(h, filepath.Join(resolvedRoot, "web", "dist"), resolvedRoot); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func hashDirWithPath(h io.Writer, rootPath, buildRoot string) error {
	rootPath = filepath.Clean(rootPath)
	entries := make([]string, 0, 32)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("hash dev image dir %q: %w", rootPath, err)
	}
	sort.Strings(entries)
	for _, path := range entries {
		if err := hashFileWithPath(h, path, buildRoot); err != nil {
			return err
		}
	}
	return nil
}

func hashFileWithPath(h io.Writer, path, buildRoot string) error {
	rel, err := filepath.Rel(buildRoot, path)
	if err != nil {
		return fmt.Errorf("hash dev image file %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("hash dev image file %q: %w", path, err)
	}
	if _, err := io.WriteString(h, filepath.ToSlash(rel)); err != nil {
		return err
	}
	if _, err := io.WriteString(h, "\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(h, fmt.Sprintf("%d\n", info.Size())); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open dev image file %q: %w", path, err)
	}
	defer file.Close()
	if _, err := io.Copy(h, file); err != nil {
		return fmt.Errorf("hash dev image file %q: %w", path, err)
	}
	if _, err := io.WriteString(h, "\n"); err != nil {
		return err
	}
	return nil
}
