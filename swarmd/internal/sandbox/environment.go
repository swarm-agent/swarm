package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Environments: a folder may define what its sandbox starts from (a base
// image plus setup commands and environment variables). Swarm builds that
// into a local image once, under gVisor, and the folder's sandbox then runs
// on it with exactly the same hardening as the default image: the image is
// the only thing that changes.

// EnvironmentImage is the image one folder's sandbox runs on, and the PATH
// that image declares (so its toolchains are found).
type EnvironmentImage struct {
	Image string
	Path  string
}

// EnvironmentResolver names a folder's environment image. ok is false for a
// folder without one; it then runs on the default image.
type EnvironmentResolver interface {
	SandboxEnvironment(root string) (EnvironmentImage, bool, error)
}

// SetEnvironmentResolver installs the lookup of folders' environment images.
func (m *Manager) SetEnvironmentResolver(resolver EnvironmentResolver) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.envResolver = resolver
	m.mu.Unlock()
}

// sandboxImage is the image a folder's sandbox runs on, and the identity that
// goes into its spec hash so a changed environment replaces the container.
func (m *Manager) sandboxImage(root string) (EnvironmentImage, string, error) {
	m.mu.Lock()
	resolver, defaultID := m.envResolver, m.imageID
	m.mu.Unlock()
	if resolver != nil {
		env, ok, err := resolver.SandboxEnvironment(root)
		if err != nil {
			return EnvironmentImage{}, "", fmt.Errorf("read the folder's environment: %w", err)
		}
		if ok && env.Image != "" {
			return env, "env:" + env.Image, nil
		}
	}
	return EnvironmentImage{Image: m.cfg.Image, Path: sandboxPath}, defaultID, nil
}

// EnvironmentBuild is one definition to build.
type EnvironmentBuild struct {
	Hash      string
	Image     string // the image name to produce (swarm-env:<hash>)
	BaseImage string
	Setup     []string
	Env       map[string]string
}

// EnvironmentBuildResult is the built image, its PATH and the build log.
type EnvironmentBuildResult struct {
	Image  string
	Path   string
	Log    string
	Reused bool
}

const (
	envBuildLabel      = "swarm.envbuild"
	envImageLabel      = "swarm.environment"
	envBuildLogMax     = 64 * 1024
	envCommandMaxBytes = 16 * 1024
)

// ErrEnvironmentBuildUnsafe refuses a build where setup commands would not
// run under gVisor.
var ErrEnvironmentBuildUnsafe = errors.New("environment builds run setup commands as root and need the gVisor runtime, which this machine's sandbox does not use")

type envLog struct{ b strings.Builder }

func (l *envLog) add(format string, args ...any) {
	if l.b.Len() < envBuildLogMax {
		fmt.Fprintf(&l.b, format, args...)
	}
}

func (l *envLog) output(out []byte) {
	text := string(out)
	if len(text) > envCommandMaxBytes {
		text = "…" + text[len(text)-envCommandMaxBytes:]
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	l.add("%s", text)
}

// BuildEnvironment builds a definition into b.Image, or reuses that image
// when it already exists (the name is the definition's hash). Setup commands
// run as root inside a temporary gVisor container on the sandbox network
// (the internet, not the host or private networks), with no host folder
// mounted; the result must run bash and git as an unprivileged user.
func (m *Manager) BuildEnvironment(ctx context.Context, b EnvironmentBuild) (EnvironmentBuildResult, error) {
	log := &envLog{}
	result := func(err error) (EnvironmentBuildResult, error) {
		return EnvironmentBuildResult{Log: log.b.String()}, err
	}
	if m == nil || !m.Active() {
		return result(ErrInactive)
	}
	m.mu.Lock()
	runtime := m.runtime
	m.mu.Unlock()
	if runtime != gvisorRuntime {
		return result(ErrEnvironmentBuildUnsafe)
	}
	if b.Image == "" || b.BaseImage == "" || len(b.Hash) < 12 {
		return result(errors.New("environment build is missing its image, base image or hash"))
	}
	engine := m.cfg.Engine

	if path, err := m.imagePath(ctx, b.Image); err == nil {
		log.add("Image %s already exists for this definition; reusing it.\n", b.Image)
		return EnvironmentBuildResult{Image: b.Image, Path: path, Log: log.b.String(), Reused: true}, nil
	}

	if _, err := m.runner.Run(ctx, engine, "image", "inspect", "--format", "{{.Id}}", b.BaseImage); err != nil {
		log.add("$ pull %s\n", b.BaseImage)
		out, err := m.runner.RunCombined(ctx, engine, "pull", b.BaseImage)
		log.output(out)
		if err != nil {
			return result(fmt.Errorf("could not pull base image %s: %w", b.BaseImage, err))
		}
	}

	name := "swarm-envbuild-" + b.Hash[:12]
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_, _ = m.runner.Run(cctx, engine, "rm", "-f", name)
	}
	cleanup()
	defer cleanup()

	args := []string{"run", "-d", "--name", name,
		"--label", envBuildLabel + "=1",
		"--runtime", runtime,
		"--user", "0:0",
		"--security-opt", "no-new-privileges",
		"--memory", m.cfg.Memory, "--memory-swap", m.cfg.Memory,
		"--pids-limit", strconv.Itoa(m.cfg.PidsLimit),
		"--network", m.cfg.Network,
		"--hostname", "sandbox",
		"--ipc", "private",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=2g,mode=1777",
		"--env", "LANG=C.UTF-8",
	}
	if m.cfg.CPUs != "" {
		args = append(args, "--cpus", m.cfg.CPUs)
	}
	for _, dns := range m.cfg.DNS {
		args = append(args, "--dns", dns)
	}
	if m.resolvConf != "" {
		args = append(args, "--mount", "type=bind,src="+m.resolvConf+",dst=/etc/resolv.conf,readonly")
	}
	names := make([]string, 0, len(b.Env))
	for key := range b.Env {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		args = append(args, "--env", key+"="+b.Env[key])
	}
	args = append(args, "--entrypoint", "sleep", b.BaseImage, "infinity")
	if out, err := m.runner.RunCombined(ctx, engine, args...); err != nil {
		log.output(out)
		return result(fmt.Errorf("could not start the build container from %s (it needs a sleep command): %w", b.BaseImage, err))
	}

	for i, command := range b.Setup {
		log.add("$ %s\n", command)
		out, err := m.runner.RunCombined(ctx, engine, "exec", "-u", "0", "-w", "/", name, "/bin/sh", "-c", command)
		log.output(out)
		if err != nil {
			return result(fmt.Errorf("setup command %d failed (%s): %w", i+1, truncateCommand(command), err))
		}
	}

	// The sandbox runs as an unprivileged user that has no account in the
	// image; Swarm's own Git and the agents' commands need bash and git.
	check := "git --version && bash --version | head -n 1"
	log.add("$ %s (as an unprivileged user)\n", check)
	out, err := m.runner.RunCombined(ctx, engine, "exec", "-u", strconv.Itoa(m.cfg.UID)+":"+strconv.Itoa(m.cfg.GID), "-e", "HOME=/tmp", "-w", "/tmp", name, "bash", "-c", check)
	log.output(out)
	if err != nil {
		return result(fmt.Errorf("the environment must provide bash and git for an unprivileged user (add them in setup, for example apt-get install -y bash git): %w", err))
	}

	log.add("$ commit %s\n", b.Image)
	out, err = m.runner.RunCombined(ctx, engine, "commit",
		"--change", "ENTRYPOINT []",
		"--change", `CMD ["sleep", "infinity"]`,
		"--change", "WORKDIR /",
		"--change", "LABEL "+envImageLabel+"="+b.Hash,
		name, b.Image)
	log.output(out)
	if err != nil {
		return result(fmt.Errorf("could not save the environment image: %w", err))
	}
	path, err := m.imagePath(ctx, b.Image)
	if err != nil {
		return result(fmt.Errorf("the saved environment image cannot be read: %w", err))
	}
	return EnvironmentBuildResult{Image: b.Image, Path: path, Log: log.b.String()}, nil
}

// imagePath reports an existing image's declared PATH (the default PATH when
// it declares none).
func (m *Manager) imagePath(ctx context.Context, image string) (string, error) {
	out, err := m.runner.Run(ctx, m.cfg.Engine, "image", "inspect", "--format", "{{json .Config.Env}}", image)
	if err != nil {
		return "", err
	}
	var env []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &env); err != nil {
		return "", err
	}
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok && value != "" {
			return value, nil
		}
	}
	return sandboxPath, nil
}

func truncateCommand(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	if len(command) > 120 {
		return command[:120] + "…"
	}
	return command
}
