package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestRunDevUpdateReconcilesSystemdUnitAfterLauncherInstallBeforeRestart(t *testing.T) {
	originalStopBackend := stopBackendForUpdate
	originalStartBackend := startBackendForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRestartSystemd := restartSystemdServiceForUpdate
	originalPreflight := preflightDevUpdateForUpdate
	originalBuildSwarmd := buildSwarmdBinariesForUpdate
	originalForceBuildTools := forceBuildToolBinariesForUpdate
	originalBuildTUI := buildSwarmTUIForUpdate
	originalWebNeedsRebuild := devFrontendAssetsNeedRebuildForUpdate
	originalBuildWeb := buildAndInstallWebAssetsForUpdate
	originalInstallLaunchers := installLaunchersForUpdate
	originalEnsureUnit := ensureSystemdServiceUnitForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		startBackendForUpdate = originalStartBackend
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		restartSystemdServiceForUpdate = originalRestartSystemd
		preflightDevUpdateForUpdate = originalPreflight
		buildSwarmdBinariesForUpdate = originalBuildSwarmd
		forceBuildToolBinariesForUpdate = originalForceBuildTools
		buildSwarmTUIForUpdate = originalBuildTUI
		devFrontendAssetsNeedRebuildForUpdate = originalWebNeedsRebuild
		buildAndInstallWebAssetsForUpdate = originalBuildWeb
		installLaunchersForUpdate = originalInstallLaunchers
		ensureSystemdServiceUnitForUpdate = originalEnsureUnit
	}()

	calls := []string{}
	profile := newDevUpdateTestProfile(t)

	preflightDevUpdateForUpdate = func(Profile) error {
		calls = append(calls, "preflight")
		return nil
	}
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		calls = append(calls, "resolve-lifecycle")
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: string(systemdServiceSystem), Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(scope systemdServiceScope, unit string) (bool, bool, error) {
		calls = append(calls, "service-active")
		if scope != systemdServiceSystem || unit != "swarm.service" {
			t.Fatalf("serviceActiveForUpdate = %s %s, want system swarm.service", scope, unit)
		}
		return true, true, nil
	}
	stopBackendForUpdate = func(Profile) error {
		calls = append(calls, "stop")
		return nil
	}
	buildSwarmdBinariesForUpdate = func(Profile) error {
		calls = append(calls, "build-swarmd")
		return nil
	}
	forceBuildToolBinariesForUpdate = func(root string, skip map[string]bool) error {
		calls = append(calls, "build-tools")
		if !skip["rebuild"] {
			t.Fatalf("ForceBuildToolBinaries skip = %v, want rebuild skipped", skip)
		}
		return nil
	}
	buildSwarmTUIForUpdate = func(Profile) error {
		calls = append(calls, "build-tui")
		return nil
	}
	devFrontendAssetsNeedRebuildForUpdate = func(Profile) (bool, error) {
		calls = append(calls, "web-check")
		return false, nil
	}
	buildAndInstallWebAssetsForUpdate = func(Profile) error {
		t.Fatalf("web asset build should not run when assets are current")
		return nil
	}
	installLaunchersForUpdate = func(string) (InstallReport, error) {
		calls = append(calls, "install-launchers")
		return InstallReport{}, nil
	}
	ensureSystemdServiceUnitForUpdate = func() error {
		calls = append(calls, "ensure-unit")
		return nil
	}
	restartSystemdServiceForUpdate = func(scope systemdServiceScope, unit string, restart bool) error {
		calls = append(calls, "restart-systemd")
		if scope != systemdServiceSystem || unit != "swarm.service" || !restart {
			t.Fatalf("restartSystemdServiceForUpdate = %s %s %v, want system swarm.service true", scope, unit, restart)
		}
		return nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		t.Fatalf("direct backend start should not run for systemd restart plan")
		return nil
	}
	if err := RunDevUpdate(profile, nil); err != nil {
		t.Fatalf("RunDevUpdate: %v", err)
	}

	installIndex := indexOfString(calls, "install-launchers")
	ensureIndex := indexOfString(calls, "ensure-unit")
	restartIndex := indexOfString(calls, "restart-systemd")
	if installIndex < 0 || ensureIndex < 0 || restartIndex < 0 {
		t.Fatalf("calls missing install/ensure/restart: %v", calls)
	}
	if !(installIndex < ensureIndex && ensureIndex < restartIndex) {
		t.Fatalf("systemd unit reconciliation order wrong: calls=%v", calls)
	}
	want := []string{"preflight", "resolve-lifecycle", "service-active", "stop", "build-swarmd", "build-tools", "build-tui", "web-check", "install-launchers", "ensure-unit", "restart-systemd"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestRunDevUpdatePreflightsBeforeStoppingBackend(t *testing.T) {
	originalStopBackend := stopBackendForUpdate
	originalPreflight := preflightDevUpdateForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		preflightDevUpdateForUpdate = originalPreflight
	}()

	profile := newDevUpdateTestProfile(t)
	preflightDevUpdateForUpdate = func(Profile) error { return errors.New("missing Go toolchain") }
	stopBackendForUpdate = func(Profile) error {
		t.Fatalf("backend must not stop after preflight failure")
		return nil
	}

	err := RunDevUpdate(profile, nil)
	if err == nil {
		t.Fatalf("RunDevUpdate succeeded; want preflight failure")
	}
	if !strings.Contains(err.Error(), "missing Go toolchain") {
		t.Fatalf("error = %q, want Go preflight failure", err)
	}
}

func TestRunDevUpdateFailsClearlyWhenSystemdUnitReconcileFails(t *testing.T) {
	originalStopBackend := stopBackendForUpdate
	originalStartBackend := startBackendForUpdate
	originalResolveLifecycle := resolveLifecycleManagerForUpdate
	originalServiceActive := serviceActiveForUpdate
	originalRestartSystemd := restartSystemdServiceForUpdate
	originalPreflight := preflightDevUpdateForUpdate
	originalBuildSwarmd := buildSwarmdBinariesForUpdate
	originalForceBuildTools := forceBuildToolBinariesForUpdate
	originalBuildTUI := buildSwarmTUIForUpdate
	originalWebNeedsRebuild := devFrontendAssetsNeedRebuildForUpdate
	originalBuildWeb := buildAndInstallWebAssetsForUpdate
	originalInstallLaunchers := installLaunchersForUpdate
	originalEnsureUnit := ensureSystemdServiceUnitForUpdate
	defer func() {
		stopBackendForUpdate = originalStopBackend
		startBackendForUpdate = originalStartBackend
		resolveLifecycleManagerForUpdate = originalResolveLifecycle
		serviceActiveForUpdate = originalServiceActive
		restartSystemdServiceForUpdate = originalRestartSystemd
		preflightDevUpdateForUpdate = originalPreflight
		buildSwarmdBinariesForUpdate = originalBuildSwarmd
		forceBuildToolBinariesForUpdate = originalForceBuildTools
		buildSwarmTUIForUpdate = originalBuildTUI
		devFrontendAssetsNeedRebuildForUpdate = originalWebNeedsRebuild
		buildAndInstallWebAssetsForUpdate = originalBuildWeb
		installLaunchersForUpdate = originalInstallLaunchers
		ensureSystemdServiceUnitForUpdate = originalEnsureUnit
	}()

	profile := newDevUpdateTestProfile(t)

	preflightDevUpdateForUpdate = func(Profile) error { return nil }
	resolveLifecycleManagerForUpdate = func(Profile) (lifecycleManager, bool, error) {
		return lifecycleManager{Kind: lifecycleKindSystemd, Scope: string(systemdServiceSystem), Unit: "swarm.service"}, true, nil
	}
	serviceActiveForUpdate = func(systemdServiceScope, string) (bool, bool, error) { return true, true, nil }
	stopBackendForUpdate = func(Profile) error { return nil }
	buildSwarmdBinariesForUpdate = func(Profile) error { return nil }
	forceBuildToolBinariesForUpdate = func(string, map[string]bool) error { return nil }
	buildSwarmTUIForUpdate = func(Profile) error { return nil }
	devFrontendAssetsNeedRebuildForUpdate = func(Profile) (bool, error) { return false, nil }
	buildAndInstallWebAssetsForUpdate = func(Profile) error { return nil }
	installLaunchersForUpdate = func(string) (InstallReport, error) { return InstallReport{}, nil }
	ensureSystemdServiceUnitForUpdate = func() error { return errors.New("unit denied") }
	restartSystemdServiceForUpdate = func(systemdServiceScope, string, bool) error {
		t.Fatalf("systemd restart should not run after unit reconciliation failure")
		return nil
	}
	startBackendForUpdate = func(Profile, StartBackendOptions) error {
		t.Fatalf("direct backend start should not run after unit reconciliation failure")
		return nil
	}
	err := RunDevUpdate(profile, nil)
	if err == nil {
		t.Fatalf("RunDevUpdate succeeded; want unit reconciliation failure")
	}
	if !strings.Contains(err.Error(), "reconcile systemd service unit after launcher install") || !strings.Contains(err.Error(), "unit denied") {
		t.Fatalf("error = %q, want clear reconcile failure", err)
	}
}

func newDevUpdateTestProfile(t *testing.T) Profile {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "scripts"),
		filepath.Join(root, "web", "dist"),
		filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	binDir := t.TempDir()
	for _, name := range []string{"swarmd", "swarmctl"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"), []byte("fff"), 0o644); err != nil {
		t.Fatalf("write libfff: %v", err)
	}
	return Profile{
		Root:    root,
		BinDir:  binDir,
		Startup: startupconfig.FileConfig{DevMode: true},
		DataDir: t.TempDir(),
		URL:     "",
	}
}

func indexOfString(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}
