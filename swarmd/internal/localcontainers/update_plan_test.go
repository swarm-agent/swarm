package localcontainers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/buildinfo"
	"swarm-refactor/swarmtui/pkg/devmode"
	"swarm-refactor/swarmtui/pkg/localupdate"
	"swarm-refactor/swarmtui/pkg/startupconfig"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestUpdatePlanNoLocalContainers(t *testing.T) {
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: makeDevRoot(t)})
	defer cleanup()

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.PathID != PathContainerUpdatePlan {
		t.Fatalf("PathID = %q", plan.PathID)
	}
	if plan.Summary.Total != 0 || plan.Summary.Affected != 0 || len(plan.Containers) != 0 {
		t.Fatalf("summary = %+v containers=%d, want empty", plan.Summary, len(plan.Containers))
	}
	if plan.Contract.WarningCopy != "This will also update your local containers." {
		t.Fatalf("WarningCopy = %q", plan.Contract.WarningCopy)
	}
}

func TestUpdatePlanDevAlreadyCurrentAndStale(t *testing.T) {
	devRoot := makeDevRoot(t)
	expectedFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "current", "Current", defaultImageName)
	putLocalContainerRecord(t, svc.store, "stale", "Stale", defaultImageName)
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		switch runtimeName {
		case "podman":
			return runtimeImageInfo{ID: "sha256:current", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: expectedFingerprint}}, nil
		case "docker":
			return runtimeImageInfo{ID: "sha256:stale", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
		default:
			return runtimeImageInfo{}, errors.New("unexpected runtime")
		}
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Mode != "dev" || !plan.DevMode {
		t.Fatalf("mode=%q dev=%v, want dev", plan.Mode, plan.DevMode)
	}
	if plan.Target.ImageRef != defaultImageName || plan.Target.Fingerprint != expectedFingerprint {
		t.Fatalf("target = %+v, want image/fingerprint", plan.Target)
	}
	if plan.Summary.Total != 2 || plan.Summary.AlreadyCurrent != 1 || plan.Summary.NeedsUpdate != 1 || plan.Summary.Affected != 1 {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	states := updateStatesByID(plan)
	if states["current"] != "already-current" {
		t.Fatalf("current state = %q", states["current"])
	}
	if states["stale"] != "needs-update" {
		t.Fatalf("stale state = %q", states["stale"])
	}
}

func TestUpdatePlanProductionDigestTarget(t *testing.T) {
	oldVersion := buildinfo.Version
	oldCommit := buildinfo.Commit
	oldClient := productionImageMetadataClient
	oldTemplate := productionMetadataURLTmpl
	buildinfo.Version = "v1.2.3"
	buildinfo.Commit = "abcdef123456"
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Commit = oldCommit
		productionImageMetadataClient = oldClient
		productionMetadataURLTmpl = oldTemplate
	})
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			"image_ref=ghcr.io/swarm-agent/swarm:v1.2.3",
			"image_digest_ref=ghcr.io/swarm-agent/swarm@sha256:newdigest",
			"version=v1.2.3",
			"commit=abcdef123456",
			"source_revision=abcdef123456",
			"image_size_bytes=123",
		}, "\n")))
	}))
	defer metadataServer.Close()
	productionImageMetadataClient = metadataServer.Client()
	productionMetadataURLTmpl = metadataServer.URL + "/%s/container-image-info.txt"

	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: false})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "prod", "Prod", "ghcr.io/swarm-agent/swarm:v1.0.0")
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:prod", RepoDigests: []string{"ghcr.io/swarm-agent/swarm@sha256:olddigest"}}, nil
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Mode != "release" || plan.DevMode {
		t.Fatalf("mode=%q dev=%v, want release", plan.Mode, plan.DevMode)
	}
	if plan.Target.DigestRef != "ghcr.io/swarm-agent/swarm@sha256:newdigest" || plan.Target.Version != "v1.2.3" {
		t.Fatalf("target = %+v", plan.Target)
	}
	if plan.Summary.Total != 1 || plan.Summary.NeedsUpdate != 1 || plan.Summary.Affected != 1 {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	item := plan.Containers[0]
	if item.CurrentDigestRef != "ghcr.io/swarm-agent/swarm@sha256:olddigest" || item.TargetDigestRef != plan.Target.DigestRef {
		t.Fatalf("item digests current=%q target=%q", item.CurrentDigestRef, item.TargetDigestRef)
	}
}

func TestUpdatePlanContainerInspectFailure(t *testing.T) {
	devRoot := makeDevRoot(t)
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "broken", "Broken", defaultImageName)
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "created", "", errors.New("container inspect failed")
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Summary.Total != 1 || plan.Summary.Unknown != 1 || plan.Summary.Errors != 1 {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	item := plan.Containers[0]
	if item.State != "unknown" || item.Reason != "container_inspect_error" || !strings.Contains(item.Error, "container inspect failed") {
		t.Fatalf("item = %+v", item)
	}
}

func TestUpdatePlanImageInspectFailure(t *testing.T) {
	devRoot := makeDevRoot(t)
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "broken", "Broken", defaultImageName)
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{}, errors.New("image inspect failed")
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Summary.Total != 1 || plan.Summary.Unknown != 1 || plan.Summary.Errors != 1 {
		t.Fatalf("summary = %+v", plan.Summary)
	}
	item := plan.Containers[0]
	if item.State != "unknown" || item.Reason != "image_inspect_error" || !strings.Contains(item.Error, "image inspect failed") {
		t.Fatalf("item = %+v", item)
	}
}

func TestUpdatePlanDevPostRebuildTarget(t *testing.T) {
	devRoot := makeDevRoot(t)
	expectedFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "stale", "Stale", defaultImageName)
	if err := localupdate.WriteRebuildStatus(svc.dataDir, localupdate.RebuildStatus{Mode: "dev", ImageRef: defaultImageName, Fingerprint: expectedFingerprint}); err != nil {
		t.Fatalf("WriteRebuildStatus() error = %v", err)
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:stale", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: "old-fingerprint"}}, nil
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{PostRebuildCheck: true})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Target.PostRebuildImageRef != defaultImageName || plan.Target.PostRebuildFingerprint != expectedFingerprint {
		t.Fatalf("post-rebuild target = %+v", plan.Target)
	}
	item := plan.Containers[0]
	if item.TargetFingerprint != expectedFingerprint || item.State != "needs-update" {
		t.Fatalf("item after post-rebuild check = %+v", item)
	}
}

func TestUpdatePlanDevPostRebuildFallsBackToRuntimeInspect(t *testing.T) {
	devRoot := makeDevRoot(t)
	expectedFingerprint, err := devmode.ContainerImageFingerprint(devRoot)
	if err != nil {
		t.Fatalf("ContainerImageFingerprint() error = %v", err)
	}
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "stale", "Stale", defaultImageName)
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:rebuilt", Labels: map[string]string{devmode.ContainerImageFingerprintLabel: expectedFingerprint}}, nil
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{PostRebuildCheck: true})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	if plan.Target.PostRebuildImageRef != defaultImageName || plan.Target.PostRebuildFingerprint != expectedFingerprint {
		t.Fatalf("fallback post-rebuild target = %+v", plan.Target)
	}
}

func TestUpdatePlanPackageAwareDevImageDeferred(t *testing.T) {
	devRoot := makeDevRoot(t)
	svc, cleanup := newUpdatePlanTestService(t, startupconfig.FileConfig{DevMode: true, DevRoot: devRoot})
	defer cleanup()
	putLocalContainerRecord(t, svc.store, "pkg", "Packages", "localhost/swarm-container-mvp:pkg-abc123")
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:pkg", Labels: map[string]string{devmode.ContainerImageBaseFingerprintLabel: "base"}}, nil
	}

	plan, err := svc.UpdatePlan(context.Background(), UpdatePlanInput{})
	if err != nil {
		t.Fatalf("UpdatePlan() error = %v", err)
	}
	item := plan.Containers[0]
	if item.State != "unknown" || item.Reason != "package_aware_deferred" {
		t.Fatalf("package-aware item = %+v", item)
	}
}

func newUpdatePlanTestService(t *testing.T, cfg startupconfig.FileConfig) (*Service, func()) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(dataDir, "local-containers.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	containerStore := pebblestore.NewSwarmLocalContainerStore(store)
	configPath := filepath.Join(t.TempDir(), "swarm.conf")
	writeStartupConfig(t, configPath, cfg)
	svc := NewServiceWithDataDir(containerStore, nil, nil, nil, nil, configPath, dataDir)
	svc.inspectContainerFn = func(runtimeName, containerName string) (string, string, error) {
		return "running", "runtime-" + containerName, nil
	}
	svc.inspectImageFn = func(ctx context.Context, runtimeName, image string) (runtimeImageInfo, error) {
		return runtimeImageInfo{ID: "sha256:test", Labels: map[string]string{}}, nil
	}
	return svc, func() { _ = store.Close() }
}

func putLocalContainerRecord(t *testing.T, store *pebblestore.SwarmLocalContainerStore, id, name, image string) {
	t.Helper()
	runtimeName := "podman"
	if id == "stale" {
		runtimeName = "docker"
	}
	if _, err := store.Put(pebblestore.SwarmLocalContainerRecord{
		ID:            id,
		Name:          name,
		ContainerName: id,
		Runtime:       runtimeName,
		Status:        "running",
		ContainerID:   "stored-" + id,
		Image:         image,
	}); err != nil {
		t.Fatalf("put local container: %v", err)
	}
}

func writeStartupConfig(t *testing.T, path string, cfg startupconfig.FileConfig) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	lines := []string{
		"startup_mode = interactive",
		"dev_mode = " + boolString(cfg.DevMode),
		"host = 127.0.0.1",
		"port = 7781",
	}
	if strings.TrimSpace(cfg.DevRoot) != "" {
		lines = append(lines, "dev_root = "+cfg.DevRoot)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func makeDevRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "scripts", "rebuild-container.sh"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile.base"),
		filepath.Join(root, "deploy", "container-mvp", "Containerfile"),
		filepath.Join(root, "deploy", "container-mvp", "entrypoint.sh"),
		filepath.Join(root, ".bin", "main", "swarmd"),
		filepath.Join(root, ".bin", "main", "swarmctl"),
		filepath.Join(root, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so"),
		filepath.Join(root, "web", "dist", "index.html"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func updateStatesByID(plan UpdatePlan) map[string]string {
	out := map[string]string{}
	for _, item := range plan.Containers {
		out[item.ID] = item.State
	}
	return out
}
