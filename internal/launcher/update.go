package launcher

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	updateDownloadTimeout     = 2 * time.Minute
	updateParentWaitLimit     = 30 * time.Second
	updateBootWaitLimit       = 30 * time.Second
	updatePollInterval        = 200 * time.Millisecond
	pendingUpdateBootstrapEnv = "SWARM_PENDING_UPDATE_BOOT"
	appliedUpdateToastEnv     = "SWARM_APPLIED_UPDATE_TOAST"
)

var (
	stopBackendForUpdate                     = StopBackend
	applyReleaseUpdateForUpdate              = ApplyReleaseUpdate
	startRuntimeCommandForUpdate             = startRuntimeCommand
	resolveLifecycleManagerForUpdate         = resolveLifecycleManager
	serviceActiveForUpdate                   = serviceActiveForScope
	restartSystemdServiceForUpdate           = restartSystemdService
	waitForRuntimeBootConfirmationForUpdate  = waitForRuntimeBootConfirmation
	rollbackPendingUpdateAndRestartForUpdate = rollbackPendingUpdateAndRestart
)

type runtimeBootStatus struct {
	Version     string `json:"version,omitempty"`
	RuntimeRoot string `json:"runtime_root,omitempty"`
	State       string `json:"state,omitempty"`
	Failure     string `json:"failure,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

type UpdateResult struct {
	Version      string
	RuntimeRoot  string
	CurrentLink  string
	PreviousLink string
}

type updateRestartPlan struct {
	managerKind   string
	systemdScope  systemdServiceScope
	systemdUnit   string
	systemdActive bool
	blockedErr    error
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
	previousRoot, _ := resolveRuntimeLink(currentLink)

	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		return UpdateResult{}, err
	}
	if installedVersionMatches(targetRoot, version) {
		if err := switchRuntimeLinks(profile.InstallRoot, targetRoot); err != nil {
			return UpdateResult{}, err
		}
		if err := markPendingRuntimeUpdate(profile.InstallRoot, targetRoot, previousRoot, version); err != nil {
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
	if err := markPendingRuntimeUpdate(profile.InstallRoot, targetRoot, previousRoot, version); err != nil {
		return UpdateResult{}, err
	}
	if err := installLauncherSymlinks(profile.InstallRoot); err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Version: version, RuntimeRoot: targetRoot, CurrentLink: currentLink, PreviousLink: previousLink}, nil
}

func RestartThroughUpdatedRuntime(profile Profile, args []string) error {
	cmd, err := startRuntimeCommand(profile, args, nil)
	if err != nil {
		return err
	}
	return cmd.Start()
}

func startRuntimeCommand(profile Profile, args []string, extraEnv map[string]string) (*exec.Cmd, error) {
	swarmPath := filepath.Join(profile.ToolBinDir, "swarm")
	if !isExecutable(swarmPath) {
		return nil, missingInstalledBinaryError("swarm", swarmPath)
	}
	cmd := exec.Command(swarmPath, args...)
	cmd.Env = profile.EnvList(extraEnv)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd, nil
}

func RequestReleaseUpdatePlan(ctx context.Context, profile Profile) (client.UpdateApplyPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body, status, err := httpRequest(ctx, profile, http.MethodPost, profile.URL+"/v1/update/apply", map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}, map[string]any{})
	if err != nil {
		return client.UpdateApplyPlan{}, err
	}
	if status != http.StatusOK {
		return client.UpdateApplyPlan{}, fmt.Errorf("update apply failed (%d): %s", status, responseErrorMessage(body))
	}
	var plan client.UpdateApplyPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return client.UpdateApplyPlan{}, fmt.Errorf("decode update apply response: %w", err)
	}
	return plan, nil
}

func RunReleaseUpdate(profile Profile, relaunchArgs []string) error {
	plan, err := RequestReleaseUpdatePlan(context.Background(), profile)
	if err != nil {
		return err
	}
	version := strings.TrimSpace(plan.TargetVersion)
	if version == "" {
		version = "new release"
	}
	fmt.Fprintf(os.Stdout, "\nUpdating to %s...\n", version)
	fmt.Fprintln(os.Stdout, "Swarm is shut down before applying the update.")
	fmt.Fprintln(os.Stdout, "Swarm will attempt to restart automatically when the update finishes.")
	fmt.Fprintln(os.Stdout, "If automatic restart is blocked, Swarm will tell you to restart it manually.")
	return RunUpdateHelper(profile, plan, 0, relaunchArgs)
}

func StartUpdateHelper(profile Profile, plan client.UpdateApplyPlan, parentPID int, relaunchArgs []string) error {
	helperPath := filepath.Join(profile.ToolBinDir, "swarmsetup")
	if !isExecutable(helperPath) {
		return missingInstalledBinaryError("swarmsetup", helperPath)
	}
	args := updateHelperArgs(profile, plan, parentPID, relaunchArgs)
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

func RunUpdateHelperForeground(profile Profile, plan client.UpdateApplyPlan, relaunchArgs []string) error {
	helperPath := filepath.Join(profile.ToolBinDir, "swarmsetup")
	if !isExecutable(helperPath) {
		return missingInstalledBinaryError("swarmsetup", helperPath)
	}
	args := updateHelperArgs(profile, plan, 0, relaunchArgs)
	cmd := exec.Command(helperPath, args...)
	cmd.Env = profile.EnvList(nil)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateHelperArgs(profile Profile, plan client.UpdateApplyPlan, parentPID int, relaunchArgs []string) []string {
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
	return args
}

func RunUpdateHelper(profile Profile, plan client.UpdateApplyPlan, parentPID int, relaunchArgs []string) error {
	if parentPID > 0 {
		if err := waitForPIDExit(parentPID, updateParentWaitLimit); err != nil {
			return err
		}
	}
	restartPlan, err := resolveUpdateRestartPlan(profile)
	if err != nil {
		return err
	}
	if err := stopBackendForUpdate(profile); err != nil {
		return err
	}
	result, err := applyReleaseUpdateForUpdate(context.Background(), profile, plan)
	if err != nil {
		return err
	}
	if restartPlan.managerKind == lifecycleKindSystemd {
		return restartUpdatedRuntimeViaSystemd(profile, result, restartPlan)
	}
	cmd, err := startRuntimeCommandForUpdate(profile, relaunchArgs, map[string]string{
		appliedUpdateToastEnv: fmt.Sprintf("Updated to %s", strings.TrimSpace(result.Version)),
	})
	if err != nil {
		return rollbackPendingUpdateAndRestartForUpdate(profile, relaunchArgs, nil, err)
	}
	if err := cmd.Start(); err != nil {
		return rollbackPendingUpdateAndRestartForUpdate(profile, relaunchArgs, cmd.Process, err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	if err := waitForRuntimeBootConfirmationForUpdate(profile.InstallRoot, result.RuntimeRoot, waitCh, updateBootWaitLimit); err != nil {
		return rollbackPendingUpdateAndRestartForUpdate(profile, relaunchArgs, cmd.Process, err)
	}
	return nil
}

func responseErrorMessage(body []byte) string {
	message := strings.TrimSpace(string(body))
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		message = strings.TrimSpace(payload.Error)
	}
	if message == "" {
		message = "empty response"
	}
	return message
}

func resolveUpdateRestartPlan(profile Profile) (updateRestartPlan, error) {
	manager, managerKnown, err := resolveLifecycleManagerForUpdate(profile)
	if err != nil {
		return updateRestartPlan{}, err
	}
	plan := updateRestartPlan{managerKind: manager.Kind}
	if manager.Kind != lifecycleKindSystemd {
		return plan, nil
	}
	plan.systemdScope = normalizeSystemdScope(manager.Scope)
	plan.systemdUnit = strings.TrimSpace(manager.Unit)
	if plan.systemdScope == "" || plan.systemdUnit == "" {
		if managerKnown {
			plan.blockedErr = errors.New("systemd lifecycle metadata is incomplete")
			return plan, nil
		}
		plan.blockedErr = errors.New("automatic systemd restart requires recorded lifecycle metadata")
		return plan, nil
	}
	active, installed, err := serviceActiveForUpdate(plan.systemdScope, plan.systemdUnit)
	if err != nil {
		plan.blockedErr = err
		return plan, nil
	}
	if !installed {
		plan.blockedErr = fmt.Errorf("systemd service %s is not installed", plan.systemdUnit)
		return plan, nil
	}
	plan.systemdActive = active
	return plan, nil
}

func restartUpdatedRuntimeViaSystemd(profile Profile, result UpdateResult, plan updateRestartPlan) error {
	if plan.blockedErr != nil {
		printManualRestartMessage(result.Version, plan.blockedErr)
		return nil
	}
	if err := restartSystemdServiceForUpdate(plan.systemdScope, plan.systemdUnit, plan.systemdActive); err != nil {
		printManualRestartMessage(result.Version, err)
		return nil
	}
	if err := markCurrentRuntimeBootSuccessful(profile.InstallRoot); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Swarm update applied to %s. Restarted via systemd service %s.\n", strings.TrimSpace(result.Version), plan.systemdUnit)
	return nil
}

func printManualRestartMessage(version string, reason error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "the new version"
	}
	fmt.Fprintf(os.Stdout, "Swarm updated to %s, but automatic restart could not be completed.\n", version)
	if reason != nil {
		fmt.Fprintf(os.Stdout, "Automatic restart was blocked: %v\n", reason)
	}
	fmt.Fprintln(os.Stdout, "Please restart Swarm manually.")
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

func pendingRuntimeLink(installRoot string) string {
	return filepath.Join(strings.TrimSpace(installRoot), "pending-target")
}

func lastKnownGoodRuntimeLink(installRoot string) string {
	return filepath.Join(strings.TrimSpace(installRoot), "last-known-good")
}

func runtimeBootStatusPath(installRoot string) string {
	return filepath.Join(strings.TrimSpace(installRoot), "boot-status.json")
}

func markPendingRuntimeUpdate(installRoot, targetRoot, previousRoot, version string) error {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	targetRoot = filepath.Clean(strings.TrimSpace(targetRoot))
	previousRoot = strings.TrimSpace(previousRoot)
	if installRoot == "" || targetRoot == "" {
		return errors.New("install root and target root are required")
	}
	if previousRoot != "" {
		previousRoot = filepath.Clean(previousRoot)
		if previousRoot != targetRoot {
			if err := replaceSymlink(lastKnownGoodRuntimeLink(installRoot), previousRoot); err != nil {
				return fmt.Errorf("set last-known-good runtime link: %w", err)
			}
		}
	}
	if err := replaceSymlink(pendingRuntimeLink(installRoot), targetRoot); err != nil {
		return fmt.Errorf("set pending-target runtime link: %w", err)
	}
	return writeRuntimeBootStatus(installRoot, runtimeBootStatus{
		Version:     strings.TrimSpace(version),
		RuntimeRoot: targetRoot,
		State:       "pending",
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func markCurrentRuntimeBootSuccessful(installRoot string) error {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	if installRoot == "" {
		return errors.New("install root is required")
	}
	pendingRoot, pending := resolveRuntimeLink(pendingRuntimeLink(installRoot))
	if !pending {
		return nil
	}
	currentRoot, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok {
		return errors.New("current runtime link is missing")
	}
	if currentRoot != pendingRoot {
		return fmt.Errorf("pending runtime %s does not match current runtime %s", pendingRoot, currentRoot)
	}
	if err := replaceSymlink(lastKnownGoodRuntimeLink(installRoot), currentRoot); err != nil {
		return fmt.Errorf("set last-known-good runtime link: %w", err)
	}
	if err := removeIfExists(pendingRuntimeLink(installRoot)); err != nil {
		return err
	}
	return writeRuntimeBootStatus(installRoot, runtimeBootStatus{
		Version:     runtimeVersionFromRoot(currentRoot),
		RuntimeRoot: currentRoot,
		State:       "success",
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func rollbackPendingRuntimeUpdate(installRoot string, cause error) (string, error) {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	if installRoot == "" {
		return "", errors.New("install root is required")
	}
	pendingRoot, ok := resolveRuntimeLink(pendingRuntimeLink(installRoot))
	if !ok {
		return "", errors.New("pending runtime link is missing")
	}
	rollbackRoot := ""
	if candidate, ok := resolveRuntimeLink(lastKnownGoodRuntimeLink(installRoot)); ok && filepath.Clean(candidate) != pendingRoot {
		rollbackRoot = candidate
	}
	if rollbackRoot == "" {
		if candidate, ok := resolveRuntimeLink(filepath.Join(installRoot, "previous")); ok && filepath.Clean(candidate) != pendingRoot {
			rollbackRoot = candidate
		}
	}
	if rollbackRoot == "" {
		return "", errors.New("no rollback runtime is available")
	}
	if err := switchRuntimeLinks(installRoot, rollbackRoot); err != nil {
		return "", err
	}
	if err := installLauncherSymlinks(installRoot); err != nil {
		return "", err
	}
	if err := replaceSymlink(lastKnownGoodRuntimeLink(installRoot), rollbackRoot); err != nil {
		return "", fmt.Errorf("set last-known-good runtime link: %w", err)
	}
	if err := removeIfExists(pendingRuntimeLink(installRoot)); err != nil {
		return "", err
	}
	failure := "boot confirmation failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		failure = strings.TrimSpace(cause.Error())
	}
	if err := writeRuntimeBootStatus(installRoot, runtimeBootStatus{
		Version:     runtimeVersionFromRoot(pendingRoot),
		RuntimeRoot: pendingRoot,
		State:       "rolled_back",
		Failure:     failure,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return "", err
	}
	return rollbackRoot, nil
}

func rollbackPendingUpdateAndRestart(profile Profile, relaunchArgs []string, proc *os.Process, cause error) error {
	terminateProcess(proc, 5*time.Second)
	_ = StopBackend(profile)
	if _, err := rollbackPendingRuntimeUpdate(profile.InstallRoot, cause); err != nil {
		return err
	}
	cmd, err := startRuntimeCommand(profile, relaunchArgs, runtimeBootEnvironment())
	if err != nil {
		return err
	}
	return cmd.Start()
}

func waitForRuntimeBootConfirmation(installRoot, targetRoot string, waitCh <-chan error, timeout time.Duration) error {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	targetRoot = filepath.Clean(strings.TrimSpace(targetRoot))
	deadline := time.Now().Add(timeout)
	for {
		currentRoot, _ := resolveRuntimeLink(filepath.Join(installRoot, "current"))
		pendingRoot, pending := resolveRuntimeLink(pendingRuntimeLink(installRoot))
		if !pending {
			if currentRoot == targetRoot {
				return nil
			}
			return fmt.Errorf("updated runtime %s lost pending confirmation before becoming healthy", targetRoot)
		}
		if currentRoot != "" && currentRoot != pendingRoot {
			return fmt.Errorf("updated runtime %s was replaced before confirmation", targetRoot)
		}
		select {
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("updated runtime exited before becoming healthy: %w", err)
			}
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for updated runtime %s to become healthy", targetRoot)
		}
		time.Sleep(updatePollInterval)
	}
}

func writeRuntimeBootStatus(installRoot string, status runtimeBootStatus) error {
	installRoot = filepath.Clean(strings.TrimSpace(installRoot))
	if installRoot == "" {
		return errors.New("install root is required")
	}
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := runtimeBootStatusPath(installRoot)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func runtimeVersionFromRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	if version, err := readBuildInfoVersion(filepath.Join(root, "build-info.txt")); err == nil {
		return strings.TrimSpace(version)
	}
	data, err := os.ReadFile(filepath.Join(root, ".version"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func runtimeBootEnvironment() map[string]string {
	if value := strings.TrimSpace(os.Getenv(pendingUpdateBootstrapEnv)); value != "" {
		return map[string]string{pendingUpdateBootstrapEnv: value}
	}
	return nil
}

func terminateProcess(proc *os.Process, timeout time.Duration) {
	if proc == nil {
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(updatePollInterval)
	}
	_ = proc.Signal(syscall.SIGKILL)
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
