package localcontainers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/devmode"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestRunDevUpdateJobReplacesNeedsUpdateAndSkipsCurrent(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:            "stale",
		Name:          "Stale",
		ContainerName: "stale",
		Runtime:       "podman",
		Status:        "running",
		ContainerID:   "old-stale-id",
		HostPort:      7801,
		RuntimePort:   7801,
		Image:         defaultImageName,
	})
	putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
		ID:            "current",
		Name:          "Current",
		ContainerName: "current",
		Runtime:       "podman",
		Status:        "running",
		ContainerID:   "current-id",
		HostPort:      7803,
		RuntimePort:   7803,
		Image:         defaultImageName,
	})
	var ran []string
	svc.renameContainerFn = func(ctx context.Context, runtimeName, oldName, newName string) error { return nil }
	svc.removeContainerFn = func(ctx context.Context, runtimeName, containerName string) error { return nil }
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		ran = append(ran, opts.ContainerName)
		return "new-" + opts.ContainerName + "-id", nil
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		if containerName == "stale" {
			return "running", "new-stale-id", nil
		}
		return "running", "current-id", nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		fingerprint := targetFingerprint
		if containerName == "stale" {
			fingerprint = "old-fingerprint"
		}
		return runtimeImageInfo{ID: "sha256:" + containerName, Labels: map[string]string{devmode.ContainerImageFingerprintLabel: fingerprint}}, nil
	}

	result, err := svc.RunUpdateJob(context.Background(), UpdateJobInput{PostRebuildCheck: true})
	if err != nil {
		t.Fatalf("RunUpdateJob() error = %v", err)
	}
	if result.DevMode != true || result.Target.PostRebuildFingerprint != targetFingerprint {
		t.Fatalf("result target = %+v dev=%v", result.Target, result.DevMode)
	}
	if result.Summary.Total != 2 || result.Summary.Replaced != 1 || result.Summary.Skipped != 1 || result.Summary.Failed != 0 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	if len(ran) != 1 || ran[0] != "stale" {
		t.Fatalf("run calls = %+v", ran)
	}
	states := updateJobStatesByID(result)
	if states["stale"] != "replaced" || states["current"] != "skipped" {
		t.Fatalf("states = %+v", states)
	}
	stored, _, err := svc.store.Get("stale")
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if stored.ContainerID != "new-stale-id" || stored.Status != "running" {
		t.Fatalf("stored stale = %+v", stored)
	}
}

func TestRunDevUpdateJobContinuesAfterPartialFailure(t *testing.T) {
	devRoot := makeDevRoot(t)
	targetFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	for _, id := range []string{"broken", "ok"} {
		putReplaceRecord(t, svc.store, pebblestore.SwarmLocalContainerRecord{
			ID:            id,
			Name:          id,
			ContainerName: id,
			Runtime:       "podman",
			Status:        "running",
			ContainerID:   "old-" + id,
			HostPort:      7810,
			RuntimePort:   7810,
			Image:         defaultImageName,
		})
	}
	svc.renameContainerFn = func(ctx context.Context, runtimeName, oldName, newName string) error { return nil }
	svc.removeContainerFn = func(ctx context.Context, runtimeName, containerName string) error { return nil }
	svc.runContainerFn = func(ctx context.Context, runtimeName string, opts runOptions) (string, error) {
		if opts.ContainerName == "broken" {
			return "", errors.New("boom")
		}
		return "new-ok", nil
	}
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		if containerName == "ok" {
			return "running", "new-ok", nil
		}
		return "running", "old-" + containerName, nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:target", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: targetFingerprint}}, nil
	}
	svc.inspectContainerImageFn = func(ctx context.Context, runtimeName, containerName string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:old", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
	}

	result, err := svc.RunUpdateJob(context.Background(), UpdateJobInput{PostRebuildCheck: true})
	if err == nil || !strings.Contains(err.Error(), "failed for 1") {
		t.Fatalf("RunUpdateJob() error = %v", err)
	}
	if result.Summary.Total != 2 || result.Summary.Replaced != 1 || result.Summary.Failed != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	states := updateJobStatesByID(result)
	if states["broken"] != "failed" || states["ok"] != "replaced" {
		t.Fatalf("states = %+v", states)
	}
}

func TestRunDevUpdateJobNoContainerNoop(t *testing.T) {
	devRoot := makeDevRoot(t)
	svc, _, cleanup := newReplacementTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	result, err := svc.RunUpdateJob(context.Background(), UpdateJobInput{PostRebuildCheck: true})
	if err != nil {
		t.Fatalf("RunUpdateJob() error = %v", err)
	}
	if result.Summary.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func updateJobStatesByID(result UpdateJobResult) map[string]string {
	out := map[string]string{}
	for _, item := range result.Items {
		out[item.ID] = item.State
	}
	return out
}
