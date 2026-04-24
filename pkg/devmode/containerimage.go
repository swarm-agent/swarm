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

func ContainerImageFingerprint(root string) (string, error) {
	resolvedRoot, err := ResolveRoot(root)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	inputs := []string{
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
