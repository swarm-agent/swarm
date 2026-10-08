package sandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// The daemon runs Git from many packages. Repositories agents can write may
// carry hooks, fsmonitor, filters or other configuration that makes Git run
// programs, so Git on those repositories must run inside the project's
// sandbox, never as the daemon. RouteGit is the one gate every such call site
// passes its prepared command through.

var defaultManager atomic.Pointer[Manager]

// SetDefault installs the manager RouteGit uses. Daemon startup calls it once.
func SetDefault(m *Manager) { defaultManager.Store(m) }

// Default returns the installed manager, or nil.
func Default() *Manager { return defaultManager.Load() }

// ErrSandboxRequired reports Git on a sandboxed project while no sandbox is
// available; the command is refused rather than run on the host.
var ErrSandboxRequired = errors.New("this project runs in a sandbox and the sandbox is not available")

// pathEnv are Git environment variables naming paths; they must point inside
// the project's sandbox mounts.
var pathEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
}

// hostOnlyEnv never crosses into the sandbox: they name host programs or
// host credential channels.
var hostOnlyEnv = map[string]bool{
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "GIT_ASKPASS": true, "GIT_EXEC_PATH": true,
	"GIT_EDITOR": true, "GIT_PAGER": true, "GIT_SEQUENCE_EDITOR": true, "GIT_PROXY_COMMAND": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_TEMPLATE_DIR": true, "GIT_CONFIG_SYSTEM": true,
}

// RouteGit rewrites cmd, prepared as exec.Command("git", ...) with its Dir,
// Env and Stdin already set, so it runs in the sandbox of the project its
// target directory belongs to. It leaves cmd unchanged when the target is not
// agent-writable or no manager is installed, and returns an error (cmd must
// not be run) when the project needs a sandbox that is unavailable.
func RouteGit(ctx context.Context, cmd *exec.Cmd) error {
	return Default().RouteGit(ctx, cmd)
}

// Prepare is RouteGit for call sites that run cmd right after: a routing
// refusal is stored in cmd.Err, so the caller's own Run/Output error handling
// reports it and nothing runs.
func Prepare(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	if err := RouteGit(ctx, cmd); err != nil && cmd != nil {
		cmd.Err = err
	}
	return cmd
}

// Command is exec.CommandContext(ctx, "git", args...) prepared with Prepare.
// The target directory must be named by -C (or the daemon's working
// directory); callers that set Dir, Env or Stdin afterwards use Prepare
// instead, once those are set.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	return Prepare(ctx, exec.CommandContext(ctx, "git", args...))
}

// RoutedArgv is RouteGit for callers that run commands through their own
// runner: it returns the engine program and arguments that run
// `git args...` (with env) in the project's sandbox, or routed=false when
// the target is not agent-writable and the caller runs Git itself.
func RoutedArgv(ctx context.Context, env []string, args ...string) (name string, argv []string, routed bool, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	if err := RouteGit(ctx, cmd); err != nil {
		return "", nil, false, err
	}
	if filepath.Base(cmd.Args[0]) == "git" {
		return "", nil, false, nil
	}
	return cmd.Path, cmd.Args[1:], true, nil
}

// ScratchDir creates a private disposable directory for files a Git command
// on path must read or write (a temporary index, for example). For a
// sandboxed project it lives in the project's worktree bucket, which the
// sandbox mounts; otherwise in the system temp directory.
func ScratchDir(path, pattern string) (string, error) {
	m := Default()
	if m != nil && (m.Active() || m.Enforced()) {
		if scope, ok, err := m.ScopeFor(path); err == nil && ok && len(scope.Mounts) > 1 {
			base := filepath.Join(scope.Mounts[len(scope.Mounts)-1], ".swarm-scratch")
			if err := os.MkdirAll(base, 0o700); err != nil {
				return "", err
			}
			return os.MkdirTemp(base, pattern)
		}
	}
	return os.MkdirTemp("", pattern)
}

// RouteGit is the manager form of the package-level RouteGit.
func (m *Manager) RouteGit(ctx context.Context, cmd *exec.Cmd) error {
	if m == nil || cmd == nil {
		return nil
	}
	if len(cmd.Args) == 0 || filepath.Base(cmd.Args[0]) != "git" {
		return fmt.Errorf("sandbox git routing expects a git command, got %q", cmd.Args)
	}
	target, err := gitTarget(cmd)
	if err != nil {
		return err
	}
	scope, ok, err := m.ScopeFor(target)
	if err != nil {
		if m.Enforced() || errors.Is(err, errUnknownWorktree) {
			return err
		}
		return nil
	}
	if !ok {
		return nil
	}
	if !m.Active() {
		if m.Enforced() || m.WasSandboxed(scope.Root) {
			return fmt.Errorf("%w (%s): %s", ErrSandboxRequired, scope.Root, m.Status().Reason)
		}
		return nil
	}
	b, err := m.acquire(ctx, scope)
	if err != nil {
		return err
	}
	name, mounts := b.name, append([]string(nil), b.mounts...)
	m.release(b)

	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	execArgs := []string{"exec"}
	if cmd.Stdin != nil {
		execArgs = append(execArgs, "-i")
	}
	execArgs = append(execArgs, "-w", target)
	forwarded, err := sandboxGitEnv(env, mounts, target)
	if err != nil {
		return err
	}
	for _, kv := range forwarded {
		execArgs = append(execArgs, "-e", kv)
	}
	execArgs = append(execArgs, name)
	if deadline, ok := ctx.Deadline(); ok {
		// Killing the engine CLI does not stop the process in the container;
		// bound it there too.
		secs := int(math.Ceil(time.Until(deadline).Seconds()))
		if secs < 1 {
			secs = 1
		}
		execArgs = append(execArgs, "timeout", "--kill-after=2", fmt.Sprintf("%d", secs))
	}
	execArgs = append(execArgs, "git")
	execArgs = append(execArgs, cmd.Args[1:]...)

	enginePath, err := exec.LookPath(m.cfg.Engine)
	if err != nil {
		return fmt.Errorf("sandbox engine: %w", err)
	}
	cmd.Path = enginePath
	cmd.Args = append([]string{m.cfg.Engine}, execArgs...)
	cmd.Err = nil
	cmd.Dir = "/"
	cmd.Env = engineEnv(os.Environ())
	return nil
}

// gitTarget is the directory Git operates on: cmd.Dir adjusted by -C options,
// or the --git-dir value when nothing else names one.
func gitTarget(cmd *exec.Cmd) (string, error) {
	dir := cmd.Dir
	gitDir := ""
	args := cmd.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			next := args[i+1]
			if filepath.IsAbs(next) || dir == "" {
				dir = next
			} else {
				dir = filepath.Join(dir, next)
			}
			i++
		case a == "-c" && i+1 < len(args):
			i++
		case strings.HasPrefix(a, "--git-dir="):
			gitDir = strings.TrimPrefix(a, "--git-dir=")
		case strings.HasPrefix(a, "-"):
			// other global option
		default:
			i = len(args) // subcommand reached
		}
	}
	if dir == "" && gitDir != "" {
		dir = gitDir
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve git working directory: %w", err)
		}
		dir = wd
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		dir = abs
	}
	return filepath.Clean(dir), nil
}

// sandboxGitEnv forwards only Git and locale settings. Path-valued Git
// variables must resolve inside the sandbox's mounts; relative ones are taken
// relative to the target directory, as Git would.
func sandboxGitEnv(env []string, mounts []string, target string) ([]string, error) {
	out := make([]string, 0, 8)
	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case key == "LC_ALL" || key == "LANG" || key == "TZ":
		case strings.HasPrefix(key, "GIT_") && !hostOnlyEnv[key]:
			if key == "GIT_CONFIG_GLOBAL" && value != os.DevNull {
				continue // the host's global config path does not exist inside
			}
			if pathEnv[key] && value != "" {
				for _, p := range filepath.SplitList(value) {
					if !filepath.IsAbs(p) {
						p = filepath.Join(target, p)
					}
					p = resolveExisting(p)
					inside := false
					for _, mount := range mounts {
						if within(mount, p) {
							inside = true
							break
						}
					}
					if !inside {
						return nil, fmt.Errorf("%s points outside the project's sandbox: %s", key, p)
					}
				}
			}
		default:
			continue
		}
		out = append(out, key+"="+value)
	}
	sort.Strings(out)
	return out, nil
}

// engineEnv is the environment for the engine CLI itself: only what it needs
// to find its binary, configuration and daemon.
func engineEnv(env []string) []string {
	out := make([]string, 0, 6)
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "XDG_RUNTIME_DIR":
			out = append(out, kv)
		}
	}
	return out
}
