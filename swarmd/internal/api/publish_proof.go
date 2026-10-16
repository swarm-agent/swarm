package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
)

// Proof verification is opt-in: only a folder rule with require-proof enables it.
// Otherwise declarations and explicit proof commands cannot block landing.
// A committed .swarm-proof.json lets a coder declare {"command":"bash repro.sh",
// "files":["repro.sh"]}. Only newly added regular files may be carried to base;
// copying changed product files would make the negative control meaningless.
func (c *controlMCPCall) verifyLandProof(ctx context.Context, rule publish.Rule, e *publish.Entry) error {
	if ignoreLandProof(rule, e) {
		return nil
	}
	kind, a, b, err := controlMCPParseJobID(map[string]any{"job_id": e.Request.JobID})
	if err != nil {
		return err
	}
	refs, err := c.jobRefs(kind, a, b)
	if err != nil {
		return err
	}
	command, _ := controlMCPMap(refs.run["input"])["proof"].(string)
	view, err := c.jobView(kind, a, b, 0, false, 0)
	if err != nil {
		return err
	}
	path, _ := controlMCPJobResultJSON(view)["workspace_path"].(string)
	if path == "" {
		return errors.New("job worktree is unavailable")
	}
	m := sandbox.Default()
	if m == nil || !m.Active() {
		return errors.New("landing proof requires an active sandbox")
	}
	scope, err := m.CommandScope(path)
	if err != nil {
		return err
	}
	if scope.Root != rule.Folder {
		return errors.New("job sandbox does not belong to publish folder")
	}
	return verifyPublishProof(ctx, rule, e, command, path, func(ctx context.Context, dir, commit, command string) *publish.ProofResult {
		return runSandboxProof(ctx, dir, commit, command, func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
			return m.ExecDaemon(ctx, scope, req)
		})
	})
}

func proofGit(ctx context.Context, folder string, args ...string) ([]byte, error) {
	cmd := sandbox.Command(ctx, append([]string{"-C", folder}, args...)...)
	out := &proofGitOutput{}
	cmd.Stdout = out
	stderr := &proofGitOutput{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		diagnostic := strings.TrimSpace(string(stderr.Bytes()))
		lines := strings.Split(diagnostic, "\n")
		if len(lines) > 8 {
			diagnostic = strings.Join(lines[len(lines)-8:], "\n")
		}
		if diagnostic != "" {
			return nil, fmt.Errorf("%s: %w", diagnostic, err)
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// Bound declaration and blob reads before allocation, including malicious trees.
type proofGitOutput struct{ buffer bytes.Buffer }

func (w *proofGitOutput) Bytes() []byte { return w.buffer.Bytes() }

func (w *proofGitOutput) Write(p []byte) (int, error) {
	if w.buffer.Len()+len(p) > 1024*1024 {
		return 0, errors.New("proof Git output exceeds 1 MiB")
	}
	return w.buffer.Write(p)
}

func ignoreLandProof(rule publish.Rule, e *publish.Entry) bool {
	if rule.RequireProof {
		return false
	}
	e.Proof = nil
	slog.Info("proof declaration ignored: folder rule has no require-proof")
	return true
}

func verifyPublishProof(ctx context.Context, rule publish.Rule, e *publish.Entry, command, repository string, run func(context.Context, string, string, string) *publish.ProofResult) error {
	if ignoreLandProof(rule, e) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	e.Proof = nil
	proof := &publish.Proof{Command: strings.TrimSpace(command)}
	// A committed declaration applies only when this job introduces or changes
	// it. An unchanged declaration inherited from an earlier landing is not this
	// job's proof (nor a source of files for an explicit API proof command).
	names, err := proofGit(ctx, repository, "diff", "--no-ext-diff", "--no-renames", "--name-only", "--diff-filter=AM", e.Request.BaseCommit, e.Request.Head, "--", ".swarm-proof.json")
	if err != nil {
		return fmt.Errorf("proof declaration: %w", err)
	}
	if strings.TrimSpace(string(names)) != "" {
		data, err := proofGit(ctx, repository, "show", e.Request.Head+":.swarm-proof.json")
		if err != nil {
			return err
		}
		if len(data) > 16384 {
			return errors.New("proof declaration exceeds 16 KiB")
		}
		var declared struct {
			Command string   `json:"command"`
			Files   []string `json:"files"`
		}
		if err := json.Unmarshal(data, &declared); err != nil {
			return fmt.Errorf("proof declaration: %w", err)
		}
		if proof.Command == "" {
			proof.Command = strings.TrimSpace(declared.Command)
		}
		proof.Files = declared.Files
	}
	if proof.Command == "" {
		if rule.RequireProof {
			return &publish.SendBackError{Reason: "landing requires a proof: add a committed .swarm-proof.json with a command and reproduction files"}
		}
		return nil
	}
	if len(proof.Command) > 8192 || len(proof.Files) > 32 {
		return errors.New("proof declaration exceeds limits")
	}
	e.Proof = proof
	// Use daemon-owned mounted scratch, not the sandbox-owned job workspace.
	// Its shared group permits the folder-owner sandbox to access checkouts.
	scratch, err := sandbox.SharedScratchDir(repository, ".swarm-proof-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	baseDir, headDir := filepath.Join(scratch, "base"), filepath.Join(scratch, "head")
	for _, item := range []struct{ dir, rev string }{{baseDir, e.Request.BaseCommit}, {headDir, e.Request.Head}} {
		if _, err := proofGit(ctx, repository, "worktree", "add", "--detach", item.dir, item.rev); err != nil {
			return fmt.Errorf("proof checkout: %w", err)
		}
		defer func(dir string) {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, _ = proofGit(cleanup, repository, "worktree", "remove", "--force", dir)
		}(item.dir)
	}
	for _, name := range proof.Files {
		if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || strings.HasPrefix(name, ".git") {
			return errors.New("proof files must be relative regular file paths")
		}
		// Git metadata, not a host-followed symlink, establishes the file type.
		tree, err := proofGit(ctx, repository, "ls-tree", e.Request.Head, "--", name)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(tree), "100644 blob ") && !strings.HasPrefix(string(tree), "100755 blob ") {
			return errors.New("proof file must be a committed regular file")
		}
		existing, err := proofGit(ctx, repository, "ls-tree", e.Request.BaseCommit, "--", name)
		if err != nil {
			return err
		}
		if len(existing) != 0 {
			return errors.New("proof files copied to base must be newly added, not changed product files")
		}
		data, err := proofGit(ctx, repository, "show", e.Request.Head+":"+name)
		if err != nil {
			return err
		}
		if len(data) > 1024*1024 {
			return errors.New("proof file exceeds 1 MiB")
		}
		// Reject symlink ancestors in the base checkout before creating anything.
		parent := baseDir
		parts := strings.Split(name, "/")
		for _, part := range parts[:len(parts)-1] {
			parent = filepath.Join(parent, part)
			st, err := os.Lstat(parent)
			if err == nil && !st.IsDir() {
				return errors.New("proof file parent is not a directory")
			}
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if os.IsNotExist(err) {
				if err := os.Mkdir(parent, 0770); err != nil {
					return err
				}
			}
		}
		if err := os.WriteFile(filepath.Join(baseDir, name), data, 0770); err != nil {
			return err
		}
	}
	proof.Base = run(ctx, baseDir, e.Request.BaseCommit, proof.Command)
	proof.Head = run(ctx, headDir, e.Request.Head, proof.Command)
	if e.Checkpoint != nil {
		if err := e.Checkpoint(); err != nil {
			return err
		}
	}
	if proof.Base.Error != "" || proof.Head.Error != "" {
		return errors.New("proof execution infrastructure failure; inspect proof.base and proof.head")
	}
	if proof.Base.ExitCode == 0 {
		return &publish.SendBackError{Reason: "the reproduction does not reproduce the problem"}
	}
	if proof.Head.ExitCode != 0 {
		return &publish.SendBackError{Reason: "the fix does not fix it"}
	}
	return nil
}

type proofOutput struct {
	mu   sync.Mutex
	data []byte
}

func (w *proofOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.data = append(w.data, p...)
	if len(w.data) > 4096 {
		w.data = append([]byte(nil), w.data[len(w.data)-4096:]...)
	}
	return n, nil
}

// Deliberately host-side, with no daemon credentials inherited. Process-group
// cancellation and WaitDelay bound descendants as well as the shell.
func runProof(ctx context.Context, dir, commit, command string) *publish.ProofResult {
	result := &publish.ProofResult{Commit: commit, ExitCode: -1}
	cmd := exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", command)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	output := &proofOutput{}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	result.Output = string(output.data)
	if ctx.Err() != nil {
		result.Error = ctx.Err().Error()
		return result
	}
	if err == nil {
		result.ExitCode = 0
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		result.ExitCode = exit.ExitCode()
		if result.ExitCode == 126 || result.ExitCode == 127 {
			result.Error = "proof command unavailable: " + err.Error()
		}
	} else {
		result.Error = err.Error()
	}
	return result
}

// Proof commands are untrusted job code, executed only in the landing sandbox.
func runSandboxProof(ctx context.Context, dir, commit, command string, execute func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error)) *publish.ProofResult {
	result := &publish.ProofResult{Commit: commit, ExitCode: -1}
	out, err := execute(ctx, sandbox.ExecRequest{Command: []string{"bash", "-o", "pipefail", "-lc", command}, WorkingDir: dir, Timeout: 10 * time.Minute, MaxTimeout: 10 * time.Minute, MaxOutput: 4096})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if ctx.Err() != nil {
		result.Error = ctx.Err().Error()
		return result
	}
	if out == nil {
		result.Error = "proof executor returned no result"
		return result
	}
	result.ExitCode = out.ExitCode
	output := &proofOutput{}
	_, _ = output.Write([]byte(out.Stdout))
	_, _ = output.Write([]byte(out.Stderr))
	result.Output = string(output.data)
	if out.ExitCode < 0 || out.ExitCode == 126 || out.ExitCode == 127 {
		result.Error = "proof command unavailable or interrupted"
	}
	return result
}
