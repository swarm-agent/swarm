package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

// RunFirstInstall admits only a root-owned native artifact launcher on a fresh
// machine. Building exposes the real launcher; provisioning still belongs to
// swarmsetup and its explicit terminal account choice, never the build process.
func RunFirstInstall(desktop bool) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	dir, err := setupRecoveryDir()
	if err != nil {
		return false, err
	}
	recovery, err := readSetupRecovery(dir)
	if err != nil {
		return true, err
	}
	if recovery != nil && recovery.Stage != "done" {
		root, err := firstInstallArtifact(filepath.Join(recovery.Artifact, "linux-amd64", "root", "swarm"))
		if err != nil || root == "" {
			return true, fmt.Errorf("cannot resume setup artifact: %w", err)
		}
		return true, runFirstInstallHelper(root, desktop)
	}
	present, err := firstInstallStatePresent()
	if err != nil || present {
		return false, err
	}
	executable, err := os.Executable()
	if err != nil {
		return false, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return false, err
	}
	artifactRoot, err := firstInstallArtifact(executable)
	if err != nil || artifactRoot == "" {
		return false, err
	}
	return true, runFirstInstallHelper(artifactRoot, desktop)
}

func firstInstallStatePresent() (bool, error) {
	roots, err := storagecontract.ResolveRoots(storagecontract.Options{})
	if err != nil {
		return false, err
	}
	for _, path := range []string{systemInstallRoot(), roots.ConfigDir, roots.DataDir, roots.CacheDir, roots.RuntimeDir, roots.LogsDir, systemdSystemUnitPath()} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil // Existing/partial installation is not a bootstrap target.
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func runFirstInstallHelper(artifactRoot string, desktop bool) error {
	helper := filepath.Join(artifactRoot, "linux-amd64", "root", "swarmsetup")
	args := []string{"--artifact-root", artifactRoot, "--onboarding", "--service"}
	if desktop {
		args = append(args, "--desktop")
	}
	// Setup owns readiness and the explicit selected-user handoff. Cancellation
	// must never launch an application merely because a unit file now exists.
	return RunForeground(helper, args, os.Environ())
}

func firstInstallArtifact(executable string) (string, error) {
	toolDir := filepath.Dir(executable)
	platformDir := filepath.Dir(toolDir)
	if filepath.Base(executable) != "swarm" || filepath.Base(toolDir) != "root" || filepath.Base(platformDir) != "linux-amd64" {
		return "", nil
	}
	root := filepath.Dir(platformDir)
	for _, rel := range []string{"build-info.txt", "LICENSE", "THIRD_PARTY_NOTICES.md", "linux-amd64/root/rebuild", "linux-amd64/root/swarmdev", "linux-amd64/root/swarm", "linux-amd64/root/swarmsetup", "linux-amd64/root/swarmtui", "linux-amd64/swarmd/swarmd", "linux-amd64/swarmd/swarmctl", "linux-amd64/swarmd/swarm-fff-search", "linux-amd64/swarmd/libfff_c.so", "web/index.html"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("incomplete first-run artifact: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("first-run artifact must be a regular file: %s", path)
		}
		if err := trustedFirstInstallPath(path); err != nil {
			return "", err
		}
	}
	return root, nil
}

func trustedFirstInstallPath(path string) error {
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !trustedFirstInstallMetadata(info) {
			return fmt.Errorf("first-run artifact requires root-owned, non-writable-by-others files and parents: %s", path)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func trustedFirstInstallMetadata(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 == 0
}
