package launcher

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

func TestInstallRuntimeFromArtifactUsesVersionedCurrentLayout(t *testing.T) {
	artifactRoot := t.TempDir()
	platformRoot := filepath.Join(artifactRoot, runtime.GOOS+"-"+runtime.GOARCH)
	for _, dir := range []string{
		filepath.Join(platformRoot, "root"),
		filepath.Join(platformRoot, "swarmd"),
		filepath.Join(artifactRoot, "web", "assets"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup", "swarmtui"} {
		path := filepath.Join(platformRoot, "root", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, name := range []string{"swarmd", "swarmctl"} {
		path := filepath.Join(platformRoot, "swarmd", name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(platformRoot, "swarmd", "libfff_c.so"), []byte("fff"), 0o644); err != nil {
		t.Fatalf("write libfff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "web", "index.html"), []byte("<!doctype html><html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "web", "assets", "app.js"), []byte("console.log('artifact');"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	const version = "v1.2.3"
	if err := os.WriteFile(filepath.Join(artifactRoot, "build-info.txt"), []byte("version="+version+"\ncommit=test\n"), 0o644); err != nil {
		t.Fatalf("write build-info: %v", err)
	}

	xdgRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgRoot, "data"))
	t.Setenv("XDG_BIN_HOME", filepath.Join(xdgRoot, "bin"))

	report, err := InstallRuntimeFromArtifact(artifactRoot)
	if err != nil {
		t.Fatalf("InstallRuntimeFromArtifact: %v", err)
	}

	versionRoot := filepath.Join(xdgRoot, "data", "swarm", "versions", version)
	for _, rel := range []string{
		filepath.Join("libexec", "swarm"),
		filepath.Join("libexec", "swarmdev"),
		filepath.Join("libexec", "rebuild"),
		filepath.Join("libexec", "swarmsetup"),
		filepath.Join("bin", "swarmtui"),
		filepath.Join("bin", "swarmd"),
		filepath.Join("bin", "swarmctl"),
		filepath.Join("lib", "libfff_c.so"),
		filepath.Join("share", "index.html"),
		filepath.Join("share", "assets", "app.js"),
		filepath.Join("share", "assets", "app.js.gz"),
		"build-info.txt",
		".version",
	} {
		path := filepath.Join(versionRoot, rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	currentTarget, err := os.Readlink(filepath.Join(xdgRoot, "data", "swarm", "current"))
	if err != nil {
		t.Fatalf("read current symlink: %v", err)
	}
	if filepath.Clean(currentTarget) != filepath.Clean(versionRoot) {
		t.Fatalf("current -> %q, want %q", currentTarget, versionRoot)
	}
	linkPath := filepath.Join(report.BinHome, "swarm")
	targetPath, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink %s: %v", linkPath, err)
	}
	wantLauncher := filepath.Join(xdgRoot, "data", "swarm", "libexec", "swarm")
	if filepath.Clean(targetPath) != filepath.Clean(wantLauncher) {
		t.Fatalf("swarm link = %q, want %q", targetPath, wantLauncher)
	}
	if got := CurrentRuntimeVersion(filepath.Join(xdgRoot, "data", "swarm")); got != version {
		t.Fatalf("CurrentRuntimeVersion = %q, want %q", got, version)
	}
}

func TestMarkPendingRuntimeUpdateAndBootSuccess(t *testing.T) {
	installRoot := t.TempDir()
	previousRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	targetRoot := filepath.Join(installRoot, "versions", "v1.1.0")
	for _, root := range []string{previousRoot, targetRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
		version := filepath.Base(root)
		if err := os.WriteFile(filepath.Join(root, "build-info.txt"), []byte("version="+version+"\n"), 0o644); err != nil {
			t.Fatalf("write build-info %s: %v", root, err)
		}
	}
	if err := replaceSymlink(filepath.Join(installRoot, "current"), targetRoot); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if err := markPendingRuntimeUpdate(installRoot, targetRoot, previousRoot, "v1.1.0"); err != nil {
		t.Fatalf("markPendingRuntimeUpdate: %v", err)
	}
	pending, ok := resolveRuntimeLink(filepath.Join(installRoot, "pending-target"))
	if !ok || pending != targetRoot {
		t.Fatalf("pending-target = %q ok=%v, want %q", pending, ok, targetRoot)
	}
	lastKnownGood, ok := resolveRuntimeLink(filepath.Join(installRoot, "last-known-good"))
	if !ok || lastKnownGood != previousRoot {
		t.Fatalf("last-known-good = %q ok=%v, want %q", lastKnownGood, ok, previousRoot)
	}
	if err := markCurrentRuntimeBootSuccessful(installRoot); err != nil {
		t.Fatalf("markCurrentRuntimeBootSuccessful: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "pending-target")); !os.IsNotExist(err) {
		t.Fatalf("pending-target should be removed after successful boot, err=%v", err)
	}
	lastKnownGood, ok = resolveRuntimeLink(filepath.Join(installRoot, "last-known-good"))
	if !ok || lastKnownGood != targetRoot {
		t.Fatalf("last-known-good after success = %q ok=%v, want %q", lastKnownGood, ok, targetRoot)
	}
}

func TestRollbackPendingRuntimeUpdateRestoresPreviousRuntime(t *testing.T) {
	installRoot := t.TempDir()
	xdgRoot := t.TempDir()
	t.Setenv("XDG_BIN_HOME", filepath.Join(xdgRoot, "bin"))
	previousRoot := filepath.Join(installRoot, "versions", "v1.0.0")
	targetRoot := filepath.Join(installRoot, "versions", "v1.1.0")
	for _, root := range []string{previousRoot, targetRoot} {
		for _, dir := range []string{"bin", "libexec", "lib", "share"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatalf("mkdir %s/%s: %v", root, dir, err)
			}
		}
		for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
			path := filepath.Join(root, "libexec", name)
			if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
		version := filepath.Base(root)
		if err := os.WriteFile(filepath.Join(root, "build-info.txt"), []byte("version="+version+"\n"), 0o644); err != nil {
			t.Fatalf("write build-info %s: %v", root, err)
		}
	}
	if err := switchRuntimeLinks(installRoot, targetRoot); err != nil {
		t.Fatalf("switchRuntimeLinks target: %v", err)
	}
	if err := markPendingRuntimeUpdate(installRoot, targetRoot, previousRoot, "v1.1.0"); err != nil {
		t.Fatalf("markPendingRuntimeUpdate: %v", err)
	}
	rolledBackRoot, err := rollbackPendingRuntimeUpdate(installRoot, errors.New("boom"))
	if err != nil {
		t.Fatalf("rollbackPendingRuntimeUpdate: %v", err)
	}
	if rolledBackRoot != previousRoot {
		t.Fatalf("rolledBackRoot = %q, want %q", rolledBackRoot, previousRoot)
	}
	currentRoot, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || currentRoot != previousRoot {
		t.Fatalf("current after rollback = %q ok=%v, want %q", currentRoot, ok, previousRoot)
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "pending-target")); !os.IsNotExist(err) {
		t.Fatalf("pending-target should be removed after rollback, err=%v", err)
	}
}

func TestRunUpdateHelperDirectRestartLaunchesFullApp(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}

	originalStopBackend := stopBackendForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartRuntime := startRuntimeCommandForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalWaitForBoot := waitForRuntimeBootConfirmationForUpdate
	originalRollbackRestart := rollbackPendingUpdateAndRestartForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		applyReleaseUpdateForUpdate = originalApplyRelease
		startRuntimeCommandForUpdate = originalStartRuntime
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		waitForRuntimeBootConfirmationForUpdate = originalWaitForBoot
		rollbackPendingUpdateAndRestartForUpdate = originalRollbackRestart
	}()

	stopBackendForUpdate = func(Profile) error { return nil }
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		return result, nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindDirect}, true, nil
	}
	started := false
	startRuntimeCommandForUpdate = func(_ Profile, args []string, extraEnv map[string]string) (*exec.Cmd, error) {
		if len(args) != 1 || args[0] != "main" {
			t.Fatalf("relaunch args = %v, want [main]", args)
		}
		if got := strings.TrimSpace(extraEnv[appliedUpdateToastEnv]); got != "Updated to v1.2.3" {
			t.Fatalf("toast env = %q", got)
		}
		if _, ok := extraEnv[pendingUpdateBootstrapEnv]; ok {
			t.Fatalf("direct restart should not set %s", pendingUpdateBootstrapEnv)
		}
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		started = true
		return cmd, nil
	}
	waitForRuntimeBootConfirmationForUpdate = func(installRoot, targetRoot string, waitCh <-chan error, timeout time.Duration) error {
		if installRoot != profile.InstallRoot {
			t.Fatalf("installRoot = %q, want %q", installRoot, profile.InstallRoot)
		}
		if targetRoot != result.RuntimeRoot {
			t.Fatalf("targetRoot = %q, want %q", targetRoot, result.RuntimeRoot)
		}
		if timeout != updateBootWaitLimit {
			t.Fatalf("timeout = %v, want %v", timeout, updateBootWaitLimit)
		}
		if err := <-waitCh; err != nil {
			t.Fatalf("waitCh err = %v", err)
		}
		return nil
	}
	rollbackPendingUpdateAndRestartForUpdate = func(Profile, []string, *os.Process, error) error {
		t.Fatalf("rollback should not be called")
		return nil
	}

	if err := RunUpdateHelper(profile, plan, 0, []string{"main"}); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	if !started {
		t.Fatalf("expected restart command to be started")
	}
}

func TestRunUpdateHelperSystemdBlockedPrintsManualRestartMessage(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}

	originalStopBackend := stopBackendForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRestartSystemd := restartSystemdServiceForUpdate
	originalStdout := os.Stdout
	defer func() {
		stopBackendForUpdate = originalStopBackend
		applyReleaseUpdateForUpdate = originalApplyRelease
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		restartSystemdServiceForUpdate = originalRestartSystemd
		os.Stdout = originalStdout
	}()

	stopBackendForUpdate = func(Profile) error { return nil }
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		return result, nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: "system", Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(scope systemdServiceScope, unit string) (bool, bool, error) {
		return true, true, nil
	}
	restartCalled := false
	restartSystemdServiceForUpdate = func(scope systemdServiceScope, unit string, restart bool) error {
		restartCalled = true
		return errors.New("sudo not found; cannot manage systemd system service")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := RunUpdateHelper(profile, plan, 0, []string{"main"})
	_ = w.Close()
	if runErr != nil {
		t.Fatalf("RunUpdateHelper: %v", runErr)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	_ = r.Close()
	output := string(data)

	if !restartCalled {
		t.Fatalf("expected systemd restart attempt")
	}
	if !strings.Contains(output, "Swarm updated to v1.2.3, but automatic restart could not be completed.") {
		t.Fatalf("missing manual restart summary: %q", output)
	}
	if !strings.Contains(output, "Automatic restart was blocked: sudo not found; cannot manage systemd system service") {
		t.Fatalf("missing blocked reason: %q", output)
	}
	if !strings.Contains(output, "Please restart Swarm manually.") {
		t.Fatalf("missing manual restart instruction: %q", output)
	}
}
