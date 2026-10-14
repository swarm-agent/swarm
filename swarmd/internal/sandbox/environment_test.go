package sandbox

// Purpose: a folder may define its own sandbox environment (environment.go).
// Requirement: the environment changes only the image a folder's sandbox runs
// on and that image's PATH, never the hardening; a changed environment
// replaces the folder's container; a resolver failure fails closed instead of
// silently using another image; and a build runs its setup commands (as root)
// only under gVisor, with no host folder mounted, checks bash and git as an
// unprivileged user, and always removes its build container. Threats: an
// environment that weakens the sandbox, a build that runs root commands
// outside gVisor or with host folders, and a stale container kept after the
// environment changed. Owners: Manager.sandboxImage, runArgsImage,
// specHashImage and BuildEnvironment. Hermetic over a scripted command
// runner; the engine itself is covered by the opt-in Docker suite.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

type fakeResolver struct {
	root string
	img  EnvironmentImage
	err  error
}

func (f fakeResolver) SandboxEnvironment(root string) (EnvironmentImage, bool, error) {
	if f.err != nil {
		return EnvironmentImage{}, false, f.err
	}
	if root == f.root {
		return f.img, true, nil
	}
	return EnvironmentImage{}, false, nil
}

func TestEnvironmentChangesOnlyImageAndPath(t *testing.T) {
	m := newTestManager(t, ModeRequired, readyRunner(), Config{})
	env := EnvironmentImage{Image: "swarm-env:abc", Path: "/usr/local/go/bin:/usr/bin:/bin"}
	m.SetEnvironmentResolver(fakeResolver{root: "/srv/p", img: env})

	img, id, err := m.sandboxImage("/srv/p")
	if err != nil || img != env || id != "env2:swarm-env:abc" {
		t.Fatalf("folder image = %+v %q %v", img, id, err)
	}
	other, otherID, err := m.sandboxImage("/srv/q")
	if err != nil || other.Image != DefaultImage || other.Path != sandboxPath || otherID == id {
		t.Fatalf("folder without an environment = %+v %q %v", other, otherID, err)
	}

	args := strings.Join(m.runArgsImage("n", "/srv/p", "s", []string{"/srv/p"}, SecretSetup{}, img), " ")
	for _, want := range []string{
		"--user 10001:10001", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--memory 4g", "--pids-limit 1024", "--network swarm-sandbox", "--env HOME=/home/sandbox",
		"--env PATH=/usr/local/go/bin:/usr/bin:/bin",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("environment run args missing %q:\n%s", want, args)
		}
	}
	if !strings.HasSuffix(args, " --entrypoint sleep swarm-env:abc infinity") {
		t.Errorf("environment image not used: %s", args)
	}
	for _, forbidden := range []string{"--privileged", "--cap-add", "docker.sock", "--network host", DefaultImage} {
		if strings.Contains(args, forbidden) {
			t.Errorf("environment run args contain %q", forbidden)
		}
	}
	if m.specHashImage("/srv/p", []string{"/srv/p"}, "", id) == m.specHashImage("/srv/p", []string{"/srv/p"}, "", otherID) {
		t.Fatal("changing the environment must change the spec, so the container is replaced")
	}

	m.SetEnvironmentResolver(fakeResolver{err: errors.New("store down")})
	if _, _, err := m.sandboxImage("/srv/p"); err == nil {
		t.Fatal("a resolver failure must fail closed, not fall back to another image")
	}
}

// scriptedRunner answers by the full joined command line.
type scriptedRunner struct {
	mu     sync.Mutex
	calls  []string
	answer func(cmd string) (string, error)
}

func (s *scriptedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	s.mu.Lock()
	s.calls = append(s.calls, cmd)
	s.mu.Unlock()
	out, err := s.answer(cmd)
	return []byte(out), err
}

func (s *scriptedRunner) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return s.Run(ctx, name, args...)
}

func (s *scriptedRunner) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, name string, args ...string) error {
	_, err := s.Run(ctx, name, args...)
	return err
}

func (s *scriptedRunner) find(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func buildManager(t *testing.T, runtimes string, answer func(cmd string) (string, error)) (*Manager, *scriptedRunner) {
	t.Helper()
	runner := &scriptedRunner{answer: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "info --format"):
			return runtimes, nil
		case cmd == "image inspect --format {{.Id}} "+DefaultImage:
			return "sha256:img\n", nil
		}
		return answer(cmd)
	}}
	m, err := NewManager(context.Background(), Config{Mode: ModeRequired, UID: 10001, GID: 10001}, runner)
	if err != nil {
		t.Fatal(err)
	}
	return m, runner
}

const gvisorRuntimes = `{"runc":{},"runsc":{"path":"/usr/local/bin/runsc"},"runsc-build":{"path":"/usr/local/bin/runsc"}}`

var testBuild = EnvironmentBuild{Hash: "0123456789abcdef0123", Image: "swarm-env:0123456789abcdef0123", BaseImage: "golang:1.24", Setup: []string{"apt-get install -y jq"}, Env: map[string]string{"GOFLAGS": "-mod=mod"}}

func TestBuildEnvironmentRefusedWithoutGVisor(t *testing.T) {
	m, runner := buildManager(t, `{"runc":{}}`, func(string) (string, error) { return "", nil })
	if _, err := m.BuildEnvironment(context.Background(), testBuild); !errors.Is(err, ErrEnvironmentBuildUnsafe) {
		t.Fatalf("build without gVisor: %v", err)
	}
	if len(runner.find("run ")) != 0 || len(runner.find("exec ")) != 0 {
		t.Fatal("a refused build must not start a container or run a command")
	}
}

func TestBuildEnvironmentRunsSetupUnderGVisorWithoutHostFolders(t *testing.T) {
	m, runner := buildManager(t, gvisorRuntimes, func(string) (string, error) { return "", nil })
	committed := false
	runner.answer = func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "info --format"):
			return gvisorRuntimes, nil
		case strings.HasPrefix(cmd, "image inspect --format {{json .Config.Env}} "+testBuild.Image):
			if !committed {
				return "", errors.New("no such image")
			}
			return `["PATH=/usr/local/go/bin:/usr/bin:/bin","GOFLAGS=-mod=mod"]`, nil
		case strings.HasPrefix(cmd, "commit "):
			committed = true
		}
		return "", nil
	}
	result, err := m.BuildEnvironment(context.Background(), testBuild)
	if err != nil {
		t.Fatalf("build: %v (log %s)", err, result.Log)
	}
	if result.Image != testBuild.Image || result.Path != "/usr/local/go/bin:/usr/bin:/bin" || result.Reused {
		t.Fatalf("result %+v", result)
	}
	runs := runner.find("run -d --name swarm-envbuild-")
	if len(runs) != 1 {
		t.Fatalf("build container runs: %v", runs)
	}
	for _, want := range []string{"--runtime runsc-build ", "--user 0:0", "--security-opt no-new-privileges", "--network swarm-sandbox", "--env GOFLAGS=-mod=mod", "--entrypoint sleep golang:1.24 infinity"} {
		if !strings.Contains(runs[0], want) {
			t.Errorf("build container missing %q: %s", want, runs[0])
		}
	}
	for _, forbidden := range []string{"--privileged", "--cap-add", "docker.sock", "--network host", "src=/srv", "src=/home"} {
		if strings.Contains(runs[0], forbidden) {
			t.Errorf("build container has %q: %s", forbidden, runs[0])
		}
	}
	// The setup command, then the PATH kept for login shells (agents'
	// commands run in bash -lc, and /etc/profile resets PATH).
	if setup := runner.find("exec -u 0 -w / swarm-envbuild-"); len(setup) != 2 || !strings.HasSuffix(setup[0], "/bin/sh -c apt-get install -y jq") || !strings.Contains(setup[1], "/etc/profile.d/") {
		t.Fatalf("setup commands: %v", setup)
	}
	if check := runner.find("exec -u 10001:10001"); len(check) != 1 || !strings.Contains(check[0], "bash -lc git --version") {
		t.Fatalf("bash and git check as the sandbox user: %v", check)
	}
	if commits := runner.find("commit "); len(commits) != 1 || !strings.Contains(commits[0], "ENTRYPOINT []") || !strings.HasSuffix(commits[0], testBuild.Image) {
		t.Fatalf("commit: %v", commits)
	}
	if len(runner.find("rm -f swarm-envbuild-")) < 2 {
		t.Fatal("the build container must be removed before and after the build")
	}

	// The saved image was checked in a new container (gVisor, no network,
	// the sandbox user) for what the build wrote.
	if checks := runner.find("run --rm --runtime runsc --network none --user 10001:10001 --entrypoint test " + testBuild.Image + " -r /etc/profile.d/"); len(checks) != 1 {
		t.Fatalf("saved image check: %v", runner.find("run --rm"))
	}

	// The same definition again reuses its (checked) image and builds nothing.
	before := len(runner.find("run -d"))
	again, err := m.BuildEnvironment(context.Background(), testBuild)
	if err != nil || !again.Reused || len(runner.find("run -d")) != before {
		t.Fatalf("rebuild of an existing definition: %+v %v", again, err)
	}
}

func TestBuildEnvironmentFailedSetupSavesNothing(t *testing.T) {
	m, runner := buildManager(t, gvisorRuntimes, func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "image inspect --format {{json .Config.Env}}"):
			return "", errors.New("no such image")
		case strings.HasPrefix(cmd, "exec -u 0"):
			return "E: Unable to locate package jq\n", errors.New("exit status 100")
		}
		return "", nil
	})
	result, err := m.BuildEnvironment(context.Background(), testBuild)
	if err == nil || !strings.Contains(err.Error(), "setup command 1 failed") || !strings.Contains(result.Log, "Unable to locate package jq") {
		t.Fatalf("failed setup: %v (log %q)", err, result.Log)
	}
	if len(runner.find("commit ")) != 0 {
		t.Fatal("a failed build must not save an image")
	}
	if len(runner.find("rm -f swarm-envbuild-")) < 2 {
		t.Fatal("a failed build must still remove its container")
	}
}

// Requirement: a build needs the runsc-build runtime (gVisor without its
// in-sandbox root overlay), or docker commit saves none of what the setup
// wrote; and an image that does not hold the build's writes is never used.
// Threat: an environment silently missing its setup (observed: gVisor's
// default overlay kept every write inside the sandbox).
func TestBuildEnvironmentNeedsBuildRuntimeAndCompleteImage(t *testing.T) {
	m, runner := buildManager(t, `{"runc":{},"runsc":{"path":"/usr/local/bin/runsc"}}`, func(string) (string, error) { return "", nil })
	if _, err := m.BuildEnvironment(context.Background(), testBuild); !errors.Is(err, ErrEnvironmentBuildRuntimeMissing) {
		t.Fatalf("build without runsc-build: %v", err)
	}
	if len(runner.find("run ")) != 0 || len(runner.find("exec ")) != 0 {
		t.Fatal("a refused build must not start a container or run a command")
	}

	m, runner = buildManager(t, gvisorRuntimes, func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "image inspect --format {{json .Config.Env}}"):
			return "", errors.New("no such image")
		case strings.HasPrefix(cmd, "run --rm"):
			return "", errors.New("exit status 1")
		}
		return "", nil
	})
	if _, err := m.BuildEnvironment(context.Background(), testBuild); err == nil || !strings.Contains(err.Error(), "does not contain what the build wrote") {
		t.Fatalf("incomplete saved image accepted: %v", err)
	}
	if len(runner.find("image rm "+testBuild.Image)) != 1 {
		t.Fatal("an incomplete saved image must be removed")
	}
}

// Purpose: BuildEnvironment installs root-owned tools, caches and login profiles
// for unrelated unprivileged UIDs. Its root setup/profile execs must explicitly
// use 022, not the worker-only 0007 mask that hides these files from those UIDs.
// This hermetic runner test is the narrowest layer proving the actual build
// exec arguments; the unprivileged validation must retain the worker mask.
func TestBuildEnvironmentUsesPublicImageUmask(t *testing.T) {
	m, runner := buildManager(t, gvisorRuntimes, func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "image inspect --format {{json .Config.Env}} "+testBuild.Image) {
			return "", errors.New("no such image")
		}
		return "", nil
	})
	// Stop after validation so no saved-image fixture can hide a skipped build.
	stop := errors.New("validation reached")
	answer := runner.answer
	runner.answer = func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "exec -u 10001:10001") {
			return "", stop
		}
		return answer(cmd)
	}
	_, err := m.BuildEnvironment(context.Background(), testBuild)
	if !errors.Is(err, stop) {
		t.Fatalf("build did not reach validation: %v", err)
	}
	setup := runner.find("exec -u 0 -w / swarm-envbuild-")
	if len(setup) != 2 {
		t.Fatalf("expected setup and profile execs: %v", setup)
	}
	for i, cmd := range setup {
		if !strings.Contains(cmd, `sh -c umask 022 && exec "$@" swarm-image-build /bin/sh -c`) || strings.Contains(cmd, "umask 0007") {
			t.Errorf("root image exec %d must use 022, not 0007: %s", i, cmd)
		}
	}
	check := runner.find("exec -u 10001:10001")
	if len(check) != 1 || !strings.Contains(check[0], `umask 0007 && exec "$@"`) {
		t.Fatalf("unprivileged check mask: %v", check)
	}
	if len(runner.find("commit ")) != 0 {
		t.Fatal("failed validation must not commit an image")
	}
}
