package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// SandboxEngine reuses the local landing build, isolation and supervised exec paths.
type SandboxEngine struct{ Log func(string, ...any) }

func (*SandboxEngine) Health(ctx context.Context, dir string) (bool, bool, error) {
	out, err := (&provider.OSCommandRunner{}).Run(ctx, "docker", "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return false, false, errors.New("container engine unavailable")
	}
	gvisor := strings.Contains(string(out), `"runsc"`)
	if !gvisor {
		return true, false, errors.New("gVisor runtime unavailable")
	}
	var s unix.Statfs_t
	if err := unix.Statfs(dir, &s); err != nil {
		return true, gvisor, err
	}
	if s.Bavail*uint64(s.Bsize) < 2<<30 {
		return true, gvisor, errors.New("work directory has less than 2 GiB free")
	}
	return true, gvisor, nil
}

type imageResolver struct{ image sandbox.EnvironmentImage }

func (r imageResolver) SandboxEnvironment(string) (sandbox.EnvironmentImage, bool, error) {
	return r.image, true, nil
}

var commitID = regexp.MustCompile(`^[a-fA-F0-9]{40,64}$`)

func (eng *SandboxEngine) Execute(ctx context.Context, l store.RunnerLanding, dir string, emit func([]byte) error) (codes []int, err error) {
	if !commitID.MatchString(l.HeadCommit) || !commitID.MatchString(l.BaseCommit) || l.CheckTimeout <= 0 || len(l.Checks) == 0 {
		return nil, errors.New("invalid landing definition")
	}
	// Credentials stay outside this disposable project mount. Root processes use
	// an unprivileged container identity, never the engine's root identity.
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 65534, 65534
		if err = os.Chown(dir, uid, gid); err != nil {
			return nil, err
		}
		if err = os.Chown(filepath.Join(dir, "source.tar.gz"), uid, gid); err != nil {
			return nil, err
		}
	}
	// Resolver state is outside the only writable project mount and is removed
	// together with the request directory.
	project := filepath.Join(dir, "project")
	if err = os.Mkdir(project, 0700); err != nil {
		return nil, err
	}
	if err = os.Chown(project, uid, gid); err != nil {
		return nil, err
	}
	if err = os.Rename(filepath.Join(dir, "source.tar.gz"), filepath.Join(project, "source.tar.gz")); err != nil {
		return nil, err
	}
	m, err := sandbox.NewManager(ctx, sandbox.Config{Mode: sandbox.ModeRequired, Runtime: "runsc", UID: uid, GID: gid, StateDir: filepath.Join(dir, "state")}, nil)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	if !m.Active() {
		return nil, errors.New(m.Status().Reason)
	}
	def := store.SandboxEnvironmentDefinition{BaseImage: l.BaseImage, Setup: l.Setup, Env: l.Env}
	hash := store.SandboxEnvironmentHash(def)
	buildCtx, buildCancel := context.WithTimeout(ctx, 30*time.Minute)
	image, err := m.BuildEnvironment(buildCtx, sandbox.EnvironmentBuild{Hash: hash, Image: store.SandboxEnvironmentImage(hash), BaseImage: l.BaseImage, Setup: l.Setup, Env: l.Env})
	buildCancel()
	if err != nil {
		return nil, err
	}
	m.SetEnvironmentResolver(imageResolver{sandbox.EnvironmentImage{Image: image.Image, Path: image.Path}})
	landing, err := m.AcquireLanding(ctx, project, project)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, landing.Release()) }()
	checkout := filepath.Join(project, "checkout")
	// All repository parsing happens inside the fresh sandbox, never on the host.
	for _, args := range archiveCommands(project, checkout) {
		result, e := landing.Exec(ctx, sandbox.ExecRequest{Command: args, WorkingDir: project, Timeout: 5 * time.Minute, MaxTimeout: 5 * time.Minute, MaxOutput: 64 << 10})
		if e != nil {
			return nil, e
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("archive preparation exited %d", result.ExitCode)
		}
	}
	checkEnv := landingEnv(l)
	for i, check := range l.Checks {
		var mu sync.Mutex
		var streamErr error
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(l.CheckTimeout)*time.Second)
		result, e := landing.Exec(checkCtx, sandbox.ExecRequest{Command: []string{"bash", "-lc", check}, WorkingDir: checkout, Env: checkEnv, Timeout: time.Duration(l.CheckTimeout) * time.Second, MaxTimeout: time.Duration(l.CheckTimeout) * time.Second, MaxOutput: 2 << 20, OnOutput: func(p []byte) {
			mu.Lock()
			defer mu.Unlock()
			if streamErr == nil {
				streamErr = emit(p)
				if streamErr != nil {
					cancel()
				}
			}
		}})
		timedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
		cancel()
		mu.Lock()
		se := streamErr
		mu.Unlock()
		if se != nil {
			return codes, se
		}
		code := 124
		if !timedOut {
			if e != nil {
				return codes, e
			}
			if result == nil {
				return codes, errors.New("missing check result")
			}
			code = result.ExitCode
		}
		codes = append(codes, code)
		if eng.Log != nil {
			eng.Log("runner check %s index=%d exit=%d", l.ID, i+1, code)
		}
	}
	return codes, nil
}

// archiveCommands run only inside the disposable sandbox. No repository
// history is transported; the single local commit exists for repository-aware checks.
func archiveCommands(project, checkout string) [][]string {
	return [][]string{
		{"mkdir", "--", checkout},
		{"tar", "--extract", "--gzip", "--file", filepath.Join(project, "source.tar.gz"), "--directory", checkout, "--no-same-owner", "--no-same-permissions", "--exclude=.git"},
		{"git", "-C", checkout, "init", "--template="},
		{"git", "-C", checkout, "add", "--force", "--all"},
		{"git", "-C", checkout, "-c", "user.name=Swarm Runner", "-c", "user.email=runner@swarm.local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "Landing archive tree"},
	}
}

func landingEnv(l store.RunnerLanding) map[string]string {
	env := make(map[string]string, len(l.Env)+2)
	for k, v := range l.Env {
		env[k] = v
	}
	env["SWARM_LANDING_HEAD"] = l.HeadCommit
	env["SWARM_LANDING_BASE"] = l.BaseCommit
	return env
}
