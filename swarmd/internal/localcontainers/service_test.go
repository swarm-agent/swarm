package localcontainers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm-refactor/swarmtui/pkg/buildinfo"
)

func TestIsMissingRuntimeContainerErrorAcceptsPodmanNoSuchObject(t *testing.T) {
	err := errors.New(`remove podman container: Error: no such object: "pc-container"`)
	if !IsMissingRuntimeContainerError(err) {
		t.Fatalf("IsMissingRuntimeContainerError(%q) = false, want true", err.Error())
	}
}

func TestCurrentRuntimeMountFallsBackToSharedRuntimeFFFLibWhenRepoRuntimeMissing(t *testing.T) {
	repoRoot := t.TempDir()
	sharedRoot := t.TempDir()
	t.Chdir(repoRoot)
	t.Setenv("SWARM_SHARED_RUNTIME_ROOT", sharedRoot)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("SWARM_ROOT", "")
	t.Setenv("SWARM_GO_ROOT", "")
	t.Setenv("STARTUP_CWD", "")
	t.Setenv("SWARM_WEB_DIR", "")

	repoLibPath := filepath.Join(repoRoot, "swarmd", "internal", "fff", "lib", "linux-amd64-gnu", "libfff_c.so")
	if err := os.MkdirAll(filepath.Dir(repoLibPath), 0o755); err != nil {
		t.Fatalf("mkdir repo lib dir: %v", err)
	}

	binDir := filepath.Join(sharedRoot, "bin")
	toolDir := filepath.Join(sharedRoot, "libexec")
	libPath := filepath.Join(sharedRoot, "lib", "libfff_c.so")

	writeTestFile(t, filepath.Join(binDir, "swarmd"), "bin")
	writeTestFile(t, filepath.Join(toolDir, "rebuild"), "tool")
	writeTestFile(t, libPath, "fff")

	mount := CurrentRuntimeMount()
	if mount == nil {
		t.Fatal("CurrentRuntimeMount returned nil")
	}
	if got, want := mount.FFFLibPath, libPath; got != want {
		t.Fatalf("FFFLibPath = %q, want %q", got, want)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFetchProductionImageMetadataRequiresDigestSizeAndVersion(t *testing.T) {
	oldVersion := buildinfo.Version
	oldCommit := buildinfo.Commit
	oldClient := productionImageMetadataClient
	buildinfo.Version = "v1.2.3"
	buildinfo.Commit = "abcdef123456"
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Commit = oldCommit
		productionImageMetadataClient = oldClient
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/swarm-agent/swarm/releases/download/v1.2.3/container-image-info.txt" {
			t.Fatalf("metadata path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(strings.Join([]string{
			"image_ref=ghcr.io/swarm-agent/swarm:v1.2.3",
			"image_digest_ref=ghcr.io/swarm-agent/swarm@sha256:abc123",
			"version=v1.2.3",
			"commit=abcdef123456",
			"source_revision=abcdef123456",
			"image_size_bytes=123456789",
		}, "\n")))
	}))
	defer server.Close()

	productionImageMetadataClient = server.Client()
	oldTemplate := productionMetadataURLTmpl
	productionMetadataURLTmpl = server.URL + "/swarm-agent/swarm/releases/download/%s/container-image-info.txt"
	t.Cleanup(func() { productionMetadataURLTmpl = oldTemplate })

	metadata, err := FetchProductionImageMetadata(context.Background())
	if err != nil {
		t.Fatalf("FetchProductionImageMetadata() error = %v", err)
	}
	if metadata.ImageRef != "ghcr.io/swarm-agent/swarm:v1.2.3" {
		t.Fatalf("ImageRef = %q", metadata.ImageRef)
	}
	if metadata.ImageDigestRef != "ghcr.io/swarm-agent/swarm@sha256:abc123" {
		t.Fatalf("ImageDigestRef = %q", metadata.ImageDigestRef)
	}
	if metadata.ImageSizeBytes != 123456789 {
		t.Fatalf("ImageSizeBytes = %d", metadata.ImageSizeBytes)
	}
}

func TestFetchProductionImageMetadataRejectsInvalidSize(t *testing.T) {
	oldVersion := buildinfo.Version
	oldClient := productionImageMetadataClient
	buildinfo.Version = "v1.2.3"
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		productionImageMetadataClient = oldClient
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Join([]string{
			"image_ref=ghcr.io/swarm-agent/swarm:v1.2.3",
			"image_digest_ref=ghcr.io/swarm-agent/swarm@sha256:abc123",
			"version=v1.2.3",
			"image_size_bytes=-1",
		}, "\n")))
	}))
	defer server.Close()

	productionImageMetadataClient = server.Client()
	oldTemplate := productionMetadataURLTmpl
	productionMetadataURLTmpl = server.URL + "/%s/container-image-info.txt"
	t.Cleanup(func() { productionMetadataURLTmpl = oldTemplate })

	_, err := FetchProductionImageMetadata(context.Background())
	if err == nil || !strings.Contains(err.Error(), "image_size_bytes is invalid") {
		t.Fatalf("FetchProductionImageMetadata() error = %v, want invalid image_size_bytes", err)
	}
}

func TestAppendLocalContainerUserArgsAddsPodmanKeepIDAndRuntimeUserEnv(t *testing.T) {
	uid := hostRuntimeUID()
	gid := hostRuntimeGID()
	if uid <= 0 || gid <= 0 {
		t.Skip("test requires non-root host uid/gid")
	}
	got := appendLocalContainerUserArgs("podman", []string{
		"--add-host", "host.containers.internal:10.0.2.2",
		"--user", "65534:65534",
		"-e", "SWARM_RUNTIME_UID=65534",
		"--userns=auto",
	})
	want := []string{
		"--add-host", "host.containers.internal:10.0.2.2",
		"--userns=keep-id",
		"--user", "0:0",
		"-e", fmt.Sprintf("SWARM_RUNTIME_UID=%d", uid),
		"-e", fmt.Sprintf("SWARM_RUNTIME_GID=%d", gid),
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("appendLocalContainerUserArgs() = %#v, want %#v", got, want)
	}
}

func TestAppendLocalContainerUserArgsAddsDockerRuntimeUserEnvWithoutUserNS(t *testing.T) {
	uid := hostRuntimeUID()
	gid := hostRuntimeGID()
	if uid <= 0 || gid <= 0 {
		t.Skip("test requires non-root host uid/gid")
	}
	got := appendLocalContainerUserArgs("docker", []string{"--add-host", "host.docker.internal:host-gateway"})
	want := []string{
		"--add-host", "host.docker.internal:host-gateway",
		"--user", "0:0",
		"-e", fmt.Sprintf("SWARM_RUNTIME_UID=%d", uid),
		"-e", fmt.Sprintf("SWARM_RUNTIME_GID=%d", gid),
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("appendLocalContainerUserArgs() = %#v, want %#v", got, want)
	}
}
