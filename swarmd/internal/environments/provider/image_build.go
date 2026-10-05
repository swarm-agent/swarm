package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"swarm-refactor/swarmtui/pkg/environments"
)

// ImageBuilder is optional: Docker/SSH never silently substitute for this local contract.
type ImageBuilder interface {
	BuildImage(context.Context, ImageBuildRequest) (*environments.ImageBuildResult, error)
	CleanupBuild(context.Context, string) error
}
type ImageBuildRequest struct {
	OperationID             string
	Connection              *environments.Connection
	Definition              environments.ImageBuildDefinition
	ProductRoot, RecipeRoot string
}

func (p *LocalDockerProvider) ConfigureBuildRoot(root string) { p.buildRoot = root }

func (p *LocalDockerProvider) buildDirectory(id string) (string, error) {
	if !strings.HasPrefix(id, "op_") || ValidateOperationID(id) != nil || !filepath.IsAbs(p.buildRoot) {
		return "", errors.New("managed build root or operation identity unavailable")
	}
	return filepath.Join(p.buildRoot, id), nil
}

func (p *LocalDockerProvider) BuildImage(ctx context.Context, req ImageBuildRequest) (result *environments.ImageBuildResult, retErr error) {
	if p.Kind() != environments.ConnectionKindLocalPodman {
		return nil, errors.New("managed builds require local_podman")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	state := &imageBuildRun{cancel: cancel, done: make(chan struct{})}
	if _, loaded := p.buildRuns.LoadOrStore(req.OperationID, state); loaded {
		cancel()
		return nil, errors.New("build already started or cancelled")
	}
	defer func() { cancel(); close(state.done); p.buildRuns.Delete(req.OperationID) }()
	if err := p.validateLocalKind(req.Connection); err != nil {
		return nil, err
	}
	if _, err := p.Capabilities(ctx, req.Connection); err != nil {
		return nil, err
	}
	if err := req.Definition.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := p.buildDirectory(req.OperationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.buildRoot, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, errors.New("build operation scratch already exists or cannot be created")
	}
	// Cleanup is operation-specific; no global image/container prune is ever used.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), CleanupTimeout)
		defer cancel()
		if err := p.cleanupBuildFiles(cleanupCtx, req.OperationID); err != nil {
			retErr = fmt.Errorf("%w: %v", ErrOperationCleanupFailed, err)
			result = nil
		}
		if retErr != nil {
			if err := p.cleanupBuildImages(cleanupCtx, req.OperationID); err != nil {
				retErr = fmt.Errorf("%w: %v", ErrOperationCleanupFailed, err)
				result = nil
			}
		}
	}()
	for _, dir := range []string{"context", "home", "run", "storage", "hooks", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	for name, data := range map[string]string{"auth.json": "{\"auths\":{}}", "ignore": "", "mounts.conf": "", "containers.conf": "[containers]\nhttp_proxy=false\n[engine]\n", "registries.conf": "unqualified-search-registries=[]\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			return nil, err
		}
	}
	digest, err := ExportCommittedBuild(ctx, p.runner, req.ProductRoot, req.RecipeRoot, filepath.Join(root, "context"), req.Definition)
	if err != nil {
		return nil, err
	}
	// Isolated image storage gives cleanup exact ownership, including intermediate
	// Buildah containers after crashes. No host auth, hooks, mounts or proxy values.
	// Build containers stay below the delegated transient unit: using the
	// systemd manager here could move descendants into sibling user scopes.
	// Runtime containers still use the separately admitted systemd manager.
	isolated := []string{"--remote=false", "--root", filepath.Join(root, "storage"), "--runroot", filepath.Join(root, "run"), "--storage-driver=vfs", "--cgroup-manager=cgroupfs", "--runtime=crun"}
	cleanEnv := []string{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR"), "DBUS_SESSION_BUS_ADDRESS=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "TMPDIR=" + filepath.Join(root, "tmp"), "CONTAINERS_CONF=" + filepath.Join(root, "containers.conf"), "CONTAINERS_REGISTRIES_CONF=" + filepath.Join(root, "registries.conf"), "CONTAINERS_MOUNTS_CONF=" + filepath.Join(root, "mounts.conf"), "podman"}
	build := append(append([]string{}, isolated...), "build", "--jobs=1", "--ignorefile", filepath.Join(root, "ignore"), "--layers=false", "--force-rm=true", "--http-proxy=false", "--isolation=oci", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--memory=8g", "--cpu-period=100000", "--cpu-quota=200000", "--ulimit=nofile=4096:4096", "--no-hosts", "--hooks-dir", filepath.Join(root, "hooks"), "--authfile", filepath.Join(root, "auth.json"), "--tls-verify=true", "--retry=0", "--iidfile", filepath.Join(root, "image-id"), "--build-arg", "SWARM_BUILD_SHA="+req.Definition.Product.Commit, "--label", "io.swarm.build.operation="+req.OperationID, "--label", "org.opencontainers.image.revision="+req.Definition.Product.Commit, "--label", "io.swarm.build.inputs="+req.Definition.Digest(), "--file", filepath.Join(root, "context", ".swarm-recipe", filepath.FromSlash(req.Definition.RecipeFile)), filepath.Join(root, "context"))
	args := []string{"--user", "--wait", "--pipe", "--collect", "--unit=swarm-build-" + req.OperationID, "--property=Delegate=yes", "--property=RuntimeMaxSec=600", "--property=TimeoutStopSec=5", "--property=KillMode=control-group", "--property=MemoryMax=10G", "--property=TasksMax=1024", "--property=CPUQuota=200%", "--property=LimitFSIZE=8G", "--"}
	args = append(args, cleanEnv...)
	args = append(args, build...)
	// Discard recipe output: errors may contain source secrets even after generic redaction.
	if err := p.runner.RunWithIO(ctx, nil, io.Discard, io.Discard, "systemd-run", args...); err != nil {
		return nil, errors.New("isolated image build failed or was cancelled; no image accepted")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := readBuildImageID(filepath.Join(root, "image-id"))
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(string(raw))
	if !environments.ValidBuildImageID(id) {
		return nil, errors.New("build returned invalid image identity")
	}
	archive := filepath.Join(root, "image.tar")
	save := append(append([]string{}, cleanEnv[1:]...), isolated...)
	save = append(save, "save", "--format=oci-archive", id)
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("image export scratch unavailable")
	}
	export := &buildArchiveWriter{writer: file, remaining: 8 << 30, cancel: cancel}
	exportErr := p.runner.RunWithIO(ctx, nil, export, io.Discard, "env", save...)
	closeErr := file.Close()
	if exportErr != nil || closeErr != nil || export.exceeded {
		return nil, errors.New("isolated image export failed or exceeded 8 GiB limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.runner.RunWithIO(ctx, nil, io.Discard, io.Discard, "podman", append(dockerHostArgs(req.Connection), "load", "--input", archive)...); err != nil {
		return nil, errors.New("managed image import failed")
	}
	out, err := p.runner.Run(ctx, "podman", append(dockerHostArgs(req.Connection), "image", "inspect", "--format=json", id)...)
	var images []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err != nil || json.Unmarshal(out, &images) != nil || len(images) != 1 || images[0].ID != id || images[0].Labels["io.swarm.build.operation"] != req.OperationID || images[0].Labels["io.swarm.build.inputs"] != req.Definition.Digest() || images[0].Labels["org.opencontainers.image.revision"] != req.Definition.Product.Commit {
		return nil, errors.New("imported image provenance mismatch")
	}
	return &environments.ImageBuildResult{OperationID: req.OperationID, ConnectionID: req.Connection.ID, ImageID: id, ContextDigest: digest, DefinitionDigest: req.Definition.Digest(), Product: req.Definition.Product, Recipe: req.Definition.Recipe, RecipeFile: req.Definition.RecipeFile}, nil
}

func (p *LocalDockerProvider) cleanupBuildFiles(ctx context.Context, id string) error {
	root, err := p.buildDirectory(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unit := "swarm-build-" + id + ".service"
	// Stop only the server-generated operation unit, then prove it no longer runs.
	_, stopErr := p.runner.Run(ctx, "systemctl", "--user", "stop", unit)
	out, err := p.runner.Run(ctx, "systemctl", "--user", "show", unit, "--property=ActiveState", "--value")
	state := strings.TrimSpace(string(out))
	if err != nil || (state != "inactive" && state != "failed") {
		return fmt.Errorf("build unit termination unconfirmed (stop failed: %t)", stopErr != nil)
	}
	// The storage is private to this operation. Podman unmounts only its own
	// external build containers before recursive removal; no shared store touched.
	if _, err := os.Stat(filepath.Join(root, "storage")); err == nil {
		_, err = p.runner.Run(ctx, "podman", "--remote=false", "--root", filepath.Join(root, "storage"), "--runroot", filepath.Join(root, "run"), "--storage-driver=vfs", "unmount", "--all", "--force")
		if err != nil {
			return errors.New("owned build storage unmount failed")
		}
	}
	return os.RemoveAll(root)
}

type imageBuildRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *LocalDockerProvider) CleanupBuild(ctx context.Context, id string) error {
	if p.Kind() != environments.ConnectionKindLocalPodman {
		return errors.New("unsupported build cleanup provider")
	}
	// The supervisor cancels the operation context before cleanup, so a queued
	// invocation fails ctx.Err rather than allocating scratch after this returns.
	existing, loaded := p.buildRuns.Load(id)
	if loaded {
		state := existing.(*imageBuildRun)
		state.cancel()
		select {
		case <-state.done:
		case <-ctx.Done():
			return errors.New("build action has not confirmed termination")
		}
	}
	if err := p.cleanupBuildFiles(ctx, id); err != nil {
		return err
	}
	return p.cleanupBuildImages(ctx, id)
}

// Only bounded, regular engine receipts can become durable provenance.
func readBuildImageID(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128 {
		return nil, errors.New("build did not produce bounded image identity")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("build image identity unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil || len(raw) > 128 {
		return nil, errors.New("build image identity exceeds limit")
	}
	return raw, nil
}

// No global prune and no force removal of an image that might have consumers.
// Labels select only this server-generated operation, including a cancelled load.
func (p *LocalDockerProvider) cleanupBuildImages(ctx context.Context, id string) error {
	if _, err := p.buildDirectory(id); err != nil {
		return err
	}
	out, err := p.runner.Run(ctx, "podman", "--remote=false", "--cgroup-manager=systemd", "images", "--filter", "label=io.swarm.build.operation="+id, "--format={{.ID}}", "--no-trunc")
	if err != nil {
		return errors.New("owned build image cleanup inventory unavailable")
	}
	ids := strings.Fields(string(out))
	if len(ids) > 8 {
		return errors.New("owned build image cleanup inventory exceeds bound")
	}
	for _, image := range ids {
		if !environments.ValidBuildImageID(image) {
			return errors.New("owned build image cleanup returned invalid identity")
		}
		if _, err := p.runner.Run(ctx, "podman", "--remote=false", "--cgroup-manager=systemd", "image", "rm", image); err != nil {
			return errors.New("owned build image removal unconfirmed")
		}
	}
	return nil
}

type buildArchiveWriter struct {
	writer    io.Writer
	remaining int64
	cancel    context.CancelFunc
	exceeded  bool
}

func (w *buildArchiveWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		w.cancel()
		return 0, errors.New("image archive exceeds bound")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}
