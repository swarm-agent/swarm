package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"swarm-refactor/swarmtui/pkg/environments"
)

// BuildCleanupError retains the primary failure and cleanup classification while
// bounding and redacting the secondary diagnostic before it reaches durable state.
func BuildCleanupError(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("%w: managed build cleanup unconfirmed: %s", ErrOperationCleanupFailed, commandDiagnostic(cleanup.Error())))
}

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
func (p *LocalDockerProvider) ConfigureRuntimeDir(dir string) { p.runtimeDir = dir }

const buildRunrootPrefix = "sbr-"

func (p *LocalDockerProvider) buildDirectory(id string) (string, error) {
	if !strings.HasPrefix(id, "op_") || ValidateOperationID(id) != nil || !filepath.IsAbs(p.buildRoot) || filepath.Clean(p.buildRoot) != p.buildRoot {
		return "", errors.New("managed build root or operation identity unavailable")
	}
	return filepath.Join(p.buildRoot, id), nil
}

// buildRunrootPath derives a deterministic, bounded per-operation runroot directly
// under the verified runtime directory. Length is guaranteed to be <= 50 characters.
func buildRunrootPath(runtimeDir, buildRoot, id string) (string, error) {
	if !strings.HasPrefix(id, "op_") || ValidateOperationID(id) != nil {
		return "", errors.New("managed build root or operation identity unavailable")
	}
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
		return "", errors.New("managed build runtime directory must be an absolute clean path")
	}
	if !filepath.IsAbs(buildRoot) || filepath.Clean(buildRoot) != buildRoot {
		return "", errors.New("managed build root must be an absolute clean path")
	}
	// 144 bits distinguish operations and account-scoped build roots without
	// exceeding Podman's limit, even under /run/user/4294967295 (49 bytes).
	hash := sha256.Sum256([]byte(buildRoot + "\x00" + id))
	token := base64.RawURLEncoding.EncodeToString(hash[:18])
	runroot := filepath.Join(runtimeDir, buildRunrootPrefix+token)
	if len(runroot) > 50 {
		return "", fmt.Errorf("managed build runroot %q exceeds Podman limit of 50 characters (length %d)", runroot, len(runroot))
	}
	return runroot, nil
}

func (p *LocalDockerProvider) resolveRuntimeDir() (string, error) {
	if p.runtimeDir != "" {
		if !filepath.IsAbs(p.runtimeDir) || filepath.Clean(p.runtimeDir) != p.runtimeDir {
			return "", errors.New("local Podman user runtime directory must be an absolute clean path")
		}
		info, err := os.Lstat(p.runtimeDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return "", errors.New("local Podman user runtime directory unavailable or unsafe; host configuration unchanged")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if int(stat.Uid) != os.Geteuid() {
				return "", errors.New("local Podman user runtime directory not owned by effective user")
			}
		}
		return p.runtimeDir, nil
	}

	sessionEnv := os.Environ()
	if runner, ok := p.runner.(*OSCommandRunner); ok && runner.commandEnv != nil {
		var err error
		sessionEnv, err = runner.commandEnv()
		if err != nil {
			return "", err
		}
	}
	var runtimeDir string
	for _, entry := range sessionEnv {
		if strings.HasPrefix(entry, "XDG_RUNTIME_DIR=") {
			runtimeDir = strings.TrimPrefix(entry, "XDG_RUNTIME_DIR=")
			break
		}
	}
	if runtimeDir == "" {
		runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
	}
	if runtimeDir == "" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Geteuid()))
	}
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
		return "", errors.New("local Podman user runtime directory must be an absolute clean path")
	}
	info, err := os.Lstat(runtimeDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", errors.New("local Podman user runtime directory unavailable or unsafe; host configuration unchanged")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(stat.Uid) != os.Geteuid() {
			return "", errors.New("local Podman user runtime directory not owned by effective user")
		}
	}
	return runtimeDir, nil
}

func (p *LocalDockerProvider) resolveSessionEnvironment() (string, string, error) {
	runtimeDir, err := p.resolveRuntimeDir()
	if err != nil {
		return "", "", err
	}
	sessionEnv := os.Environ()
	if runner, ok := p.runner.(*OSCommandRunner); ok && runner.commandEnv != nil {
		if env, envErr := runner.commandEnv(); envErr == nil {
			sessionEnv = env
		}
	}
	var busAddress string
	for _, entry := range sessionEnv {
		if strings.HasPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=") {
			busAddress = strings.TrimPrefix(entry, "DBUS_SESSION_BUS_ADDRESS=")
			break
		}
	}
	if busAddress == "" {
		busAddress = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	}
	return runtimeDir, busAddress, nil
}

func (p *LocalDockerProvider) buildRunroot(id string) (string, error) {
	runtimeDir, err := p.resolveRuntimeDir()
	if err != nil {
		return "", err
	}
	return buildRunrootPath(runtimeDir, p.buildRoot, id)
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
	runroot, err := p.buildRunroot(req.OperationID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.buildRoot, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, errors.New("build operation scratch already exists or cannot be created")
	}
	// Publish an allocation intent before touching runtime resources. A crash
	// before ownership publication leaves private evidence, never deletion
	// authority inferred from the short pathname.
	if err := writeBuildAllocationIntent(root, req.OperationID+"\n"+p.buildRoot+"\n"+runroot); err != nil {
		return nil, BuildCleanupError(err, errors.New("build allocation evidence unavailable; scratch retained"))
	}
	if err := os.Mkdir(runroot, 0700); err != nil {
		// Only root was allocated by this invocation. In particular, never
		// install runroot cleanup after a rejected exclusive allocation.
		return nil, BuildCleanupError(errors.New("build operation runroot already exists or cannot be created"), os.RemoveAll(root))
	}
	if err := p.publishBuildOwnership(req.OperationID, runroot); err != nil {
		return nil, BuildCleanupError(err, errors.New("build ownership publication incomplete; allocation evidence retained"))
	}
	// Cleanup is operation-specific; no global image/container prune is ever used.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), CleanupTimeout)
		defer cancel()
		if err := p.cleanupBuildFiles(cleanupCtx, req.OperationID); err != nil {
			retErr = BuildCleanupError(retErr, err)
			result = nil
		}
		if retErr != nil {
			if err := p.cleanupBuildImages(cleanupCtx, req.OperationID); err != nil {
				retErr = BuildCleanupError(retErr, err)
				result = nil
			}
		}
	}()
	for _, dir := range []string{"context", "home", "storage", "hooks", "tmp"} {
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
	if _, err := p.verifyActiveBuildOwnership(req.OperationID); err != nil {
		return nil, err
	}
	cleanEnv, isolated, err := p.privateBuildEngine(root, runroot)
	if err != nil {
		return nil, err
	}
	build := append(append([]string{}, isolated...), "build", "--jobs=1", "--ignorefile", filepath.Join(root, "ignore"), "--layers=false", "--force-rm=true", "--http-proxy=false", "--isolation=oci", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--memory=8g", "--cpu-period=100000", "--cpu-quota=200000", "--ulimit=nofile=4096:4096", "--no-hosts", "--hooks-dir", filepath.Join(root, "hooks"), "--authfile", filepath.Join(root, "auth.json"), "--tls-verify=true", "--retry=0", "--iidfile", filepath.Join(root, "image-id"), "--build-arg", "SWARM_BUILD_SHA="+req.Definition.Product.Commit, "--label", "io.swarm.build.operation="+req.OperationID, "--label", "org.opencontainers.image.revision="+req.Definition.Product.Commit, "--label", "io.swarm.build.inputs="+req.Definition.Digest(), "--file", filepath.Join(root, "context", ".swarm-recipe", filepath.FromSlash(req.Definition.RecipeFile)), filepath.Join(root, "context"))
	args := []string{"--user", "--wait", "--pipe", "--collect", "--unit=swarm-build-" + req.OperationID, "--property=Delegate=yes", "--property=RuntimeMaxSec=600", "--property=TimeoutStopSec=5", "--property=KillMode=control-group", "--property=MemoryMax=10G", "--property=TasksMax=1024", "--property=CPUQuota=200%", "--property=LimitFSIZE=8G", "--"}
	args = append(args, cleanEnv...)
	args = append(args, build...)
	// Never publish recipe output, even after generic credential redaction.
	if err := p.runBuildCommand(ctx, "build", io.Discard, "systemd-run", args...); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := readBuildImageID(filepath.Join(root, "image-id"))
	if err != nil {
		return nil, err
	}
	id := normalizedBuildImageID(strings.TrimSpace(string(raw)))
	if id == "" {
		return nil, errors.New("build returned invalid image identity")
	}
	if _, err := p.verifyActiveBuildOwnership(req.OperationID); err != nil {
		return nil, err
	}
	archive := filepath.Join(root, "image.tar")
	save := append(append([]string{}, cleanEnv[1:]...), isolated...)
	save = append(save, "save", "--format=oci-archive", id)
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("image export scratch unavailable")
	}
	export := &buildArchiveWriter{writer: file, remaining: 8 << 30, cancel: cancel}
	exportErr := p.runBuildCommand(ctx, "export", export, "env", save...)
	closeErr := file.Close()
	if export.exceeded {
		return nil, errors.New("isolated image export exceeded 8 GiB limit")
	}
	if exportErr != nil {
		return nil, exportErr
	}
	if closeErr != nil {
		return nil, errors.New("isolated image export scratch close failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.runBuildCommand(ctx, "import", io.Discard, "podman", append(dockerHostArgs(req.Connection), "load", "--input", archive)...); err != nil {
		return nil, err
	}
	out, err := p.runner.Run(ctx, "podman", append(dockerHostArgs(req.Connection), "image", "inspect", "--format=json", id)...)
	var images []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if err != nil || json.Unmarshal(out, &images) != nil || len(images) != 1 || normalizedBuildImageID(images[0].ID) != id || images[0].Labels["io.swarm.build.operation"] != req.OperationID || images[0].Labels["io.swarm.build.inputs"] != req.Definition.Digest() || images[0].Labels["org.opencontainers.image.revision"] != req.Definition.Product.Commit {
		return nil, errors.New("imported image provenance mismatch")
	}
	return &environments.ImageBuildResult{OperationID: req.OperationID, ConnectionID: req.Connection.ID, ImageID: id, ContextDigest: digest, DefinitionDigest: req.Definition.Digest(), Product: req.Definition.Product, Recipe: req.Definition.Recipe, RecipeFile: req.Definition.RecipeFile}, nil
}

// normalizedBuildImageID accepts only full, lowercase SHA-256 identities. Engine
// inspect may omit the algorithm prefix; durable receipts never do. Short IDs,
// other algorithms, whitespace and malformed digest bytes are not equivalent.
func normalizedBuildImageID(id string) string {
	if len(id) == 64 {
		id = "sha256:" + id
	}
	if !environments.ValidBuildImageID(id) {
		return ""
	}
	return id
}

// All commands that touch the private engine store share these exact overrides.
// In particular, cleanup must not inherit the daemon's HOME/auth/proxy/config or
// initialize a shared store merely to obtain a rootless user namespace.
func (p *LocalDockerProvider) privateBuildEngine(root, runroot string) ([]string, []string, error) {
	runtimeDir, busAddress, err := p.resolveSessionEnvironment()
	if err != nil {
		return nil, nil, err
	}
	isolated := []string{"--remote=false", "--root", filepath.Join(root, "storage"), "--runroot", runroot, "--storage-driver=vfs", "--cgroup-manager=cgroupfs", "--runtime=crun"}
	cleanEnv := []string{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "XDG_RUNTIME_DIR=" + runtimeDir, "DBUS_SESSION_BUS_ADDRESS=" + busAddress, "TMPDIR=" + filepath.Join(root, "tmp"), "CONTAINERS_CONF=" + filepath.Join(root, "containers.conf"), "CONTAINERS_REGISTRIES_CONF=" + filepath.Join(root, "registries.conf"), "CONTAINERS_MOUNTS_CONF=" + filepath.Join(root, "mounts.conf"), "podman"}
	return cleanEnv, isolated, nil
}

// Fixed script, never recipe/input shell text. Pre-order directory chmod handles
// VFS 0555 roots even where namespace capabilities cannot bypass filesystem DAC.
// One synchronous chmod child at a time; the operation deadline bounds traversal.
// Final rm and the caller's absence check establish removal, not chmod success.
const buildStorageRemovalScript = `find "$1" -xdev -type d -exec chmod u+rwx -- {} \; && exec rm --recursive --force --one-file-system -- "$1"`

func (p *LocalDockerProvider) cleanupBuildFiles(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, CleanupTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := p.buildDirectory(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		runroot, rErr := p.buildRunroot(id)
		if rErr != nil {
			return rErr
		}
		if _, rErr = os.Lstat(runroot); errors.Is(rErr, os.ErrNotExist) {
			return nil
		}
		return errors.New("build scratch ownership unavailable; runtime resource retained")
	}
	if err != nil || !privateBuildDirectory(info) {
		return errors.New("owned build scratch is unavailable or unsafe")
	}
	owner, err := p.verifyBuildOwnership(id)
	if err != nil {
		return err
	}
	// A previous cleanup may have been interrupted by daemon/process death.
	// Prove both operation-specific units are stopped before touching storage.
	for _, unit := range []string{"swarm-build-" + id + ".service", "swarm-build-cleanup-" + id + ".service"} {
		if err := p.stopBuildUnit(ctx, unit); err != nil {
			return err
		}
	}
	return p.removeOwnedBuildFiles(ctx, id, root, owner)
}

func (p *LocalDockerProvider) stopBuildUnit(ctx context.Context, unit string) error {
	_, stopErr := p.runner.Run(ctx, "systemctl", "--user", "stop", unit)
	out, err := p.runner.Run(ctx, "systemctl", "--user", "show", unit, "--property=LoadState", "--property=ActiveState")
	// --collect unloads completed units; show may exit nonzero for not-found.
	// Require explicit structured inactive evidence, never just a failed stop.
	properties := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = value
		}
	}
	absent := properties["LoadState"] == "not-found" && properties["ActiveState"] == "inactive"
	stopped := err == nil && properties["LoadState"] == "loaded" && (properties["ActiveState"] == "inactive" || properties["ActiveState"] == "failed")
	if !absent && !stopped {
		return fmt.Errorf("build unit termination unconfirmed (stop failed: %t)", stopErr != nil)
	}
	return nil
}

func (p *LocalDockerProvider) removeOwnedBuildFiles(ctx context.Context, id, root string, owner buildOwnership) error {
	runroot := owner.Runroot
	// The storage is private to this operation. Podman unmounts only its own
	// external build containers before recursive removal; no shared store touched.
	storage := filepath.Join(root, "storage")
	storageInfo, statErr := os.Lstat(storage)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("owned build storage inspection failed")
	}
	if statErr == nil && !privateBuildDirectory(storageInfo) {
		return errors.New("owned build storage is not a private directory")
	}
	populated := false
	if statErr == nil {
		dir, err := os.Open(storage)
		if err != nil {
			return errors.New("owned build storage inventory unavailable")
		}
		entries, readErr := dir.ReadDir(1)
		_ = dir.Close()
		if readErr != nil && readErr != io.EOF {
			return errors.New("owned build storage inventory unavailable")
		}
		populated = len(entries) > 0
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if populated {
		if _, err := os.Lstat(runroot); err != nil {
			return errors.New("populated build storage requires owned runtime resource; scratch retained")
		}
		activeOwner, err := p.verifyActiveBuildOwnership(id)
		if err != nil || activeOwner != owner {
			return errors.New("build ownership changed before cleanup; resources retained")
		}
		cleanEnv, isolated, err := p.privateBuildEngine(root, runroot)
		if err != nil {
			return err
		}
		engine := append(append([]string{}, cleanEnv[1:]...), isolated...)
		if err := p.runBuildCommand(ctx, "cleanup-unmount", io.Discard, "env", append(engine, "unmount", "--all", "--force")...); err != nil {
			return err
		}
		// Recheck both receipts/inodes after the engine boundary. A partial
		// deletion retains these receipts and the runroot for restart recovery.
		current, err := p.verifyActiveBuildOwnership(id)
		if err != nil || current != owner {
			return errors.New("build ownership changed during unmount; resources retained")
		}
		currentStorage, err := os.Lstat(storage)
		if err != nil || !privateBuildDirectory(currentStorage) || !os.SameFile(storageInfo, currentStorage) {
			return errors.New("build storage changed during unmount; resources retained")
		}
		// The engine namespace maps layer owners; only directories inside
		// this verified store gain owner traversal/write permission. Neither
		// find nor rm follows symlinks; traversal stays on this filesystem.
		// No global reset/prune, host-wide chmod/chown or privileged fallback.
		unit := "swarm-build-cleanup-" + id + ".service"
		args := []string{"--user", "--wait", "--pipe", "--collect", "--unit=" + unit, "--property=KillMode=control-group", "--property=Delegate=yes", "--property=RuntimeMaxSec=" + strconv.Itoa(int(CleanupTimeout.Seconds())), "--property=TimeoutStopSec=5", "--property=TasksMax=64", "--"}
		args = append(args, "env")
		args = append(args, engine...)
		args = append(args, "unshare", "sh", "-c", buildStorageRemovalScript, "swarm-build-cleanup", storage)
		removalErr := p.runBuildCommand(ctx, "cleanup-storage", io.Discard, "systemd-run", args...)
		// exec.CommandContext can kill the launcher, not namespace descendants.
		// Independently stop/prove the unit even on timeout or cancellation.
		stopCtx, cancel := context.WithTimeout(context.Background(), CleanupTimeout)
		stopErr := p.stopBuildUnit(stopCtx, unit)
		cancel()
		if err := errors.Join(removalErr, stopErr); err != nil {
			return err
		}
		if _, err := os.Lstat(storage); !errors.Is(err, os.ErrNotExist) {
			return errors.New("owned build storage removal unconfirmed; ownership retained")
		}
	} else if err := os.Remove(storage); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Empty/pre-launch stores need no engine initialization or namespace.
		return errors.New("empty build storage removal unconfirmed; ownership retained")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.cleanupRunroot(id); err != nil {
		// Keep the scratch receipt for recovery; never erase it after an
		// unconfirmed runtime cleanup.
		return err
	}
	return removeBuildScratch(root)
}

func (p *LocalDockerProvider) cleanupRunroot(id string) error {
	owner, err := p.verifyBuildOwnership(id)
	if err != nil {
		return err
	}
	runroot := owner.Runroot
	if runroot == "" {
		return nil
	}
	if !filepath.IsAbs(runroot) || filepath.Clean(runroot) != runroot {
		return errors.New("owned build runroot is not an absolute clean path")
	}
	info, err := os.Lstat(runroot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("owned build runroot inspection failed")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("owned build runroot is an unsafe symlink")
	}
	if !info.IsDir() {
		return errors.New("owned build runroot is not a directory")
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("owned build runroot has unsafe permissions")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(stat.Uid) != os.Geteuid() {
			return errors.New("owned build runroot is not owned by the current user")
		}
	}
	return removeBuildScratch(runroot)
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
	// Scratch and imported images are independent owned resources. A failed
	// unmount must not prevent attempting removal of an already imported image.
	filesErr := p.cleanupBuildFiles(ctx, id)
	imagesErr := p.cleanupBuildImages(ctx, id)
	return errors.Join(filesErr, imagesErr)
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
		image = normalizedBuildImageID(image)
		if image == "" {
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
