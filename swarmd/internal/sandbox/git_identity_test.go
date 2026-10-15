package sandbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func isolatedIdentityHost(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("SWARM_GIT_AUTHOR", "")
	return home
}

// Purpose: ensure must initialize only the home volume, once on creation,
// without granting capabilities to agents or making identity/ownership failures
// fatal. Go scratch must also be prepared on creation and reuse before commands
// are admitted. The fake engine is the narrowest layer proving lifecycle and args.
func TestSandboxCreationHome(t *testing.T) {
	for _, failure := range []string{"", "identity", "helper"} {
		t.Run(failure, func(t *testing.T) {
			isolatedIdentityHost(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(old)
			r := readyRunner()
			if failure == "identity" {
				r.fail["exec -e"] = errors.New("identity write denied")
			}
			if failure == "helper" {
				r.fail["run --rm"] = errors.New("helper denied")
			}
			m := newTestManager(t, ModeRequired, r, Config{})
			m.runtime = "runsc"
			m.owner = func(string) (int, bool) { return 4242, true }
			image := EnvironmentImage{Image: "test-env", Path: sandboxPath}
			if err := m.ensure(ctx, "test-box", "/project", "spec", []string{"/project"}, SecretSetup{}, image); err != nil {
				t.Fatal(err)
			}
			want := []string{"run", "--rm", "--user", "0", "--network", "none", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges", "--read-only", "--runtime", "runsc", "--mount", "type=volume,src=test-box-home,dst=/home/sandbox", "--entrypoint", "sh", "test-env", "-c", `chown "$1" "$2" && chmod 0700 "$2"`, "swarm-home", "4242:10001", sandboxHome}
			count := 0
			for _, args := range r.calls {
				if len(args) > 1 && args[0] == "run" && args[1] == "--rm" {
					count++
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("helper args: %q, want %q", args, want)
					}
				}
				if len(args) > 1 && args[0] == "run" && args[1] == "-d" {
					joined := strings.Join(args, " ")
					if !strings.Contains(joined, "--cap-drop ALL") || strings.Contains(joined, "--cap-add") {
						t.Fatalf("agent capabilities: %s", joined)
					}
				}
			}
			if count != 1 {
				t.Fatalf("helper calls = %d, want 1", count)
			}
			if failure != "" && !strings.Contains(logs.String(), "WARN") {
				t.Fatalf("missing warning: %s", logs.String())
			}
			before := len(r.calls)
			for _, running := range []string{"true", "false"} {
				r.out["container inspect"] = "spec " + running
				if err := m.ensure(ctx, "test-box", "/project", "spec", nil, SecretSetup{}, image); err != nil {
					t.Fatal(err)
				}
			}
			prepared := 0
			for _, args := range r.calls[before:] {
				if reflect.DeepEqual(args, append([]string{"exec", "test-box"}, withSandboxUmask("mkdir", "-p", sandboxHome+"/.cache/go-tmp")...)) {
					prepared++
					continue
				}
				if args[0] != "container" && args[0] != "start" {
					t.Fatalf("reinitialized existing sandbox: %q", args)
				}
			}
			if prepared != 2 {
				t.Fatalf("scratch preparation calls = %d, want 2", prepared)
			}
		})
	}
}

// Purpose: ensure skips the privileged home helper when ownership and mode
// already match. Fake engine output proves this decision without host chown.
func TestSandboxCreationHomeAlreadyOwned(t *testing.T) {
	isolatedIdentityHost(t)
	r := readyRunner()
	r.out["exec -u"] = "10001:10001 700\n"
	m := newTestManager(t, ModeRequired, r, Config{})
	m.owner = func(string) (int, bool) { return 10001, true }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.ensure(ctx, "test-box", "/project", "spec", nil, SecretSetup{}, EnvironmentImage{Image: "test-env"}); err != nil {
		t.Fatal(err)
	}
	if r.ran("run --rm") {
		t.Fatal("helper ran for an initialized volume")
	}
}

type identityConfigRunner struct {
	fakeRunner
	env []string
	dir string
}

func (r *identityConfigRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	_, _ = r.record(args)
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		i += 2
	}
	i++ // box name
	cmd := exec.CommandContext(ctx, args[i], args[i+1:]...)
	cmd.Dir, cmd.Env = r.dir, r.env
	return cmd.CombinedOutput()
}

// Purpose: configureGitIdentity must persist a fresh-box fallback only in its
// global config, preserve existing sandbox/host/local identities, and validate
// untrusted SWARM_GIT_AUTHOR. Real Git with isolated homes proves metadata and
// precedence; only engine transport is replaced, not Git behavior.
func TestSandboxGitIdentityCommit(t *testing.T) {
	for _, tc := range []struct{ name, override, hostName, localName, boxName, want string }{
		{name: "default", want: "Swarm <swarm@localhost>"},
		{name: "override", override: "Box Author <box@example.test>", want: "Box Author <box@example.test>"},
		{name: "invalid-control", override: "Bad\nName <bad@example.test>", want: "Swarm <swarm@localhost>"},
		{name: "invalid-bare", override: "bad@example.test", want: "Swarm <swarm@localhost>"},
		{name: "host", hostName: "Host Author", override: "Box Author <box@example.test>", want: "Host Author <host@example.test>"},
		{name: "local", localName: "Local Author", want: "Local Author <local@example.test>"},
		{name: "box", hostName: "Host Author", boxName: "Existing Author", want: "Existing Author <existing@example.test>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := isolatedIdentityHost(t)
			t.Setenv("SWARM_GIT_AUTHOR", tc.override)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if tc.hostName != "" {
				if err := os.WriteFile(filepath.Join(host, ".gitconfig"), []byte("[user]\nname = "+tc.hostName+"\nemail = host@example.test\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			home, repo := t.TempDir(), t.TempDir()
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
			git := func(args ...string) string {
				cmd := exec.CommandContext(ctx, "git", args...)
				cmd.Dir, cmd.Env = repo, env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %q: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q", "-b", "primary")
			if tc.localName != "" {
				git("config", "--local", "user.name", tc.localName)
				git("config", "--local", "user.email", "local@example.test")
			}
			if tc.boxName != "" {
				git("config", "--global", "user.name", tc.boxName)
				git("config", "--global", "user.email", "existing@example.test")
			}
			before := git("config", "--local", "--list")
			r := &identityConfigRunner{env: env, dir: repo}
			m := &Manager{cfg: Config{Engine: "test-engine"}, runner: r}
			if err := m.configureGitIdentity(ctx, "test-box"); err != nil {
				t.Fatal(err)
			}
			git("commit", "--allow-empty", "-qm", "identity test")
			if got := git("show", "-s", "--format=%an <%ae>|%cn <%ce>"); got != tc.want+"|"+tc.want {
				t.Fatalf("identity %q, want %q", got, tc.want)
			}
			if got := git("config", "--local", "--list"); got != before {
				t.Fatalf("local config changed: %s", got)
			}
			config, err := os.ReadFile(filepath.Join(home, ".gitconfig"))
			if err != nil {
				t.Fatal(err)
			}
			if err := m.configureGitIdentity(ctx, "test-box"); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
			if !bytes.Equal(config, after) {
				t.Fatal("existing identity rewritten")
			}
		})
	}
}

// Purpose: warmed RouteGit and Exec must not spend engine calls on identity
// checks, and must apply umask 0007 without interpreting command arguments.
// The manager/fake transport layer proves call counts and argv for both paths,
// including the pre-execution container-generation inspection needed for death detection.
func TestSandboxCommandUmaskAndNoIdentityCalls(t *testing.T) {
	isolatedIdentityHost(t)
	r := readyRunner()
	m := newTestManager(t, ModeRequired, r, Config{Engine: "sh"})
	project := t.TempDir()
	m.owner = func(string) (int, bool) { return m.cfg.UID, true }
	m.SetLayout(Layout{Projects: func() ([]string, error) { return []string{project}, nil }})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Warm(ctx, project); err != nil {
		t.Fatal(err)
	}
	before := len(r.calls)
	cmd := exec.Command("git", "-C", project, "status")
	if err := m.RouteGit(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != before {
		t.Fatalf("RouteGit made %d extra engine calls", len(r.calls)-before)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), `umask 0007 && exec "$@"`) {
		t.Fatalf("Git wrapper: %q", cmd.Args)
	}
	if _, err := m.Exec(ctx, Scope{Root: project}, ExecRequest{WorkingDir: project, Command: []string{"printf", "%s", "literal; argument"}}); err != nil {
		t.Fatal(err)
	}
	// State inspection, supervisor probe, execution, and acknowledgement.
	if len(r.calls)-before != 4 {
		t.Fatalf("Exec made %d calls, want 4", len(r.calls)-before)
	}
	args := r.calls[before+2]
	want := []string{"sh", "-c", `umask 0007 && exec "$@"`, "swarm-exec", "printf", "%s", "literal; argument"}
	if !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatalf("Exec wrapper: %q", args)
	}
}

// Purpose: sandboxUmaskScript must make new files group-writable and deny
// other users while preserving argv. A real shell in a temporary directory is
// the narrowest layer proving the mode inherited by Exec and RouteGit commands.
func TestSandboxUmaskFileMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, "sh", "-c", sandboxUmaskScript, "swarm-exec", "touch", "literal; file")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper: %v: %s", err, out)
	}
	info, err := os.Stat(filepath.Join(dir, "literal; file"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0660 {
		t.Fatalf("created mode %o, want 660", got)
	}
}
