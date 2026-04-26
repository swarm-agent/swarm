package localcontainers

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/devmode"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestReplaceRunningLocalContainerPreservesRecordAndUpdatesDeployment(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, deployStore, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	workspacePath := t.TempDir()
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:             "alpha",
		Name:           "Alpha",
		ContainerName:  "alpha",
		Runtime:        "podman",
		NetworkName:    "alpha-net",
		Status:         "running",
		ContainerID:    "old-container-id",
		HostAPIBaseURL: "http://host.containers.internal:7781",
		HostPort:       7783,
		RuntimePort:    7783,
		Image:          defaultImageName,
		Mounts: []pebblestore.SwarmLocalContainerMount{{
			SourcePath: workspacePath,
			TargetPath: "/workspaces/workspace",
			Mode:       pebblestore.ContainerMountModeReadWrite,
		}},
	})
	if _, err := deployStore.Put(pebblestore.DeployContainerRecord{
		ID:              "alpha",
		Name:            "Alpha",
		Runtime:         "podman",
		ContainerName:   "alpha",
		ContainerID:     "old-container-id",
		BackendHostPort: 7783,
		DesktopHostPort: 7784,
		Image:           defaultImageName,
		AttachStatus:    "attached",
		Status:          "running",
	}); err != nil {
		t.Fatalf("put deployment: %v", err)
	}

	var removed []string
	var renamed [][2]string
	var ran []runOptions
	svc.removeContainerFn = func(ctx context.Context, runtimeName, containerName string) error {
		removed = append(removed, containerName)
		return nil
	}
	svc.renameContainerFn = func(ctx context.Context, runtimeName, oldName, newName string) error {
		renamed = append(renamed, [2]string{oldName, newName})
		return nil
	}
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		ran = append(ran, opts)
		return "new-container-id", nil
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "running", "new-container-id", nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:" + strings.TrimPrefix(image, "sha256:"), Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:old", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
	}

	result, err := svc.Replace(context.Background(), ReplaceInput{
		ID: "alpha",
		Target: UpdatePlanTarget{
			PostRebuildImageRef:    defaultImageName,
			PostRebuildFingerprint: targetFingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if result.State != "replaced" || result.ContainerID != "new-container-id" || result.TargetFingerprint != targetFingerprint {
		t.Fatalf("result = %+v", result)
	}
	if len(renamed) != 1 || renamed[0][0] != "alpha" || !strings.Contains(renamed[0][1], "swarm-update-old") {
		t.Fatalf("renamed = %+v", renamed)
	}
	if len(ran) != 1 {
		t.Fatalf("run calls = %d", len(ran))
	}
	if got := ran[0]; got.ContainerName != "alpha" || got.NetworkName != "alpha-net" || got.HostPort != 7783 || got.Image != defaultImageName || len(got.Mounts) != 1 {
		t.Fatalf("run options = %+v", got)
	}
	if len(removed) < 2 || removed[len(removed)-1] != renamed[0][1] {
		t.Fatalf("removed = %+v backup=%q", removed, renamed[0][1])
	}
	stored, ok, err := svc.store.Get("alpha")
	if err != nil || !ok {
		t.Fatalf("get stored record ok=%v err=%v", ok, err)
	}
	if stored.ContainerID != "new-container-id" || stored.Image != defaultImageName || stored.Status != "running" || stored.HostPort != 7783 || stored.NetworkName != "alpha-net" {
		t.Fatalf("stored record = %+v", stored)
	}
	deployment, ok, err := deployStore.Get("alpha")
	if err != nil || !ok {
		t.Fatalf("get deployment ok=%v err=%v", ok, err)
	}
	if deployment.ContainerID != "new-container-id" || deployment.Image != defaultImageName || deployment.Status != "running" || deployment.BackendHostPort != 7783 || deployment.DesktopHostPort != 7784 {
		t.Fatalf("deployment = %+v", deployment)
	}
}

func TestReplaceStoppedLocalContainerStaysStopped(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:            "stopped",
		Name:          "Stopped",
		ContainerName: "stopped",
		Runtime:       "podman",
		Status:        "exited",
		ContainerID:   "old-container-id",
		HostPort:      7785,
		RuntimePort:   7785,
		Image:         defaultImageName,
	})

	var stopped bool
	svc.renameContainerFn = func(ctx context.Context, runtimeName, oldName, newName string) error { return nil }
	svc.removeContainerFn = func(ctx context.Context, runtimeName, containerName string) error { return nil }
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		return "new-stopped-id", nil
	}
	svc.controlContainerFn = func(ctx context.Context, runtimeName, action, containerName string) error {
		if action == "stop" && containerName == "stopped" {
			stopped = true
		}
		return nil
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "exited", "new-stopped-id", nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:old", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
	}

	result, err := svc.Replace(context.Background(), ReplaceInput{
		ID: "stopped",
		Target: UpdatePlanTarget{
			PostRebuildImageRef:    defaultImageName,
			PostRebuildFingerprint: targetFingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if result.State != "replaced" || result.Status != "exited" || !stopped {
		t.Fatalf("result=%+v stopped=%v", result, stopped)
	}
	stored, _, err := svc.store.Get("stopped")
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.Status != "exited" || stored.ContainerID != "new-stopped-id" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestReplaceAlreadyCurrentSkips(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:            "current",
		Name:          "Current",
		ContainerName: "current",
		Runtime:       "podman",
		Status:        "running",
		ContainerID:   "current-id",
		HostPort:      7787,
		RuntimePort:   7787,
		Image:         defaultImageName,
	})
	var ran bool
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		ran = true
		return "unexpected", nil
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "running", "current-id", nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}

	result, err := svc.Replace(context.Background(), ReplaceInput{
		ID: "current",
		Target: UpdatePlanTarget{
			PostRebuildImageRef:    defaultImageName,
			PostRebuildFingerprint: targetFingerprint,
		},
	})
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if result.State != "skipped" || result.Reason != "already-current" || ran {
		t.Fatalf("result=%+v ran=%v", result, ran)
	}
}

func TestReplaceFailureLeavesOldRecordUnderstandable(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:            "broken",
		Name:          "Broken",
		ContainerName: "broken",
		Runtime:       "podman",
		Status:        "running",
		ContainerID:   "old-broken-id",
		HostPort:      7789,
		RuntimePort:   7789,
		Image:         defaultImageName,
	})
	var restored bool
	var removed []string
	svc.renameContainerFn = func(ctx context.Context, runtimeName, oldName, newName string) error {
		if oldName != "broken" && newName == "broken" {
			restored = true
		}
		return nil
	}
	svc.removeContainerFn = func(ctx context.Context, runtimeName, containerName string) error {
		removed = append(removed, containerName)
		return nil
	}
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		return "", errors.New("new container failed")
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "running", "old-broken-id", nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:old", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
	}

	_, err = svc.Replace(context.Background(), ReplaceInput{
		ID: "broken",
		Target: UpdatePlanTarget{
			PostRebuildImageRef:    defaultImageName,
			PostRebuildFingerprint: targetFingerprint,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "new container failed") {
		t.Fatalf("Replace() error = %v", err)
	}
	if !restored {
		t.Fatal("old container was not restored after replacement failure")
	}
	if len(removed) < 2 || removed[len(removed)-1] != "broken" {
		t.Fatalf("removed after failed replace = %+v, want failed new container cleanup", removed)
	}
	stored, _, err := svc.store.Get("broken")
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.ContainerID != "old-broken-id" || stored.Image != defaultImageName || stored.Status != "running" {
		t.Fatalf("stored record changed after failed replace: %+v", stored)
	}
}

func newReplacementTestService(t *testing.T, cfg startupconfig.FileConfig) (*Service, *pebblestore.DeployContainerStore, func()) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dataDir, "local-containers.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	containerStore := pebblestore.NewSwarmLocalContainerStore(store)
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	configPath := filepath.Join(t.TempDir(), "swarm.conf")
	writeStartupConfig(t, configPath, cfg)
	svc := NewServiceWithDataDir(containerStore, deploymentStore, nil, nil, nil, configPath, dataDir)
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "running", "runtime-" + containerName, nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:test", Labels: map[string]string{}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:test", Labels: map[string]string{}}, nil
	}
	svc.inspectContainerEnvFn = func(ctx context.Context, runtimeName, containerName string) ([]string, error) {
		return nil, nil
	}
	svc.inspectContainerMountsFn = func(ctx context.Context, runtimeName, containerName string) ([]Mount, error) {
		return nil, nil
	}
	svc.inspectContainerRunArgsFn = func(ctx context.Context, runtimeName, containerName string) ([]string, error) {
		return nil, nil
	}
	return svc, deploymentStore, func() { _ = store.Close() }
}

func putReplaceRecord(t *testing.T, store *pebblestore.SwarmLocalContainerStore, record pebblestore.SwarmLocalContainerRecord) {
	t.Helper()
	if _, err := store.Put(record); err != nil {
		t.Fatalf("put local container: %v", err)
	}
}
