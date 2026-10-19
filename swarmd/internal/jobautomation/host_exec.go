package jobautomation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"swarm/packages/swarmd/internal/publish"
)

// HostExecutor runs a run step's script on the host, as the daemon's user,
// in the folder, the same way the publish command runs (publish.RunScript):
// a minimal environment (PATH, HOME, a per-step TMPDIR) plus the step's
// SWARM_* variables, a time limit and a process-group kill. A run step is
// the machine owner's own post-job script, not an agent command, so it may
// write the folder's repository and use the machine's own credentials. It
// runs only a script owned by root that group and others cannot write; a
// relative command must also resolve to a file inside the folder. The file
// is opened once, without following symlinks; that open file is checked and
// copied into a private folder of the step's, and the copy runs, so a file
// swapped in at the path after the check never runs.
type HostExecutor struct{}

// timeoutExitCode is a step's exit code when its time limit killed it (the
// code timeout(1) uses).
const timeoutExitCode = 124

// stepWaitDelay bounds the wait for a killed script's output when a child
// left its process group and still holds it.
const stepWaitDelay = 5 * time.Second

func (HostExecutor) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	folder := filepath.Clean(req.Folder)
	script, err := ResolveScript(folder, req.Command)
	if err != nil {
		return RunOutcome{}, &RefusedError{Reason: err.Error()}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultStepTimeoutSeconds * time.Second
	}
	if timeout > MaxStepTimeoutSeconds*time.Second {
		timeout = MaxStepTimeoutSeconds * time.Second
	}
	env := make([]string, 0, len(req.Env))
	for name, value := range req.Env {
		// Only the step's SWARM_* variables join the minimal environment.
		if strings.HasPrefix(name, "SWARM_") && !strings.ContainsAny(name, "=\x00") {
			env = append(env, name+"="+value)
		}
	}
	sort.Strings(env)
	scratch, err := os.MkdirTemp("", "swarm-step-*")
	if err != nil {
		return RunOutcome{}, fmt.Errorf("create the step's scratch folder: %w", err)
	}
	defer os.RemoveAll(scratch)
	// Check the resolved file again immediately before running it, on the
	// open file whose contents run.
	checked, info, err := publish.OpenOwnedByRoot(script, true)
	if err != nil {
		return RunOutcome{}, &RefusedError{Reason: scriptRefusal(req.Command, "", script, err)}
	}
	defer checked.Close()
	if !filepath.IsAbs(filepath.FromSlash(req.Command)) {
		// A hard link is the one way to put a root-owned file (a shell, say)
		// in a folder without root: a script in the folder has no other link.
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			return RunOutcome{}, &RefusedError{Reason: fmt.Sprintf("%s has other hard links, so it may be a file from outside the folder: a script in the folder must have exactly one link; install it with sudo install -o root -m 755 SCRIPT %s", req.Command, script)}
		}
	}
	run, cleanup, err := copyChecked(checked, filepath.Base(script))
	if err != nil {
		return RunOutcome{}, err
	}
	defer cleanup()
	out := &stepOutput{tail: tailBuffer{max: MaxLogTailBytes}, onOutput: req.OnOutput}
	var timedOut bool
	var runErr error
	for attempt := 0; ; attempt++ {
		timedOut, runErr = publish.RunScript(ctx, publish.Script{
			Path: run, Args: req.Args, Dir: folder, Env: env, Scratch: scratch,
			Stdin: bytes.NewReader(req.Stdin), Stdout: out, Stderr: out,
			Timeout: timeout, WaitDelay: stepWaitDelay,
		})
		// ETXTBSY: a process another goroutine was starting still held the
		// copy's write handle (golang/go#22315). The exec never started, so
		// the script did not run; try again shortly.
		if !errors.Is(runErr, syscall.ETXTBSY) || attempt == 4 || ctx.Err() != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if timedOut {
		out.Write([]byte(fmt.Sprintf("\nswarm: the step timed out after %s; its processes were killed\n", timeout)))
		return RunOutcome{ExitCode: timeoutExitCode, Output: out.tail.bytes()}, nil
	}
	if runErr != nil && ctx.Err() != nil {
		// The daemon is stopping: the step is resumed, not recorded.
		return RunOutcome{Output: out.tail.bytes()}, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		code := exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
		return RunOutcome{ExitCode: code, Output: out.tail.bytes()}, nil
	}
	return RunOutcome{Output: out.tail.bytes()}, runErr
}

// copyChecked copies the checked script (an open file) into a new private
// folder (mode 0700, the daemon user's) as name, mode 0500, and returns its
// path and a cleanup that removes the folder. The original is owned by root
// and nobody else can write it, so the copy is exactly the file checked;
// running the copy, not the path, means a file swapped in at the path
// between the check and the run never runs.
func copyChecked(checked *os.File, name string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "swarm-step-script-*")
	if err != nil {
		return "", nil, fmt.Errorf("create the step's script folder: %w", err)
	}
	cleanup = func() { os.RemoveAll(dir) }
	path = filepath.Join(dir, name)
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err == nil {
		_, err = io.Copy(dst, checked)
		if closeErr := dst.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = os.Chmod(path, 0o500)
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy the checked script: %w", err)
	}
	return path, cleanup, nil
}

// ResolveScript resolves a run step's command (an absolute path, or a path
// relative to the folder), symlinks included, and returns the final file if
// it is owned by root, not writable by group or others, executable and not
// in a .git directory, and, for a relative command, inside the folder (a
// link in the folder never reaches a root-owned program elsewhere, such as
// a shell). The refusal gives the reason and the fix.
func ResolveScript(folder, command string) (string, error) {
	if err := ValidCommandPath(command); err != nil {
		return "", err
	}
	full := filepath.FromSlash(command)
	if !filepath.IsAbs(full) {
		full = filepath.Join(folder, full)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s does not exist: install the script with sudo install -o root -m 755 SCRIPT %s", command, full)
		}
		return "", fmt.Errorf("%s cannot be read: %v", command, err)
	}
	if !filepath.IsAbs(resolved) {
		if resolved, err = filepath.Abs(resolved); err != nil {
			return "", fmt.Errorf("%s cannot be resolved: %v", command, err)
		}
	}
	if !filepath.IsAbs(filepath.FromSlash(command)) {
		root, err := filepath.EvalSymlinks(folder)
		if err != nil {
			return "", fmt.Errorf("the folder %s cannot be resolved: %v", folder, err)
		}
		if rel, err := filepath.Rel(root, resolved); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%s resolves to %s, outside the folder: a relative command must resolve to a file inside the folder; to run a script elsewhere, give its absolute path", command, resolved)
		}
	}
	if inGitDir(resolved) {
		return "", fmt.Errorf("%s resolves to %s, inside a .git directory: a run step's script is never under .git", command, resolved)
	}
	if err := publish.OwnedByRoot(resolved, true); err != nil {
		link := ""
		if dir, dirErr := filepath.EvalSymlinks(filepath.Dir(full)); dirErr != nil || filepath.Join(dir, filepath.Base(full)) != resolved {
			link = resolved
		}
		return "", errors.New(scriptRefusal(command, link, resolved, err))
	}
	return resolved, nil
}

// scriptRefusal is why a script failed the root-owner check, and the fix.
// link is the file the command's own symlink resolves to, if it is one.
func scriptRefusal(command, link, resolved string, err error) string {
	name := command
	if link != "" {
		name += " (a link to " + link + ")"
	}
	var why string
	switch {
	case errors.Is(err, publish.ErrNotRootOwned):
		why = "it is not owned by root"
	case errors.Is(err, publish.ErrWritable):
		why = "group or others can write it"
	case errors.Is(err, publish.ErrNotExecutable):
		why = "it is not executable"
	case errors.Is(err, publish.ErrNotRegular):
		why = "it is not a regular file"
	case errors.Is(err, publish.ErrChanged):
		return fmt.Sprintf("%s changed while it was being checked (a symlink or rename in its path): it did not run; install it where only root can change it, with sudo install -o root -m 755 SCRIPT %s", name, resolved)
	default:
		why = err.Error()
	}
	return fmt.Sprintf("%s must be owned by root and not writable by group or others (%s): install it with sudo install -o root -m 755 SCRIPT %s", name, why, resolved)
}

func inGitDir(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

// stepOutput is a step's stdout and stderr, in order: its tail is the
// step's output, and every write also goes to the runner's log tail.
type stepOutput struct {
	tail     tailBuffer
	onOutput func([]byte)
}

func (o *stepOutput) Write(p []byte) (int, error) {
	o.tail.write(p)
	if o.onOutput != nil {
		o.onOutput(append([]byte(nil), p...))
	}
	return len(p), nil
}
