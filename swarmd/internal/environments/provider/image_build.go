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
 OperationID string
 Connection *environments.Connection
 Definition environments.ImageBuildDefinition
 ProductRoot, RecipeRoot string
}

func (p *LocalDockerProvider) ConfigureBuildRoot(root string) { p.buildRoot = root }

func (p *LocalDockerProvider) buildDirectory(id string) (string, error) {
 if !strings.HasPrefix(id, "op_") || !opIDRegex.MatchString(id) || !filepath.IsAbs(p.buildRoot) { return "", errors.New("managed build root or operation identity unavailable") }
 return filepath.Join(p.buildRoot, id), nil
}

func (p *LocalDockerProvider) BuildImage(ctx context.Context, req ImageBuildRequest) (result *environments.ImageBuildResult, retErr error) {
 if p.Kind() != environments.ConnectionKindLocalPodman { return nil, errors.New("managed builds require local_podman") }
 ctx, cancel := context.WithCancel(ctx)
 state := &imageBuildRun{cancel:cancel, done:make(chan struct{})}
 if _, loaded := p.buildRuns.LoadOrStore(req.OperationID,state); loaded { cancel(); return nil, errors.New("build already started or cancelled") }
 defer func(){ cancel(); close(state.done) }()
 if err := p.validateLocalKind(req.Connection); err != nil { return nil, err }
 if _, err := p.Capabilities(ctx, req.Connection); err != nil { return nil, err }
 if err := req.Definition.Validate(); err != nil { return nil, err }
 root, err := p.buildDirectory(req.OperationID); if err != nil { return nil, err }
 if err := os.MkdirAll(p.buildRoot, 0700); err != nil { return nil, err }
 if err := os.Mkdir(root, 0700); err != nil { return nil, errors.New("build operation scratch already exists or cannot be created") }
 // Cleanup is operation-specific; no global image/container prune is ever used.
 defer func() {
  cleanupCtx, cancel := context.WithTimeout(context.Background(), CleanupTimeout); defer cancel()
  if err := p.cleanupBuildFiles(cleanupCtx, req.OperationID); err != nil { retErr = fmt.Errorf("%w: %v", ErrOperationCleanupFailed, err); result = nil }
 }()
 for _, dir := range []string{"context", "home", "run", "storage", "hooks", "tmp"} { if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil { return nil, err } }
 for name, data := range map[string]string{"auth.json":"{\"auths\":{}}", "mounts.conf":"", "containers.conf":"[containers]\nhttp_proxy=false\n[engine]\n", "registries.conf":"unqualified-search-registries=[]\n"} {
  if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil { return nil, err }
 }
 digest, err := ExportCommittedBuild(ctx, p.runner, req.ProductRoot, req.RecipeRoot, filepath.Join(root,"context"), req.Definition)
 if err != nil { return nil, err }
 // Isolated image storage gives cleanup exact ownership, including intermediate
 // Buildah containers after crashes. No host auth, hooks, mounts or proxy values.
 isolated := []string{"--remote=false", "--root", filepath.Join(root,"storage"), "--runroot", filepath.Join(root,"run"), "--storage-driver=vfs", "--cgroup-manager=systemd", "--runtime=crun"}
 cleanEnv := []string{"env", "-i", "PATH="+os.Getenv("PATH"), "HOME="+filepath.Join(root,"home"), "XDG_RUNTIME_DIR="+os.Getenv("XDG_RUNTIME_DIR"), "DBUS_SESSION_BUS_ADDRESS="+os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "TMPDIR="+filepath.Join(root,"tmp"), "CONTAINERS_CONF="+filepath.Join(root,"containers.conf"), "CONTAINERS_REGISTRIES_CONF="+filepath.Join(root,"registries.conf"), "CONTAINERS_MOUNTS_CONF="+filepath.Join(root,"mounts.conf"), "podman"}
 build := append(append([]string{}, isolated...), "build", "--jobs=1", "--layers=false", "--force-rm=true", "--http-proxy=false", "--isolation=oci", "--network=slirp4netns:allow_host_loopback=false", "--cgroupns=private", "--memory=8g", "--cpu-period=100000", "--cpu-quota=200000", "--ulimit=nofile=4096:4096", "--no-hosts", "--hooks-dir", filepath.Join(root,"hooks"), "--authfile", filepath.Join(root,"auth.json"), "--tls-verify=true", "--retry=0", "--iidfile", filepath.Join(root,"image-id"), "--build-arg", "SWARM_BUILD_SHA="+req.Definition.Product.Commit, "--label", "io.swarm.build.operation="+req.OperationID, "--label", "org.opencontainers.image.revision="+req.Definition.Product.Commit, "--label", "io.swarm.build.inputs="+req.Definition.Digest(), "--file", filepath.Join(root,"context", ".swarm-recipe", filepath.FromSlash(req.Definition.RecipeFile)), filepath.Join(root,"context"))
 args := []string{"--user", "--wait", "--pipe", "--collect", "--unit=swarm-build-"+req.OperationID, "--property=RuntimeMaxSec=600", "--property=TimeoutStopSec=5", "--property=KillMode=control-group", "--property=MemoryMax=10G", "--property=TasksMax=1024", "--property=CPUQuota=200%", "--"}
 args = append(args, cleanEnv...); args = append(args, build...)
 // Discard recipe output: errors may contain source secrets even after generic redaction.
 if err := p.runner.RunWithIO(ctx, nil, io.Discard, io.Discard, "systemd-run", args...); err != nil { return nil, errors.New("isolated image build failed or was cancelled; no image accepted") }
 if err := ctx.Err(); err != nil { return nil, err }
 raw, err := os.ReadFile(filepath.Join(root,"image-id")); if err != nil || len(raw)>128 { return nil, errors.New("build did not produce bounded image identity") }
 id := strings.TrimSpace(string(raw)); if !environments.ValidBuildImageID(id) { return nil, errors.New("build returned invalid image identity") }
 archive := filepath.Join(root,"image.tar")
 save := append(append([]string{}, cleanEnv[1:]...), isolated...)
 save = append(save, "save", "--format=oci-archive", "--output", archive, id)
 if err := p.runner.RunWithIO(ctx, nil, io.Discard, io.Discard, "env", save...); err != nil { return nil, errors.New("isolated image export failed") }
 if err := ctx.Err(); err != nil { return nil, err }
 if err := p.runner.RunWithIO(ctx, nil, io.Discard, io.Discard, "podman", append(dockerHostArgs(req.Connection), "load", "--input", archive)...); err != nil { return nil, errors.New("managed image import failed") }
 out, err := p.runner.Run(ctx,"podman", append(dockerHostArgs(req.Connection), "image", "inspect", "--format=json", id)...)
 var images []struct{ ID string `json:"Id"`; Labels map[string]string `json:"Labels"` }
 if err != nil || json.Unmarshal(out,&images)!=nil || len(images)!=1 || images[0].ID!=id || images[0].Labels["io.swarm.build.operation"]!=req.OperationID || images[0].Labels["io.swarm.build.inputs"]!=req.Definition.Digest() { return nil, errors.New("imported image provenance mismatch") }
 return &environments.ImageBuildResult{OperationID:req.OperationID, ConnectionID:req.Connection.ID, ImageID:id, ContextDigest:digest, DefinitionDigest:req.Definition.Digest(), Product:req.Definition.Product, Recipe:req.Definition.Recipe, RecipeFile:req.Definition.RecipeFile}, nil
}

func (p *LocalDockerProvider) cleanupBuildFiles(ctx context.Context, id string) error {
 root, err := p.buildDirectory(id); if err != nil { return err }
 unit := "swarm-build-"+id+".service"
 // Stop only the server-generated operation unit, then prove it no longer runs.
 _, stopErr := p.runner.Run(ctx,"systemctl","--user","stop",unit)
 out, err := p.runner.Run(ctx,"systemctl","--user","show",unit,"--property=ActiveState","--value")
 state := strings.TrimSpace(string(out))
 if err != nil || (state!="inactive" && state!="failed") { return fmt.Errorf("build unit termination unconfirmed (stop failed: %t)",stopErr!=nil) }
 // The storage is private to this operation. Podman unmounts only its own
 // external build containers before recursive removal; no shared store touched.
 if _, err := os.Stat(filepath.Join(root,"storage")); err==nil {
  _, err = p.runner.Run(ctx,"podman","--remote=false","--root",filepath.Join(root,"storage"),"--runroot",filepath.Join(root,"run"),"--storage-driver=vfs","unmount","--all","--force")
  if err!=nil { return errors.New("owned build storage unmount failed") }
 }
 return os.RemoveAll(root)
}

type imageBuildRun struct { cancel context.CancelFunc; done chan struct{} }

func (p *LocalDockerProvider) CleanupBuild(ctx context.Context, id string) error {
 if p.Kind()!=environments.ConnectionKindLocalPodman { return errors.New("unsupported build cleanup provider") }
 fence := &imageBuildRun{cancel:func(){},done:make(chan struct{})}; close(fence.done)
 existing, loaded := p.buildRuns.LoadOrStore(id,fence)
 if loaded {
  state := existing.(*imageBuildRun); state.cancel()
  select { case <-state.done: case <-ctx.Done(): return errors.New("build action has not confirmed termination") }
 }
 return p.cleanupBuildFiles(ctx,id)
}

