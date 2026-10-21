package sandbox

import (
	"context"

	"os/exec"

	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// Requirement: ensure must not admit a new or reused command container without
// its home identity. Inject failure at configureGitIdentity's engine boundary;
// this unit layer proves admission fails closed without needing Docker.
func TestGitIdentityRequiredOnEnsure(t *testing.T) {
	for _, state := range []string{"", "spec true first", "spec false first"} {
		t.Run(state, func(t *testing.T) {
			r := readyRunner()
			r.out["container inspect"] = state
			r.fail["exec -e"] = errors.New("identity unavailable")
			m := newTestManager(t, ModeAuto, r, Config{StateDir: t.TempDir()})
			started, err := m.ensure(context.Background(), "box", t.TempDir(), "spec", nil, SecretSetup{}, EnvironmentImage{})
			if err == nil || started != "" {
				t.Fatalf("admitted without identity: started=%q err=%v", started, err)
			}
		})
	}
}

// Requirement: configureGitIdentity supplies missing sandbox-home components
// without overwriting an existing identity. Execute its actual shell and Git
// against a temporary home; only the engine transport is replaced.
func TestGitIdentityHomeDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("SWARM_GIT_AUTHOR", "")
	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1"}
	r := &identityConfigRunner{env: env, dir: home}
	m := &Manager{runner: r}
	for _, want := range []string{"Swarm", "Existing Author"} {
		if want != "Swarm" {
			cmd := exec.Command("git", "config", "--global", "user.name", want)
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("set identity: %v %s", err, out)
			}
		}
		if err := m.configureGitIdentity(context.Background(), "box"); err != nil {
			t.Fatal(err)
		}
		for key, value := range map[string]string{"user.name": want, "user.email": "swarm@localhost"} {
			cmd := exec.Command("git", "config", "--file", filepath.Join(home, ".gitconfig"), "--get", key)
			cmd.Env = env
			out, err := cmd.Output()
			if err != nil || strings.TrimSpace(string(out)) != value {
				t.Fatalf("%s = %q: %v", key, out, err)
			}
		}
	}
}
