package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/pkg/storagecontract"
)

type systemDirSpec struct {
	Path  string
	Mode  os.FileMode
	Owner bool
}

func EnsureSystemInstallReady() error {
	roots, err := storagecontract.ResolveRoots(storagecontract.Options{})
	if err != nil {
		return err
	}
	dirs := []systemDirSpec{
		{Path: systemBinDir(), Mode: 0o755},
		{Path: filepath.Dir(systemInstallRoot()), Mode: 0o755},
		{Path: systemBinaryDir(), Mode: 0o755, Owner: true},
		{Path: systemToolBinDir(), Mode: 0o755, Owner: true},
		{Path: systemInstallRoot(), Mode: 0o755, Owner: true},
		{Path: systemLibDir(), Mode: 0o755, Owner: true},
		{Path: systemDesktopDistDir(), Mode: 0o755, Owner: true},
		{Path: roots.ConfigDir, Mode: 0o700, Owner: true},
		{Path: roots.DataDir, Mode: 0o700, Owner: true},
		{Path: filepath.Join(roots.DataDir, "dev"), Mode: 0o700, Owner: true},
		{Path: roots.CacheDir, Mode: 0o700, Owner: true},
		{Path: roots.RuntimeDir, Mode: 0o700, Owner: true},
		{Path: filepath.Join(roots.RuntimeDir, "dev"), Mode: 0o700, Owner: true},
		{Path: filepath.Join(roots.RuntimeDir, "ports"), Mode: 0o700, Owner: true},
		{Path: roots.LogsDir, Mode: 0o755, Owner: true},
		{Path: filepath.Join(roots.LogsDir, "dev"), Mode: 0o755, Owner: true},
	}
	if filepath.Clean(roots.RuntimeDir) == "/run/swarmd" {
		dirs = append(dirs, systemDirSpec{Path: filepath.Join(string(filepath.Separator), "etc", "tmpfiles.d"), Mode: 0o755})
	}
	if err := ensureDirsLocal(dirs); err == nil {
		return ensureTmpfilesConfig(roots)
	}
	if err := ensureDirsPrivileged(dirs); err != nil {
		return err
	}
	return ensureTmpfilesConfig(roots)
}

func ensureDirsLocal(dirs []systemDirSpec) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir.Path, dir.Mode); err != nil {
			return err
		}
		if dir.Owner {
			if err := os.Chmod(dir.Path, dir.Mode); err != nil {
				return err
			}
		}
		if dir.Owner && !dirWritable(dir.Path) {
			return fmt.Errorf("directory is not writable: %s", dir.Path)
		}
		if !dir.Owner && !dirExists(dir.Path) {
			return fmt.Errorf("directory does not exist: %s", dir.Path)
		}
	}
	return nil
}

func dirWritable(path string) bool {
	probe, err := os.CreateTemp(path, ".swarm-write-check-")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func ensureDirsPrivileged(dirs []systemDirSpec) error {
	uid, gid := installOwnerIDs()
	for _, dir := range dirs {
		args := []string{"install", "-d", "-m", fmt.Sprintf("%04o", dir.Mode.Perm())}
		if dir.Owner {
			args = append(args, "-o", uid, "-g", gid)
		}
		args = append(args, dir.Path)
		if err := runPrivilegedCommand(args...); err != nil {
			return fmt.Errorf("provision system directory %q: %w", dir.Path, err)
		}
	}
	return nil
}

func ensureSystemBinDir() error {
	if err := os.MkdirAll(systemBinDir(), 0o755); err == nil {
		return nil
	}
	if err := runPrivilegedCommand("install", "-d", "-m", "0755", systemBinDir()); err != nil {
		return fmt.Errorf("provision system bin directory %q: %w", systemBinDir(), err)
	}
	return nil
}

func ensureTmpfilesConfig(roots storagecontract.Roots) error {
	if filepath.Clean(roots.RuntimeDir) != "/run/swarmd" {
		return nil
	}
	uid, gid := installOwnerIDs()
	content := fmt.Sprintf("d /run/swarmd 0700 %s %s -\nd /run/swarmd/dev 0700 %s %s -\nd /run/swarmd/ports 0700 %s %s -\n", uid, gid, uid, gid, uid, gid)
	path := filepath.Join(string(filepath.Separator), "etc", "tmpfiles.d", "swarmd.conf")
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp("", "swarmd-tmpfiles-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)
	if err := runPrivilegedCommand("install", "-m", "0644", tmpPath, path); err != nil {
		return fmt.Errorf("provision tmpfiles config %q: %w", path, err)
	}
	return nil
}

func installOwnerIDs() (string, string) {
	uid := strings.TrimSpace(os.Getenv("SUDO_UID"))
	gid := strings.TrimSpace(os.Getenv("SUDO_GID"))
	if uid == "" || gid == "" {
		uid = strconv.Itoa(os.Getuid())
		gid = strconv.Itoa(os.Getgid())
	}
	return uid, gid
}

func runPrivilegedCommand(args ...string) error {
	if len(args) == 0 {
		return errors.New("privileged command is required")
	}
	name := args[0]
	cmdArgs := args[1:]
	if os.Geteuid() != 0 {
		sudoPath, err := exec.LookPath("sudo")
		if err != nil {
			return errors.New("sudo not found; Swarm system install paths require sudo or pre-created writable directories")
		}
		cmdArgs = append([]string{name}, cmdArgs...)
		name = sudoPath
	}
	cmd := exec.Command(name, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func replaceSymlinkPrivileged(linkPath, target string) error {
	if symlinkAlreadyPoints(linkPath, target) {
		return nil
	}
	if err := replaceSymlink(linkPath, target); err == nil {
		return nil
	}
	if err := runPrivilegedCommand("ln", "-sfnT", target, linkPath); err != nil {
		return err
	}
	return nil
}

func symlinkAlreadyPoints(linkPath, target string) bool {
	info, err := os.Lstat(linkPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	existing, err := os.Readlink(linkPath)
	return err == nil && filepath.Clean(existing) == filepath.Clean(target)
}
