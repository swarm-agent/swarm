package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestSwitchRuntimeLinksReplacesLegacyTopLevelRuntimeDirs(t *testing.T) {
	installRoot := t.TempDir()
	targetRoot := filepath.Join(installRoot, "versions", "v1.2.3")
	for _, dir := range []string{"bin", "libexec", "lib", "share"} {
		if err := os.MkdirAll(filepath.Join(targetRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir target %s: %v", dir, err)
		}
		legacyDir := filepath.Join(installRoot, dir)
		if err := os.MkdirAll(legacyDir, 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(legacyDir, "legacy.txt"), []byte("old layout"), 0o644); err != nil {
			t.Fatalf("write legacy %s: %v", dir, err)
		}
	}

	if err := switchRuntimeLinks(installRoot, targetRoot); err != nil {
		t.Fatalf("switchRuntimeLinks: %v", err)
	}

	currentRoot, ok := resolveRuntimeLink(filepath.Join(installRoot, "current"))
	if !ok || currentRoot != targetRoot {
		t.Fatalf("current = %q ok=%v, want %q", currentRoot, ok, targetRoot)
	}
	for _, dir := range []string{"bin", "libexec", "lib", "share"} {
		linkPath := filepath.Join(installRoot, dir)
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("lstat %s: %v", linkPath, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink after switching runtime links; mode=%s", linkPath, info.Mode())
		}
		target, ok := resolveRuntimeLink(linkPath)
		want := filepath.Join(installRoot, "current", dir)
		if !ok || target != want {
			t.Fatalf("%s = %q ok=%v, want %q", linkPath, target, ok, want)
		}
	}
}

func TestReplaceSymlinkDoesNotRemoveUnexpectedDirectory(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "last-known-good")
	if err := os.MkdirAll(linkPath, 0o755); err != nil {
		t.Fatalf("mkdir existing dir: %v", err)
	}
	if err := replaceSymlink(linkPath, filepath.Join(root, "target")); err == nil {
		t.Fatalf("replaceSymlink should fail for an existing directory")
	}
	if info, err := os.Lstat(linkPath); err != nil || !info.IsDir() {
		t.Fatalf("existing directory should remain, info=%v err=%v", info, err)
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

func TestRunUpdateHelperDirectRestartStartsBackendThenRunsTUIForeground(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}

	originalStopBackend := stopBackendForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartBackend := startBackendForUpdate
	originalRunTUI := runTUIWithExtraEnvForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalRollbackRestart := rollbackPendingUpdateAndRestartForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		applyReleaseUpdateForUpdate = originalApplyRelease
		startBackendForUpdate = originalStartBackend
		runTUIWithExtraEnvForUpdate = originalRunTUI
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		rollbackPendingUpdateAndRestartForUpdate = originalRollbackRestart
	}()

	calls := []string{}
	stopBackendForUpdate = func(Profile) error {
		calls = append(calls, "stop")
		return nil
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		calls = append(calls, "apply")
		return result, nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		calls = append(calls, "start-backend")
		return nil
	}
	runTUIWithExtraEnvForUpdate = func(_ Profile, args []string, extraEnv map[string]string) error {
		calls = append(calls, "run-tui")
		if len(args) != 1 || args[0] != "main" {
			t.Fatalf("relaunch args = %v, want [main]", args)
		}
		if got := strings.TrimSpace(extraEnv[appliedUpdateToastEnv]); got != "Updated to v1.2.3" {
			t.Fatalf("toast env = %q", got)
		}
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindDirect}, true, nil
	}
	rollbackPendingUpdateAndRestartForUpdate = func(Profile, []string, *os.Process, error) error {
		t.Fatalf("rollback should not be called")
		return nil
	}

	if err := RunUpdateHelper(profile, plan, 0, []string{"main"}); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	want := "stop,apply,start-backend,run-tui"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestRunUpdateHelperSystemdStopsServiceBeforeApplyThenRunsTUIForeground(t *testing.T) {
	profile := Profile{InstallRoot: t.TempDir(), DataDir: t.TempDir()}
	plan := client.UpdateApplyPlan{TargetVersion: "v1.2.3"}
	result := UpdateResult{Version: "v1.2.3", RuntimeRoot: filepath.Join(profile.InstallRoot, "versions", "v1.2.3")}

	originalStopBackend := stopBackendForUpdate
	originalStopSystemd := stopSystemdServiceForUpdate
	originalApplyRelease := applyReleaseUpdateForUpdate
	originalStartBackend := startBackendForUpdate
	originalRunTUI := runTUIWithExtraEnvForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		stopSystemdServiceForUpdate = originalStopSystemd
		applyReleaseUpdateForUpdate = originalApplyRelease
		startBackendForUpdate = originalStartBackend
		runTUIWithExtraEnvForUpdate = originalRunTUI
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
	}()

	calls := []string{}
	stopBackendForUpdate = func(Profile) error {
		t.Fatalf("direct backend stop should not be used for active systemd service")
		return nil
	}
	stopSystemdServiceForUpdate = func(scope systemdServiceScope, unit string) error {
		calls = append(calls, "stop-systemd")
		if scope != systemdServiceSystem || unit != "swarm.service" {
			t.Fatalf("systemd stop = %s %s, want system swarm.service", scope, unit)
		}
		return nil
	}
	applyReleaseUpdateForUpdate = func(context.Context, Profile, client.UpdateApplyPlan) (UpdateResult, error) {
		calls = append(calls, "apply")
		return result, nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		calls = append(calls, "start-backend")
		return nil
	}
	runTUIWithExtraEnvForUpdate = func(Profile, []string, map[string]string) error {
		calls = append(calls, "run-tui")
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: "system", Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(scope systemdServiceScope, unit string) (bool, bool, error) {
		return true, true, nil
	}

	if err := RunUpdateHelper(profile, plan, 0, []string{"main"}); err != nil {
		t.Fatalf("RunUpdateHelper: %v", err)
	}
	want := "stop-systemd,apply,start-backend,run-tui"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}
