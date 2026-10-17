package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	return eng.executeChecks(ctx, l, dir, uid, gid, func(ctx context.Context, project string) (checkLanding, error) {
		return m.AcquireLandingWithVolumes(ctx, project, project, runnerCacheVolumes(hash, l.Env))
	}, emit)
}

type checkLanding interface {
	Exec(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error)
	Release() error
}

func runnerCacheVolumes(hash string, env map[string]string) []sandbox.NamedVolume {
	var volumes []sandbox.NamedVolume
	for _, key := range []struct{ env, short string }{{"GOCACHE", "go-build"}, {"GOMODCACHE", "go-mod"}, {"npm_config_store_dir", "pnpm"}} {
		if dest := env[key.env]; filepath.IsAbs(dest) {
			volumes = append(volumes, sandbox.NamedVolume{Name: "swarm-runner-cache-" + hash[:16] + "-" + key.short, Dest: dest})
		}
	}
	return volumes
}

func (eng *SandboxEngine) executeChecks(ctx context.Context, l store.RunnerLanding, dir string, uid, gid int, acquire func(context.Context, string) (checkLanding, error), emit func([]byte) error) ([]int, error) {
	ctx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	var outputMu sync.Mutex
	var streamErr error
	output := func(p []byte) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if streamErr == nil {
			streamErr = emit(p)
			if streamErr != nil {
				cancelAll()
			}
		}
	}
	codes := make([]int, len(l.Checks))
	errs := make([]error, len(l.Checks))
	var wg sync.WaitGroup
	for i, check := range l.Checks {
		wg.Add(1)
		go func(i int, check string) {
			defer wg.Done()
			codes[i], errs[i] = eng.executeCheck(ctx, l, dir, uid, gid, i, check, acquire, output)
			if errs[i] != nil {
				cancelAll()
			}
		}(i, check)
	}
	wg.Wait()
	if eng.Log != nil {
		for i, code := range codes {
			if errs[i] == nil {
				eng.Log("runner check %s index=%d exit=%d", l.ID, i+1, code)
			}
		}
	}
	return codes, errors.Join(append([]error{streamErr}, errs...)...)
}

func (eng *SandboxEngine) executeCheck(ctx context.Context, l store.RunnerLanding, dir string, uid, gid, i int, check string, acquire func(context.Context, string) (checkLanding, error), output func([]byte)) (code int, err error) {
	project := filepath.Join(dir, fmt.Sprintf("project-%d", i))
	if err := os.Mkdir(project, 0700); err != nil {
		return 0, err
	}
	if err := os.Chown(project, uid, gid); err != nil {
		return 0, err
	}
	// Each sandbox gets an independent archive file, not a shared writable inode.
	source, err := os.Open(filepath.Join(dir, "source.tar.gz"))
	if err != nil {
		return 0, err
	}
	defer source.Close()
	archive, err := os.OpenFile(filepath.Join(project, "source.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	_, copyErr := io.Copy(archive, source)
	err = errors.Join(copyErr, archive.Chown(uid, gid), archive.Close())
	if err != nil {
		return 0, err
	}
	landing, err := acquire(ctx, project)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, landing.Release()) }()
	checkout := filepath.Join(project, "checkout")
	// All repository parsing happens inside the fresh sandbox, never on the host.
	for _, args := range archiveCommands(project, checkout) {
		result, e := landing.Exec(ctx, sandbox.ExecRequest{Command: args, WorkingDir: project, Timeout: 5 * time.Minute, MaxTimeout: 5 * time.Minute, MaxOutput: 64 << 10})
		if e != nil {
			return 0, e
		}
		if result == nil {
			return 0, errors.New("missing archive preparation result")
		}
		if result.ExitCode != 0 {
			return 0, fmt.Errorf("archive preparation exited %d", result.ExitCode)
		}
	}
	checkEnv := landingEnv(l)
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(l.CheckTimeout)*time.Second)
	result, e := landing.Exec(checkCtx, sandbox.ExecRequest{Command: []string{"bash", "-lc", check}, WorkingDir: checkout, Env: checkEnv, Timeout: time.Duration(l.CheckTimeout) * time.Second, MaxTimeout: time.Duration(l.CheckTimeout) * time.Second, MaxOutput: 2 << 20, OnOutput: output})
	timedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
	cancel()
	code = 124
	if !timedOut {
		if e != nil {
			return 0, e
		}
		if result == nil {
			return 0, errors.New("missing check result")
		}
		code = result.ExitCode
	}
	return code, nil
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
