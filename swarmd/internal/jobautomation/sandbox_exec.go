package jobautomation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
)

// SandboxExecutor runs run steps in the folder's sandbox, the same one jobs'
// Bash uses: sandbox.Default() → CommandScope(folder) → Manager.Exec, never
// ExecDaemon and never on the host. The script gets exactly the folder's
// secret grants, as the stand-ins the sandbox already carries.
type SandboxExecutor struct {
	// Manager returns the sandbox manager; nil means sandbox.Default.
	Manager func() *sandbox.Manager
	// Grants lists the secret names granted to a folder; nil skips the
	// check that every secret a step names is granted.
	Grants func(folder string) ([]string, error)
}

func (e SandboxExecutor) manager() *sandbox.Manager {
	if e.Manager != nil {
		return e.Manager()
	}
	return sandbox.Default()
}

// maxCaptureBytes bounds what the sandbox captures; the step keeps the tail.
const maxCaptureBytes = 1 << 20

func (e SandboxExecutor) Run(ctx context.Context, req RunRequest) (RunOutcome, error) {
	m := e.manager()
	if m == nil || !m.Active() {
		reason := "sandbox is not configured"
		if m != nil {
			reason = m.Status().Reason
		}
		return RunOutcome{}, &RefusedError{Reason: "the sandbox is not active (" + reason + "): run steps only run in the folder's sandbox"}
	}
	if len(req.Secrets) > 0 {
		if !m.SecretsGateway() {
			return RunOutcome{}, &RefusedError{Reason: "the step names secrets (" + strings.Join(req.Secrets, ", ") + ") but the secrets gateway is off: start Swarm with --secrets-gateway=on, or remove secrets from the step"}
		}
		if e.Grants != nil {
			granted, err := e.Grants(req.Folder)
			if err != nil {
				return RunOutcome{}, fmt.Errorf("read the folder's secret grants: %w", err)
			}
			have := map[string]bool{}
			for _, name := range granted {
				have[name] = true
			}
			for _, name := range req.Secrets {
				if !have[name] {
					return RunOutcome{}, &RefusedError{Reason: "secret " + name + " is not granted to " + req.Folder + ": grant it to the folder first"}
				}
			}
		}
	}
	folder := filepath.Clean(req.Folder)
	scope, err := m.CommandScope(folder)
	if err != nil {
		return RunOutcome{}, &RefusedError{Reason: "the folder has no sandbox: " + err.Error()}
	}
	if !sameDir(scope.Root, folder) {
		return RunOutcome{}, &RefusedError{Reason: fmt.Sprintf("%s is inside the project %s, not a project folder: set the automation's scope to the folder itself", folder, scope.Root)}
	}
	script, err := scriptPath(folder, req.Command)
	if err != nil {
		return RunOutcome{}, &RefusedError{Reason: err.Error()}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultStepTimeoutSeconds * time.Second
	}
	res, execErr := m.Exec(ctx, scope, sandbox.ExecRequest{
		Command: append([]string{script}, req.Args...), WorkingDir: folder, Env: req.Env, Stdin: bytes.NewReader(req.Stdin),
		Timeout: timeout, MaxTimeout: MaxStepTimeoutSeconds * time.Second, MaxOutput: maxCaptureBytes, OnOutput: req.OnOutput,
	})
	out := RunOutcome{}
	if res != nil {
		out.ExitCode = res.ExitCode
		out.Output = []byte(res.CombinedOutput())
	}
	if execErr != nil && res != nil && res.ExitCode != 0 {
		// The script ran and failed (the sandbox reports a kill as an
		// error with the exit code): its exit code is the outcome.
		return out, nil
	}
	return out, execErr
}

// scriptPath resolves a run step's command to an executable regular file
// inside the folder, following no link out of it.
func scriptPath(folder, command string) (string, error) {
	if err := ValidCommandPath(command); err != nil {
		return "", err
	}
	full := filepath.Join(folder, filepath.FromSlash(command))
	resolvedFolder, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", fmt.Errorf("the folder %s cannot be read: %v", folder, err)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", fmt.Errorf("%s is not a file in the folder: %v", command, err)
	}
	if rel, err := filepath.Rel(resolvedFolder, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s resolves outside the folder", command)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file in the folder", command)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable: chmod +x it and commit that", command)
	}
	return full, nil
}

func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
