package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
)

const (
	BackendDocker     = "docker"
	BackendBubblewrap = "bubblewrap"
)

// Local identifies the per-process Linux executor, with no image or lease.
func (m *Manager) Local() bool { return m != nil && m.cfg.Backend == BackendBubblewrap }

func newLocalManager(ctx context.Context, cfg Config) (*Manager, error) {
	if cfg.MaxWorkerRuns < 0 {
		return nil, fmt.Errorf("worker_max_concurrent_runs must be non-negative")
	}
	if cfg.MaxWorkerRuns == 0 {
		cfg.MaxWorkerRuns = max(1, runtime.NumCPU())
	}
	m := &Manager{cfg: cfg, boxes: map[string]*box{}, used: map[string]struct{}{}, stop: make(chan struct{}), localRuns: map[string]int{}}
	m.status = Status{Mode: cfg.Mode, Runtime: BackendBubblewrap}
	if cfg.Mode == ModeOff {
		m.status.Reason = "sandbox mode is off"
		return m, nil
	}
	if cfg.Mode != ModeRequired {
		return nil, fmt.Errorf("bubblewrap requires required or off mode")
	}
	if runtime.GOOS != "linux" {
		m.status.Reason = "bubblewrap requires Linux"
		return m, nil
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		m.status.Reason = "bubblewrap unavailable: install bwrap to enable execution"
		return m, nil
	}
	m.cfg.Engine = path
	// Probe the actual namespace contract; finding an executable is insufficient.
	probe, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	args := localBaseArgs()
	args = append(args, "--", "/usr/bin/true")
	cmd := exec.CommandContext(probe, path, args...)
	cmd.Env = []string{}
	if out, err := cmd.CombinedOutput(); err != nil {
		m.status.Reason = fmt.Sprintf("bubblewrap namespace protection unavailable: %v: %s", err, strings.TrimSpace(string(out)))
		return m, nil
	}
	m.status.Active = true
	dir, err := os.MkdirTemp("", "swarm-isolation-")
	if err != nil {
		return nil, fmt.Errorf("create isolated Git config: %w", err)
	}
	m.localEmptyConfig = filepath.Join(dir, "config")
	if err := os.WriteFile(m.localEmptyConfig, nil, 0400); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return m, nil
}

func localBaseArgs() []string {
	args := []string{"--unshare-all", "--unshare-user", "--disable-userns", "--die-with-parent", "--new-session", "--cap-drop", "ALL", "--clearenv"}
	// Only system runtime files are visible. No host /etc, /run, /tmp or home.
	for _, p := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", sandboxHome, "--setenv", "HOME", sandboxHome, "--setenv", "PATH", sandboxPath, "--setenv", "TMPDIR", "/tmp", "--setenv", "LANG", "C.UTF-8")
	return args
}

func (m *Manager) localArgv(scope Scope, req ExecRequest) ([]string, error) {
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("sandbox command is required")
	}
	if !m.Active() {
		return nil, fmt.Errorf("%w: %s", ErrSandboxRequired, m.Status().Reason)
	}
	root := scope.Root
	if scope.JobCheckout != "" {
		root = scope.JobCheckout
	}
	if scope.ReviewCheckout != "" {
		root = scope.ReviewCheckout
	}
	mounts, err := m.validateMounts([]string{root})
	if err != nil {
		return nil, err
	}
	root = mounts[0]
	for _, runtimeRoot := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		p := resolveForCompare(runtimeRoot)
		if p != "" && (within(p, root) || within(root, p)) {
			return nil, fmt.Errorf("assigned checkout overlaps read-only system runtime")
		}
	}
	wd := resolveExisting(req.WorkingDir)
	if !within(root, wd) {
		return nil, fmt.Errorf("command working directory is outside assigned checkout")
	}
	args := localBaseArgs()
	bind := "--bind"
	if scope.ReviewCheckout != "" {
		bind = "--ro-bind"
	}
	args = append(args, bind, root, root)
	gitArgs, err := m.localGitMounts(root, scope.Root, scope.ReviewCheckout != "" || !scope.daemonGit)
	if err != nil {
		return nil, err
	}
	args = append(args, gitArgs...)
	// A registered workspace can contain a nested workspace or managed storage.
	// Its broader bind must never grant write access to those peer checkouts.
	peers, layout, err := m.projectRoots()
	if err != nil {
		return nil, err
	}
	if layout.WorktreesRoot != "" {
		peers = append(peers, resolveExisting(layout.WorktreesRoot))
	}
	for _, peer := range peers {
		if peer != root && within(root, peer) {
			if _, err := os.Stat(peer); err == nil {
				args = append(args, "--ro-bind", peer, peer)
			}
		}
	}
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "HOME" || k == "PATH" || k == "TMPDIR" || k == "LD_PRELOAD" || k == "LD_LIBRARY_PATH" || k == "BASH_ENV" || k == "ENV" {
			continue
		}
		args = append(args, "--setenv", k, req.Env[k])
	}
	args = append(args, "--chdir", wd, "--")
	return append(args, withSandboxUmask(req.Command...)...), nil
}

// Linked checkouts share objects and branch refs, but never mount the source
// checkout or another checkout's metadata writable. Existing peer refs are
// mounted read-only so lock-and-rename cannot replace them. Objects remain a
// shared Git database: this is filesystem isolation, not an append-only store.
func (m *Manager) localGitMounts(root, source string, readonly bool) ([]string, error) {
	dot := filepath.Join(root, ".git")
	if st, err := os.Lstat(dot); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlinked Git directory is not supported")
	}
	data, err := os.ReadFile(dot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		if st, e := os.Stat(dot); e == nil && st.IsDir() {
			if readonly {
				return []string{"--ro-bind", dot, dot}, nil
			}
			args := localGuardArgs(root)
			if !readonly {
				args = append(args, m.localDisabledConfig(filepath.Join(root, ".git"))...)
			}
			return args, nil
		}
		return nil, err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir: ") {
		return nil, fmt.Errorf("invalid linked checkout gitdir")
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	gitdir = resolveExisting(gitdir)
	backlink, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil || resolveExisting(strings.TrimSpace(string(backlink))) != resolveExisting(dot) {
		return nil, fmt.Errorf("linked checkout metadata does not belong to assigned checkout")
	}
	commonData, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return nil, fmt.Errorf("linked checkout common dir: %w", err)
	}
	common := strings.TrimSpace(string(commonData))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	common = resolveExisting(common)
	if source != root && common != resolveExisting(filepath.Join(source, ".git")) {
		return nil, fmt.Errorf("linked checkout common directory is outside assigned project")
	}
	if source != root && resolveExisting(filepath.Join(source, ".git")) != filepath.Join(source, ".git") {
		return nil, fmt.Errorf("symlinked source Git directory is not supported")
	}
	if source == root {
		roots, _, err := m.projectRoots()
		if err != nil {
			return nil, err
		}
		known := false
		for _, project := range roots {
			if common == filepath.Join(project, ".git") && common == resolveExisting(filepath.Join(project, ".git")) {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("linked checkout source is not registered")
		}
	}
	if _, err := m.validateMounts([]string{common, gitdir}); err != nil {
		return nil, err
	}
	if !within(filepath.Join(common, "worktrees"), gitdir) || gitdir == filepath.Join(common, "worktrees") {
		return nil, fmt.Errorf("unsupported linked checkout metadata layout")
	}
	if err := m.localMetadataDir(common, common); err != nil {
		return nil, err
	}
	if err := m.localMetadataDir(common, gitdir); err != nil {
		return nil, err
	}
	args := []string{"--ro-bind", dot, dot, "--ro-bind", common, common}
	if readonly {
		return args, nil
	}
	args = append(args, "--bind", gitdir, gitdir)
	args = append(args, m.localDisabledConfig(common)...)
	args = append(args, m.localDisabledConfig(gitdir)...)
	objects := filepath.Join(common, "objects")
	if _, err := os.Stat(objects); err == nil {
		if err := m.localMetadataDir(common, objects); err != nil {
			return nil, err
		}
		args = append(args, "--bind", objects, objects)
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(strings.TrimPrefix(string(head), "ref: "))
	if strings.HasPrefix(string(head), "ref: ") {
		if !strings.HasPrefix(ref, "refs/heads/") || strings.Contains(ref, "..") || strings.ContainsAny(ref, "\x00\n") {
			return nil, fmt.Errorf("unsupported linked checkout branch")
		}
		for _, base := range []string{"", "logs"} {
			own := filepath.Join(common, base, ref)
			parent := filepath.Dir(own)
			if st, e := os.Stat(parent); e == nil && st.IsDir() {
				if err := m.localMetadataDir(common, parent); err != nil {
					return nil, err
				}
				args = append(args, "--bind", parent, parent)
				entries, e := os.ReadDir(parent)
				if e != nil {
					return nil, e
				}
				for _, entry := range entries {
					p := filepath.Join(parent, entry.Name())
					if p != own && p != own+".lock" {
						if err := m.localMetadataDir(common, p); err != nil {
							return nil, err
						}
						args = append(args, "--ro-bind", p, p)
					}
				}
			}
		}
	}
	return args, nil
}

// Git metadata may be agent-influenced. Never let a symlink turn a narrow
// writable metadata bind into a mount of another host directory.
func (m *Manager) localMetadataDir(common, path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) || !within(common, resolved) {
		return fmt.Errorf("symlinked or external Git metadata is not supported")
	}
	_, err = m.validateMounts([]string{path})
	return err
}
func localGuardArgs(root string) []string {
	var args []string
	for _, g := range gitGuardMounts(root) {
		args = append(args, "--ro-bind", g.HostPath, g.Dest)
	}
	return args
}

// Output capture stays bounded while delivering the complete stream to the
// caller's bounded tool writer. exec.Cmd serializes the shared stdout/stderr.
type localOutput struct {
	mu        sync.Mutex
	bytes     []byte
	limit     int
	truncated bool
	onOutput  func([]byte)
}

func (w *localOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.onOutput != nil {
		w.onOutput(p)
	}
	n := len(p)
	remaining := w.limit - len(w.bytes)
	if remaining < len(p) {
		w.truncated = true
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	w.bytes = append(w.bytes, p...)
	return n, nil
}
func (m *Manager) execLocal(ctx context.Context, scope Scope, req ExecRequest) (*provider.ExecResult, error) {
	release, err := m.localAdmit(scope)
	if err != nil {
		return nil, err
	}
	defer release()
	args, err := m.localArgv(scope, req)
	if err != nil {
		return nil, err
	}
	timeout := req.Timeout
	if req.MaxTimeout > 0 && (timeout <= 0 || timeout > req.MaxTimeout) {
		timeout = req.MaxTimeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	run, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(run, m.cfg.Engine, args...)
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	limit := req.MaxOutput
	if limit <= 0 {
		limit = 1 << 20
	}
	out := &localOutput{limit: limit, onOutput: req.OnOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Stdin = req.Stdin
	start := time.Now()
	err = cmd.Run()
	result := &provider.ExecResult{DurationMS: time.Since(start).Milliseconds(), Stdout: string(out.bytes), Truncated: out.truncated, LastObservedAt: time.Now()}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exit.ExitCode()
		} else if run.Err() == nil {
			return result, err
		}
	}
	if run.Err() != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("%w: %v", provider.ErrOperationCancelled, ctx.Err())
		}
		return result, provider.ErrOperationTimedOut
	}
	return result, nil
}

func (m *Manager) routeLocalGit(ctx context.Context, cmd *exec.Cmd, target string) error {
	if !m.Active() {
		return fmt.Errorf("%w: %s", ErrSandboxRequired, m.Status().Reason)
	}
	scope, err := m.CommandScope(target)
	if err != nil {
		return err
	}
	scope = jobScope(ctx, scope)
	scope, err = m.enforceReviewIdentity(ctx, scope, target)
	if err != nil {
		return err
	}
	scope.daemonGit = scope.ReviewCheckout == ""
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	root := scope.Root
	if scope.JobCheckout != "" {
		root = scope.JobCheckout
	}
	if scope.ReviewCheckout != "" {
		root = scope.ReviewCheckout
	}
	if _, err := m.localGitMounts(root, scope.Root, true); err != nil {
		return err
	}
	allowedMounts := []string{root}
	if admin, err := localGitAdmin(root); err == nil {
		allowedMounts = append(allowedMounts, admin)
	}
	allowed, err := sandboxGitEnv(env, allowedMounts, target)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, kv := range allowed {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE", "GIT_INDEX_FILE", "GIT_OPTIONAL_LOCKS":
			values[k] = v
		}
	}
	lifecycleMounts, err := m.localWorktreeMounts(scope, target, cmd.Args[1:])
	if err != nil {
		return err
	}
	args, err := m.localArgv(scope, ExecRequest{WorkingDir: target, Command: append([]string{"git", "-c", "safe.directory=" + target, "-c", "user.name=Swarm", "-c", "user.email=swarm@localhost", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper="}, cmd.Args[1:]...), Env: values})
	if err != nil {
		return err
	}
	for i, a := range args {
		if a == "--chdir" {
			args = append(append(append([]string{}, args[:i]...), lifecycleMounts...), args[i:]...)
			break
		}
	}
	release, err := m.localAdmit(scope)
	if err != nil {
		return err
	}
	gitLeases.Store(cmd, release)
	cmd.Path = m.cfg.Engine
	cmd.Args = append([]string{m.cfg.Engine}, args...)
	cmd.Err = nil
	cmd.Dir = "/"
	cmd.Env = []string{}
	return nil
}

func (m *Manager) localAdmit(scope Scope) (func(), error) {
	root := scope.Root
	if scope.JobCheckout != "" {
		root = scope.JobCheckout
	}
	if scope.ReviewCheckout != "" {
		root = scope.ReviewCheckout
	}
	root = resolveExisting(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	if scope.ReviewCheckout != "" && m.reviewClosing[scope.ReviewCheckout] {
		return nil, fmt.Errorf("review sandbox is closing")
	}
	m.localRuns[root]++
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.localRuns[root]--
			if m.localRuns[root] == 0 {
				delete(m.localRuns, root)
			}
		})
	}, nil
}

// Canonical daemon Git may write shared objects, so repository config must not
// introduce executable filters, include files, helpers, fsmonitor or hooks.
func (m *Manager) localDisabledConfig(gitdir string) []string {
	var args []string
	for _, name := range []string{"config", "config.worktree"} {
		p := filepath.Join(gitdir, name)
		if _, err := os.Lstat(p); err == nil {
			args = append(args, "--ro-bind", m.localEmptyConfig, p)
		}
	}
	return args
}

// Only canonical daemon Git lifecycle commands can expose managed storage.
// Bash never receives a bucket mount. Adds mount an empty destination; removes
// mount its parent with every peer entry read-only to permit directory unlink.
func (m *Manager) localWorktreeMounts(scope Scope, target string, argv []string) ([]string, error) {
	args := argv
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "-C", "-c", "--git-dir", "--work-tree":
			if len(args) < 2 {
				return nil, fmt.Errorf("incomplete Git global option")
			}
			args = args[2:]
		default:
			args = args[1:]
		}
	}
	if len(args) < 2 || args[0] != "worktree" || (args[1] != "add" && args[1] != "remove") {
		return nil, nil
	}
	operation := args[1]
	if scope.ReviewCheckout != "" {
		return nil, fmt.Errorf("reviewer cannot mutate worktree lifecycle")
	}
	args = args[2:]
	destination := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-b" || a == "-B" || a == "--reason" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		destination = a
		break
	}
	if destination == "" {
		return nil, fmt.Errorf("Git worktree destination is required")
	}
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(target, destination)
	}
	destination = filepath.Clean(destination)
	if resolveExisting(destination) != destination {
		return nil, fmt.Errorf("symlinked worktree destination is unsupported")
	}
	_, layout, err := m.projectRoots()
	if err != nil {
		return nil, err
	}
	if layout.Bucket == nil || layout.WorktreesRoot == "" {
		return nil, fmt.Errorf("worktree lifecycle requires registered managed storage")
	}
	bucket, err := layout.Bucket(scope.Root)
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(layout.WorktreesRoot, bucket)
	if filepath.Dir(destination) != parent || strings.HasPrefix(filepath.Base(destination), ".") {
		return nil, fmt.Errorf("Git worktree destination is outside the assigned managed bucket")
	}
	if err := m.localProspectiveMount(parent); err != nil {
		return nil, err
	}
	if err := m.localProspectiveMount(destination); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if _, err := m.validateMounts([]string{parent}); err != nil {
		return nil, err
	}
	if operation == "add" {
		if err := os.Mkdir(destination, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		if _, err := m.validateMounts([]string{destination}); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(destination)
		if err != nil {
			return nil, err
		}
		if len(entries) != 0 {
			return nil, fmt.Errorf("new worktree destination must be empty")
		}
		return []string{"--bind", destination, destination}, nil
	}
	if _, err := m.localGitMounts(destination, scope.Root, true); err != nil {
		return nil, err
	}
	m.mu.Lock()
	active := m.localRuns[destination] != 0
	m.mu.Unlock()
	if active {
		return nil, ErrCleanupDeferred
	}
	mounts := []string{"--bind", parent, parent}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		p := filepath.Join(parent, entry.Name())
		if p == destination {
			continue
		}
		if resolveExisting(p) != p {
			return nil, fmt.Errorf("symlinked managed peer is unsupported")
		}
		if _, err := m.validateMounts([]string{p}); err != nil {
			return nil, err
		}
		mounts = append(mounts, "--ro-bind", p, p)
	}
	return mounts, nil
}

func (m *Manager) localProspectiveMount(path string) error {
	path = resolveExisting(path)
	if path == "/" {
		return fmt.Errorf("cannot mount filesystem root")
	}
	for _, protected := range m.cfg.ProtectedRoots {
		p := resolveForCompare(protected)
		if p != "" && (within(p, path) || within(path, p)) {
			return fmt.Errorf("managed worktree destination overlaps protected storage")
		}
	}
	for _, ancestor := range m.cfg.ProtectedAncestors {
		p := resolveForCompare(ancestor)
		if p != "" && within(path, p) {
			return fmt.Errorf("managed worktree destination contains protected ancestor")
		}
	}
	return nil
}

func localGitAdmin(root string) (string, error) {
	dot := filepath.Join(root, ".git")
	st, err := os.Lstat(dot)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return dot, nil
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("invalid Git admin path")
	}
	data, err := os.ReadFile(dot)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if !strings.HasPrefix(text, "gitdir: ") {
		return "", fmt.Errorf("invalid Git admin pointer")
	}
	path := strings.TrimSpace(strings.TrimPrefix(text, "gitdir: "))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return resolveExisting(path), nil
}
func (m *Manager) localScratchDir(path, pattern string) (string, error) {
	if !m.Active() {
		return "", fmt.Errorf("%w: %s", ErrSandboxRequired, m.Status().Reason)
	}
	scope, err := m.CommandScope(path)
	if err != nil {
		return "", err
	}
	root := scope.Root
	if scope.JobCheckout != "" {
		root = scope.JobCheckout
	}
	if scope.ReviewCheckout != "" {
		return "", fmt.Errorf("review checkout cannot create writable Git scratch")
	}
	if _, err := m.localGitMounts(root, scope.Root, true); err != nil {
		return "", err
	}
	admin, err := localGitAdmin(root)
	if err != nil {
		return "", err
	}
	if _, err := m.validateMounts([]string{admin}); err != nil {
		return "", err
	}
	return os.MkdirTemp(admin, pattern)
}
