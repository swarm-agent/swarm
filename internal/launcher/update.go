package launcher

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

const (
	updateDownloadTimeout = 2 * time.Minute
	updateParentWaitLimit = 30 * time.Second
)

type UpdateResult struct {
	Version      string
	RuntimeRoot  string
	CurrentLink  string
	PreviousLink string
}

func ApplyReleaseUpdate(ctx context.Context, profile Profile, plan client.UpdateApplyPlan) (UpdateResult, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return UpdateResult{}, fmt.Errorf("update apply currently supports linux/amd64 only (running on %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	if strings.EqualFold(strings.TrimSpace(profile.Lane), "dev") {
		return UpdateResult{}, errors.New("update apply is disabled for the dev lane")
	}
	version := strings.TrimSpace(plan.TargetVersion)
	if version == "" {
		return UpdateResult{}, errors.New("target version is required")
	}
	if strings.TrimSpace(plan.AssetURL) == "" {
		return UpdateResult{}, errors.New("asset url is required")
	}
	sha256Digest := normalizeUpdateSHA256(plan.SHA256)
	if sha256Digest == "" {
		return UpdateResult{}, errors.New("sha256 digest is required")
	}

	versionsDir := filepath.Join(profile.InstallRoot, "versions")
	targetRoot := filepath.Join(versionsDir, version)
	currentLink := filepath.Join(profile.InstallRoot, "current")
	previousLink := filepath.Join(profile.InstallRoot, "previous")

	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return UpdateResult{}, err
	}
	if installedVersionMatches(targetRoot, version) {
		if err := switchRuntimeLinks(profile.InstallRoot, targetRoot); err != nil {
			return UpdateResult{}, err
		}
		if err := installLauncherSymlinks(profile.InstallRoot); err != nil {
			return UpdateResult{}, err
		}
		return UpdateResult{Version: version, RuntimeRoot: targetRoot, CurrentLink: currentLink, PreviousLink: previousLink}, nil
	}

	stageDir, err := os.MkdirTemp(versionsDir, ".update-stage-")
	if err != nil {
		return UpdateResult{}, err
	}
	defer os.RemoveAll(stageDir)

	archivePath := filepath.Join(stageDir, pathBaseOrDefault(plan.AssetName, "swarm-update.tar.gz"))
	if err := downloadAndVerifyArchive(ctx, archivePath, strings.TrimSpace(plan.AssetURL), sha256Digest); err != nil {
		return UpdateResult{}, err
	}

	extractDir := filepath.Join(stageDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return UpdateResult{}, err
	}
	rootDir, err := extractTarGz(archivePath, extractDir)
	if err != nil {
		return UpdateResult{}, err
	}
	if rootDir == "" {
		return UpdateResult{}, errors.New("release archive missing top-level directory")
	}
	artifactRoot := filepath.Join(extractDir, rootDir)
	buildInfoVersion, err := readBuildInfoVersion(filepath.Join(artifactRoot, "build-info.txt"))
	if err != nil {
		return UpdateResult{}, err
	}
	if buildInfoVersion != version {
		return UpdateResult{}, fmt.Errorf("release version mismatch: archive=%s expected=%s", buildInfoVersion, version)
	}

	stagedRuntime := filepath.Join(stageDir, "runtime")
	if err := installRuntimeTreeFromArtifact(stagedRuntime, artifactRoot); err != nil {
		return UpdateResult{}, err
	}
	if err := os.WriteFile(filepath.Join(stagedRuntime, ".version"), []byte(version+"\n"), 0o644); err != nil {
		return UpdateResult{}, err
	}
	if err := os.RemoveAll(targetRoot); err != nil {
		return UpdateResult{}, err
	}
	if err := os.Rename(stagedRuntime, targetRoot); err != nil {
		return UpdateResult{}, fmt.Errorf("activate staged runtime: %w", err)
	}
	if err := switchRuntimeLinks(profile.InstallRoot, targetRoot); err != nil {
		return UpdateResult{}, err
	}
	if err := installLauncherSymlinks(profile.InstallRoot); err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Version: version, RuntimeRoot: targetRoot, CurrentLink: currentLink, PreviousLink: previousLink}, nil
}

func RestartThroughUpdatedRuntime(profile Profile, args []string) error {
	swarmPath := filepath.Join(profile.ToolBinDir, "swarm")
	if !isExecutable(swarmPath) {
		return missingInstalledBinaryError("swarm", swarmPath)
	}
	cmd := exec.Command(swarmPath, args...)
	cmd.Env = profile.EnvList(nil)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

func StartUpdateHelper(profile Profile, plan client.UpdateApplyPlan, parentPID int, relaunchArgs []string) error {
	helperPath := filepath.Join(profile.ToolBinDir, "swarmsetup")
	if !isExecutable(helperPath) {
		return missingInstalledBinaryError("swarmsetup", helperPath)
	}
	args := []string{
		"--apply-release",
		"--lane", strings.TrimSpace(profile.Lane),
		"--target-version", strings.TrimSpace(plan.TargetVersion),
		"--asset-name", strings.TrimSpace(plan.AssetName),
		"--asset-url", strings.TrimSpace(plan.AssetURL),
		"--sha256", strings.TrimSpace(plan.SHA256),
	}
	if parentPID > 0 {
		args = append(args, "--parent-pid", strconv.Itoa(parentPID))
	}
	for _, arg := range relaunchArgs {
		args = append(args, "--relaunch-arg", arg)
	}
	cmd := exec.Command(helperPath, args...)
	cmd.Env = profile.EnvList(nil)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func RunUpdateHelper(profile Profile, plan client.UpdateApplyPlan, parentPID int, relaunchArgs []string) error {
	if parentPID > 0 {
		if err := waitForPIDExit(parentPID, updateParentWaitLimit); err != nil {
			return err
		}
	}
	if err := StopBackend(profile); err != nil {
		return err
	}
	if _, err := ApplyReleaseUpdate(context.Background(), profile, plan); err != nil {
		return err
	}
	return RestartThroughUpdatedRuntime(profile, relaunchArgs)
}

func CurrentRuntimeVersion(installRoot string) string {
	currentRoot, ok := resolveRuntimeLink(filepath.Join(strings.TrimSpace(installRoot), "current"))
	if !ok {
		return ""
	}
	version, err := readBuildInfoVersion(filepath.Join(currentRoot, "build-info.txt"))
	if err == nil {
		return version
	}
	versionBytes, err := os.ReadFile(filepath.Join(currentRoot, ".version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(versionBytes))
}

func installRuntimeTreeFromArtifact(runtimeRoot, artifactRoot string) error {
	artifactRoot = filepath.Clean(strings.TrimSpace(artifactRoot))
	if artifactRoot == "" {
		return errors.New("artifact root must not be empty")
	}
	platformRoot := filepath.Join(artifactRoot, runtime.GOOS+"-"+runtime.GOARCH)
	rootArtifactDir := filepath.Join(platformRoot, "root")
	swarmdArtifactDir := filepath.Join(platformRoot, "swarmd")
	toolDir := filepath.Join(runtimeRoot, "libexec")
	binDir := filepath.Join(runtimeRoot, "bin")
	libDir := filepath.Join(runtimeRoot, "lib")
	requiredFiles := []struct {
		name       string
		source     string
		target     string
		executable bool
	}{
		{name: "swarm", source: filepath.Join(rootArtifactDir, "swarm"), target: filepath.Join(toolDir, "swarm"), executable: true},
		{name: "swarmdev", source: filepath.Join(rootArtifactDir, "swarmdev"), target: filepath.Join(toolDir, "swarmdev"), executable: true},
		{name: "rebuild", source: filepath.Join(rootArtifactDir, "rebuild"), target: filepath.Join(toolDir, "rebuild"), executable: true},
		{name: "swarmsetup", source: filepath.Join(rootArtifactDir, "swarmsetup"), target: filepath.Join(toolDir, "swarmsetup"), executable: true},
		{name: "swarmtui", source: filepath.Join(rootArtifactDir, "swarmtui"), target: filepath.Join(binDir, "swarmtui"), executable: true},
		{name: "swarmd", source: filepath.Join(swarmdArtifactDir, "swarmd"), target: filepath.Join(binDir, "swarmd"), executable: true},
		{name: "swarmctl", source: filepath.Join(swarmdArtifactDir, "swarmctl"), target: filepath.Join(binDir, "swarmctl"), executable: true},
		{name: "libfff_c.so", source: filepath.Join(swarmdArtifactDir, "libfff_c.so"), target: filepath.Join(libDir, "libfff_c.so"), executable: false},
	}
	for _, item := range requiredFiles {
		if item.executable {
			if !isExecutable(item.source) {
				return fmt.Errorf("missing executable artifact for %s: %s", item.name, item.source)
			}
		} else if !isReadableFile(item.source) {
			return fmt.Errorf("missing runtime artifact for %s: %s", item.name, item.source)
		}
		if err := copyFile(item.source, item.target); err != nil {
			return err
		}
	}
	webArtifactDir := filepath.Join(artifactRoot, "web")
	if _, err := os.Stat(filepath.Join(webArtifactDir, "index.html")); err == nil {
		webTargetDir := filepath.Join(runtimeRoot, "share")
		if err := os.RemoveAll(webTargetDir); err != nil {
			return err
		}
		if err := copyDir(webArtifactDir, webTargetDir); err != nil {
			return err
		}
		if err := writeCompressedDesktopAssets(webTargetDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := copyFile(filepath.Join(artifactRoot, "build-info.txt"), filepath.Join(runtimeRoot, "build-info.txt")); err != nil {
		return err
	}
	return nil
}

func switchRuntimeLinks(installRoot, targetRoot string) error {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	targetRoot = filepath.Clean(strings.TrimSpace(targetRoot))
	if installRoot == "" || targetRoot == "" {
		return errors.New("install root and target root are required")
	}
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return err
	}
	currentLink := filepath.Join(installRoot, "current")
	previousLink := filepath.Join(installRoot, "previous")
	if previousTarget, ok := resolveRuntimeLink(currentLink); ok && filepath.Clean(previousTarget) != targetRoot {
		if err := replaceSymlink(previousLink, previousTarget); err != nil {
			return fmt.Errorf("set previous runtime link: %w", err)
		}
	}
	if err := replaceSymlink(currentLink, targetRoot); err != nil {
		return fmt.Errorf("set current runtime link: %w", err)
	}
	for _, name := range []string{"bin", "libexec", "lib", "share"} {
		if err := replaceSymlink(filepath.Join(installRoot, name), filepath.Join(installRoot, "current", name)); err != nil {
			return fmt.Errorf("set %s runtime link: %w", name, err)
		}
	}
	return nil
}

func installLauncherSymlinks(installRoot string) error {
	binHome, err := xdgBinHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binHome, 0o755); err != nil {
		return err
	}
	links := map[string]string{
		"swarm":      filepath.Join(installRoot, "libexec", "swarm"),
		"swarmdev":   filepath.Join(installRoot, "libexec", "swarmdev"),
		"rebuild":    filepath.Join(installRoot, "libexec", "rebuild"),
		"swarmsetup": filepath.Join(installRoot, "libexec", "swarmsetup"),
	}
	for name, target := range links {
		if !isExecutable(target) {
			return fmt.Errorf("missing executable launcher: %s", target)
		}
		if err := replaceSymlink(filepath.Join(binHome, name), target); err != nil {
			return fmt.Errorf("link %s -> %s: %w", filepath.Join(binHome, name), target, err)
		}
	}
	return nil
}

func downloadAndVerifyArchive(ctx context.Context, targetPath, rawURL, expectedSHA string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, updateDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release asset: github returned %s", resp.Status)
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, hash), resp.Body); err != nil {
		_ = file.Close()
		return fmt.Errorf("write release asset: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expectedSHA {
		return fmt.Errorf("sha256 mismatch: got %s want %s", actual, expectedSHA)
	}
	return nil
}

func extractTarGz(archivePath, targetDir string) (string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open gzip stream: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var root string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar entry: %w", err)
		}
		name := strings.TrimSpace(hdr.Name)
		if name == "" {
			continue
		}
		cleanName := filepath.Clean(name)
		if cleanName == "." || strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return "", fmt.Errorf("invalid archive entry %q", name)
		}
		parts := strings.Split(cleanName, string(filepath.Separator))
		if len(parts) > 0 && root == "" {
			root = parts[0]
		}
		targetPath := filepath.Join(targetDir, cleanName)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(hdr.Mode).Perm()); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return "", err
			}
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(file, tr); err != nil {
				_ = file.Close()
				return "", err
			}
			if err := file.Close(); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("unsupported archive entry %q", name)
		}
	}
	return root, nil
}

func readBuildInfoVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read build info: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "version=") {
			continue
		}
		version := strings.TrimSpace(strings.TrimPrefix(line, "version="))
		if version == "" {
			break
		}
		return version, nil
	}
	return "", errors.New("build info missing version")
}

func resolveRuntimeLink(linkPath string) (string, bool) {
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(linkPath), target)
	}
	return filepath.Clean(target), true
}

func replaceSymlink(linkPath, target string) error {
	tmpPath := linkPath + ".tmp"
	_ = os.Remove(tmpPath)
	if err := os.Symlink(target, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, linkPath); err != nil {
		_ = os.Remove(tmpPath)
		_ = os.Remove(linkPath)
		if err2 := os.Symlink(target, linkPath); err2 != nil {
			return err
		}
	}
	return nil
}

func installedVersionMatches(targetRoot, version string) bool {
	if _, err := os.Stat(filepath.Join(targetRoot, "bin", "swarmtui")); err != nil {
		return false
	}
	installed, err := readBuildInfoVersion(filepath.Join(targetRoot, "build-info.txt"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(installed) == strings.TrimSpace(version)
}

func normalizeUpdateSHA256(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func pathBaseOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	base := filepath.Base(value)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return fallback
	}
	return base
}

func waitForPIDExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return nil
		}
		err = proc.Signal(syscall.Signal(0))
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for process %d to exit", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
