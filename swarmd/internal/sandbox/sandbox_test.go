package sandbox

// Purpose: the sandbox is the boundary between agent-controlled commands and
// the daemon's storage, credentials and socket. These hermetic tests pin the
// decisions that do not need a container engine: the hardening flags every
// sandbox is created with (Manager.RunArgs), the refusal of mounts that
// overlap daemon storage or contain the daemon's home (validateMounts), the
// fail-closed behaviour when sandboxes are required or a project was
// sandboxed before (Manager.RouteGit, NewManager), and that the daemon's
// environment never crosses into a sandbox (sandboxGitEnv). Engine behaviour
// itself is proven by the opt-in Docker suite in sandbox_e2e_test.go.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fail  map[string]error // keyed by first engine argument
	out   map[string]string
}

func (f *fakeRunner) record(args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	key := strings.Join(args[:min(2, len(args))], " ")
	if err := f.fail[key]; err != nil {
		return "", err
	}
	return f.out[key], nil
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	out, err := f.record(args)
	return []byte(out), err
}

func (f *fakeRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.Run(ctx, name, args...)
}

func (f *fakeRunner) RunWithIO(_ context.Context, _ io.Reader, _, _ io.Writer, _ string, args ...string) error {
	_, err := f.record(args)
	return err
}

func (f *fakeRunner) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func readyRunner() *fakeRunner {
	return &fakeRunner{fail: map[string]error{}, out: map[string]string{
		"image inspect": "sha256:img\n",
		"info --format": `{"runc":{}}`,
	}}
}

func newTestManager(t *testing.T, mode Mode, runner *fakeRunner, cfg Config) *Manager {
	t.Helper()
	cfg.Mode = mode
	if cfg.UID == 0 {
		cfg.UID, cfg.GID = 10001, 10001
	}
	m, err := NewManager(context.Background(), cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunArgsCarryEveryHardeningFlag(t *testing.T) {
	m := newTestManager(t, ModeRequired, readyRunner(), Config{})
	args := strings.Join(m.RunArgs("swarm-sandbox-x", "/srv/p", "spec", []string{"/srv/p", "/w/b"}), " ")
	for _, want := range []string{
		"--user 10001:10001", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--memory 4g", "--memory-swap 4g", "--pids-limit 1024", "--network swarm-sandbox",
		"--mount type=bind,src=/srv/p,dst=/srv/p", "--mount type=bind,src=/w/b,dst=/w/b",
		"--env HOME=/home/sandbox", "--dns 1.1.1.1",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("run args missing %q:\n%s", want, args)
		}
	}
	for _, forbidden := range []string{"--privileged", "--cap-add", "docker.sock", "--network host", "--pid host"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("run args contain %q", forbidden)
		}
	}
}

func TestRunArgsPreferGVisor(t *testing.T) {
	runner := readyRunner()
	runner.out["info --format"] = `{"runc":{},"runsc":{"path":"/usr/local/bin/runsc"}}`
	m := newTestManager(t, ModeRequired, runner, Config{})
	if !strings.Contains(strings.Join(m.RunArgs("n", "/p", "s", []string{"/p"}), " "), "--runtime runsc") {
		t.Fatal("gVisor runtime not selected although registered")
	}
	if m.Status().Runtime != "runsc" {
		t.Fatalf("status runtime %q", m.Status().Runtime)
	}
}

func TestManagerRefusesRoot(t *testing.T) {
	if _, err := NewManager(context.Background(), Config{Mode: ModeRequired, UID: 0, GID: 0}, readyRunner()); err == nil && os.Getuid() == 0 {
		t.Fatal("manager accepted running sandboxes as root")
	}
}

func TestAutoModeResolvesOnce(t *testing.T) {
	missing := readyRunner()
	missing.fail["network inspect"] = errors.New("no such network")
	off := newTestManager(t, ModeAuto, missing, Config{})
	if off.Active() || off.Enforced() || off.Status().Mode != ModeOff || !strings.Contains(off.Status().Reason, "network") {
		t.Fatalf("auto without a network must resolve to off with a reason: %+v", off.Status())
	}
	on := newTestManager(t, ModeAuto, readyRunner(), Config{})
	if !on.Active() || !on.Enforced() {
		t.Fatalf("auto with everything ready must enforce sandboxes: %+v", on.Status())
	}
	req := newTestManager(t, ModeRequired, missing, Config{})
	if req.Active() || !req.Enforced() {
		t.Fatalf("required without a network must stay enforced and inactive: %+v", req.Status())
	}
}

func TestValidateMountsRefusesProtectedStorage(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	data := filepath.Join(base, "var", "lib", "swarmd")
	home := filepath.Join(base, "home", "swarm")
	project := filepath.Join(home, "projects", "app")
	link := filepath.Join(base, "link-to-data")
	for _, d := range []string{data, project} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, ModeRequired, readyRunner(), Config{ProtectedRoots: []string{data}, ProtectedAncestors: []string{home}})
	for _, bad := range []string{data, filepath.Join(base, "var"), link, home, filepath.Join(base, "home"), "/"} {
		if _, err := m.validateMounts([]string{bad}); err == nil {
			t.Errorf("mount %s was accepted", bad)
		}
	}
	if err := os.MkdirAll(filepath.Join(data, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.validateMounts([]string{filepath.Join(data, "inside")}); err == nil {
		t.Error("mount inside protected storage was accepted")
	}
	if got, err := m.validateMounts([]string{project}); err != nil || len(got) != 1 || got[0] != project {
		t.Errorf("project inside home refused: %v %v", got, err)
	}
}

func TestRouteGitFailsClosed(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "p")
	other := filepath.Join(base, "elsewhere")
	for _, d := range []string{project, other} {
		_ = os.MkdirAll(d, 0o700)
	}
	layout := Layout{Projects: func() ([]string, error) { return []string{project}, nil }}

	broken := readyRunner()
	broken.fail["version --format"] = errors.New("engine down")
	required := newTestManager(t, ModeRequired, broken, Config{})
	required.SetLayout(layout)
	cmd := exec.Command("git", "-C", project, "status")
	before := strings.Join(cmd.Args, " ")
	if err := required.RouteGit(context.Background(), cmd); err == nil {
		t.Fatal("git on a project ran without a required sandbox")
	}
	if strings.Join(cmd.Args, " ") != before {
		t.Fatal("refused command was rewritten")
	}

	// A process without sandboxes must still refuse Git on a project that
	// ran sandboxed before: it may hold agent-planted configuration.
	state := filepath.Join(base, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, usedFile), []byte(`["`+project+`"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	off := newTestManager(t, ModeOff, readyRunner(), Config{StateDir: state})
	off.SetLayout(layout)
	if err := off.RouteGit(context.Background(), exec.Command("git", "-C", project, "status")); !errors.Is(err, ErrSandboxRequired) {
		t.Fatalf("expected ErrSandboxRequired, got %v", err)
	}
	// Paths agents cannot write stay on the host.
	plain := exec.Command("git", "-C", other, "status")
	if err := off.RouteGit(context.Background(), plain); err != nil || filepath.Base(plain.Path) != "git" {
		t.Fatalf("unrelated git was changed: %v %v", plain.Args, err)
	}
}

func TestRouteGitRewritesIntoProjectSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "p")
	sub := filepath.Join(project, "sub")
	_ = os.MkdirAll(sub, 0o700)
	runner := readyRunner()
	m := newTestManager(t, ModeRequired, runner, Config{})
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{project}, nil }})

	t.Setenv("SWARM_TEST_SECRET_TOKEN", "must-not-cross")
	cmd := exec.Command("git", "--no-optional-locks", "-C", "sub", "status", "--porcelain")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_SSH_COMMAND=ssh -i /host/key", "GIT_INDEX_FILE="+filepath.Join(project, ".git", "tmp-index"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.RouteGit(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args, " ")
	if filepath.Base(cmd.Path) != "docker" || !strings.Contains(args, "exec -w "+sub+" ") || !strings.Contains(args, ContainerName(project)+" timeout --kill-after=2 ") ||
		!strings.HasSuffix(args, "git --no-optional-locks -C sub status --porcelain") {
		t.Fatalf("unexpected rewrite: %s", args)
	}
	if !strings.Contains(args, "-e GIT_OPTIONAL_LOCKS=0") || strings.Contains(args, "GIT_SSH_COMMAND") || strings.Contains(args, "must-not-cross") {
		t.Fatalf("environment not filtered: %s", args)
	}
	for _, kv := range cmd.Env {
		if strings.Contains(kv, "must-not-cross") {
			t.Fatalf("engine CLI got daemon secrets: %v", cmd.Env)
		}
	}
	if !runner.ran("run -d --init") {
		t.Fatal("sandbox was not created")
	}
}

func TestScopeForWorktreeNeedsKnownProject(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "p")
	worktrees := filepath.Join(base, "wt")
	lane := filepath.Join(worktrees, "bucket-p", "lane")
	stray := filepath.Join(worktrees, "unknown", "lane")
	for _, d := range []string{project, lane, stray} {
		_ = os.MkdirAll(d, 0o700)
	}
	m := newTestManager(t, ModeRequired, readyRunner(), Config{})
	m.SetLayout(Layout{
		Projects:      func() ([]string, error) { return []string{project}, nil },
		WorktreesRoot: worktrees,
		Bucket:        func(string) (string, error) { return "bucket-p", nil },
	})
	scope, ok, err := m.ScopeFor(lane)
	if err != nil || !ok || scope.Root != project || len(scope.Mounts) != 2 || scope.Mounts[1] != filepath.Join(worktrees, "bucket-p") {
		t.Fatalf("lane scope: %+v %v %v", scope, ok, err)
	}
	if _, _, err := m.ScopeFor(stray); !errors.Is(err, errUnknownWorktree) {
		t.Fatalf("worktree of an unknown project must be refused, got %v", err)
	}
}
