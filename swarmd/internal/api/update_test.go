package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/localupdate"
	"swarm-refactor/swarmtui/pkg/startupconfig"
)

func TestConfiguredDevRootUsesStartupConfigDevRoot(t *testing.T) {
	root := makeUpdateDevRoot(t)
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	got, err := server.configuredDevRoot()
	if err != nil {
		t.Fatalf("configuredDevRoot() error = %v", err)
	}
	if got != root {
		t.Fatalf("configuredDevRoot() = %q, want %q", got, root)
	}
}

func TestConfiguredDevRootRequiresConfiguredDevRoot(t *testing.T) {
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	if _, err := server.configuredDevRoot(); err == nil {
		t.Fatal("configuredDevRoot() error = nil, want missing dev_root error")
	}
}

func TestDesktopUpdateKindUsesStartupConfigDevMode(t *testing.T) {
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{startupConfigPath: startupPath}
	got, err := server.desktopUpdateKind()
	if err != nil {
		t.Fatalf("desktopUpdateKind() error = %v", err)
	}
	if got != updateKindDev {
		t.Fatalf("desktopUpdateKind() = %q, want %q", got, updateKindDev)
	}

	cfg.DevMode = false
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	got, err = server.desktopUpdateKind()
	if err != nil {
		t.Fatalf("desktopUpdateKind() release error = %v", err)
	}
	if got != updateKindRelease {
		t.Fatalf("desktopUpdateKind() = %q, want %q", got, updateKindRelease)
	}
}

func TestUpdateLaneForKindUsesCurrentLane(t *testing.T) {
	t.Setenv("SWARM_LANE", "dev")
	if got := updateLaneForKind(updateKindDev); got != "dev" {
		t.Fatalf("updateLaneForKind(dev) = %q, want dev", got)
	}
	t.Setenv("SWARM_LANE", "main")
	if got := updateLaneForKind(updateKindDev); got != "main" {
		t.Fatalf("updateLaneForKind(dev on main lane) = %q, want main", got)
	}
	t.Setenv("SWARM_LANE", "")
	if got := updateLaneForKind(updateKindRelease); got != "main" {
		t.Fatalf("updateLaneForKind(release) = %q, want main", got)
	}
}

func TestStartDetachedUpdateCommandDevRequiresGoBeforeLaunch(t *testing.T) {
	root := makeUpdateDevRoot(t)
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{dataDir: t.TempDir(), startupConfigPath: startupPath}

	originalResolveLauncher := resolveSwarmLauncherPathForUpdate
	originalPrepareLaunch := prepareUpdateHelperLaunchForUpdate
	originalLookPath := execLookPathForUpdate
	defer func() {
		resolveSwarmLauncherPathForUpdate = originalResolveLauncher
		prepareUpdateHelperLaunchForUpdate = originalPrepareLaunch
		execLookPathForUpdate = originalLookPath
	}()
	t.Setenv("GO_BIN", filepath.Join(root, "missing-go"))
	resolveSwarmLauncherPathForUpdate = func() (string, error) { return "/bin/echo", nil }
	execLookPathForUpdate = func(name string) (string, error) {
		if name == "go" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + name, nil
	}
	prepareUpdateHelperLaunchForUpdate = func(updateHelperLaunchConfig) (updateHelperLaunchCommand, error) {
		t.Fatalf("prepareUpdateHelperLaunch should not run when Go preflight fails")
		return updateHelperLaunchCommand{}, nil
	}

	_, err := server.startDetachedUpdateCommand(context.Background(), updateKindDev, "job-no-go", &updateJobRunner{})
	if err == nil {
		t.Fatalf("startDetachedUpdateCommand succeeded; want Go preflight error")
	}
	if !strings.Contains(err.Error(), "dev update requires Go toolchain before stopping Swarm") || !strings.Contains(err.Error(), "missing Go toolchain") {
		t.Fatalf("error = %q, want clear Go preflight failure", err)
	}
}

func TestStartDetachedUpdateCommandDevPassesGoEnvToSystemdRun(t *testing.T) {
	root := makeUpdateDevRoot(t)
	goBin := filepath.Join(root, ".tools", "go", "bin", "go")
	if err := os.MkdirAll(filepath.Dir(goBin), 0o755); err != nil {
		t.Fatalf("mkdir go bin: %v", err)
	}
	if err := os.WriteFile(goBin, []byte("#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = GOROOT ]; then echo /tmp/test-goroot; exit 0; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write go shim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(goBin), "gofmt"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write gofmt shim: %v", err)
	}
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	cfg.DevRoot = root
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{dataDir: t.TempDir(), startupConfigPath: startupPath}

	originalResolveLauncher := resolveSwarmLauncherPathForUpdate
	originalPrepareLaunch := prepareUpdateHelperLaunchForUpdate
	defer func() {
		resolveSwarmLauncherPathForUpdate = originalResolveLauncher
		prepareUpdateHelperLaunchForUpdate = originalPrepareLaunch
	}()
	t.Setenv("GO_BIN", goBin)
	resolveSwarmLauncherPathForUpdate = func() (string, error) { return "/bin/sleep", nil }
	prepareUpdateHelperLaunchForUpdate = func(cfg updateHelperLaunchConfig) (updateHelperLaunchCommand, error) {
		assertStringInSlice(t, cfg.Env, "SWARM_ROOT="+root)
		assertStringInSlice(t, cfg.Env, "GO_BIN="+goBin)
		assertStringInSlice(t, cfg.Env, "GOFMT_BIN="+filepath.Join(filepath.Dir(goBin), "gofmt"))
		assertStringInSlice(t, cfg.Env, "GOROOT=/tmp/test-goroot")
		assertStringInSlice(t, cfg.Env, "GOTOOLCHAIN=auto")
		return updateHelperLaunchCommand{CommandPath: "/bin/sleep", Args: []string{"60"}, Env: os.Environ()}, nil
	}

	details, err := server.startDetachedUpdateCommand(context.Background(), updateKindDev, "job-with-go", &updateJobRunner{})
	if err != nil {
		t.Fatalf("startDetachedUpdateCommand: %v", err)
	}
	if details.HelperPID <= 0 {
		t.Fatalf("helper pid = %d, want positive", details.HelperPID)
	}
	proc, err := os.FindProcess(details.HelperPID)
	if err == nil {
		_ = proc.Kill()
	}
}

func TestUpdateRunnerAllowsRetryAfterPersistedFailureForSameKind(t *testing.T) {
	originalResolveLauncher := resolveSwarmLauncherPathForUpdate
	originalPrepareLaunch := prepareUpdateHelperLaunchForUpdate
	defer func() {
		resolveSwarmLauncherPathForUpdate = originalResolveLauncher
		prepareUpdateHelperLaunchForUpdate = originalPrepareLaunch
	}()

	dataDir := t.TempDir()
	failed := desktopUpdateJob{
		ID:     "job-1",
		Kind:   updateKindDev,
		Status: updateJobStatusFailed,
		Error:  "previous dev update failed",
	}
	if err := localupdate.WriteUpdateJobStatus(dataDir, localupdate.UpdateJobStatus{ID: failed.ID, Kind: failed.Kind, Status: failed.Status, Error: failed.Error}); err != nil {
		t.Fatalf("WriteUpdateJobStatus: %v", err)
	}
	runner := &updateJobRunner{current: desktopUpdateJob{
		ID:     failed.ID,
		Kind:   updateKindDev,
		Status: updateJobStatusRunning,
	}}

	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.DevMode = true
	cfg.DevRoot = makeUpdateDevRoot(t)
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("Write startup config: %v", err)
	}
	server := &Server{dataDir: dataDir, startupConfigPath: startupPath}

	resolveSwarmLauncherPathForUpdate = func() (string, error) { return "/bin/echo", nil }
	prepareUpdateHelperLaunchForUpdate = func(cfg updateHelperLaunchConfig) (updateHelperLaunchCommand, error) {
		return updateHelperLaunchCommand{}, errors.New("stop after retry reached launcher")
	}

	_, err := runner.Start(t.Context(), server)
	if err == nil {
		t.Fatalf("Start succeeded; want launcher error after stale running state is cleared")
	}
	if !strings.Contains(err.Error(), "stop after retry reached launcher") {
		t.Fatalf("Start error = %q, want launcher error", err)
	}
	status, ok, err := localupdate.ReadUpdateJobStatusPath(localupdate.UpdateJobStatusPath(dataDir))
	if err != nil {
		t.Fatalf("ReadUpdateJobStatusPath: %v", err)
	}
	if !ok {
		t.Fatalf("persisted update status missing")
	}
	if status.ID == failed.ID {
		t.Fatalf("runner reused failed job id %q instead of starting a retry", status.ID)
	}
	if status.Status != updateJobStatusFailed || !strings.Contains(status.Error, "stop after retry reached launcher") {
		t.Fatalf("persisted status = %+v, want retry failure from launcher", status)
	}
}

func makeUpdateDevRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	files := []string{
		filepath.Join(root, "scripts", "rebuild-container.sh"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile.base"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile"),
		filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"),
	}
	for _, path := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("test\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	return filepath.Clean(root)
}
