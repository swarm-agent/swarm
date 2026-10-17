package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: executeChecks must reuse absolute source paths without sharing active
// sandboxes or retaining untrusted files. The lease seam is the narrowest layer
// proving path allocation, cleanup and release ordering without Docker.
func TestEngineLanes(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[fail], func(t *testing.T) {
			root := t.TempDir()
			eng := &SandboxEngine{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var previous string
			var info os.FileInfo
			var prior *parallelLanding
			injected := errors.New("check failed")
			for n := 0; n < 2; n++ {
				dir := laneArchive(t, root)
				_, err := eng.executeChecks(ctx, store.RunnerLanding{Checks: []string{"check"}, CheckTimeout: 1}, dir, os.Getuid(), os.Getgid(), func(_ context.Context, project string) (checkLanding, error) {
					if n == 1 {
						if prior == nil || !prior.released {
							t.Error("previous sandbox not released")
						}
						if _, err := os.Lstat(filepath.Join(project, "stale")); !errors.Is(err, os.ErrNotExist) {
							t.Errorf("stale file remains: %v", err)
						}
						current, err := os.Stat(project)
						if err != nil || !os.SameFile(info, current) {
							t.Error("project directory replaced")
						}
					}
					f := &parallelLanding{run: func(_ context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
						if n == 1 && req.WorkingDir != previous {
							t.Errorf("checkout changed: %s != %s", req.WorkingDir, previous)
						}
						previous = req.WorkingDir
						if err := os.WriteFile(filepath.Join(project, "stale"), []byte("stale"), 0600); err != nil {
							return nil, err
						}
						info, _ = os.Stat(project)
						if fail && n == 0 {
							return nil, injected
						}
						return &provider.ExecResult{}, nil
					}}
					prior = f
					return f, nil
				}, func([]byte) error { return nil })
				if fail && n == 0 {
					if !errors.Is(err, injected) {
						t.Fatalf("error = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func laneArchive(t *testing.T, root string) string {
	t.Helper()
	dir, err := os.MkdirTemp(root, "landing-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.tar.gz"), []byte("verified fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

type laneLanding struct {
	parallelLanding
	release func() error
}

func (l *laneLanding) Release() error { return l.release() }

// Purpose: executeChecks must hold a lane until Release finishes, and must not
// reuse a path whose sandbox removal failed. Channel barriers prove these
// failure and concurrency postconditions without timing-dependent sleeps.
func TestEngineLanesConcurrentRelease(t *testing.T) {
	for _, failRelease := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked-release", true: "failed-release"}[failRelease], func(t *testing.T) {
			root := t.TempDir()
			eng := &SandboxEngine{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan string, 1)
			unblock := make(chan struct{})
			done := make(chan error, 1)
			firstDir, secondDir := laneArchive(t, root), laneArchive(t, root)
			injected := errors.New("release failed")
			go func() {
				_, err := eng.executeChecks(ctx, store.RunnerLanding{Checks: []string{"check"}, CheckTimeout: 1}, firstDir, os.Getuid(), os.Getgid(), func(_ context.Context, project string) (checkLanding, error) {
					return &laneLanding{parallelLanding: parallelLanding{run: func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error) {
						return &provider.ExecResult{}, nil
					}}, release: func() error {
						started <- project
						if failRelease {
							return injected
						}
						select {
						case <-unblock:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}}, nil
				}, func([]byte) error { return nil })
				done <- err
			}()
			var first string
			select {
			case first = <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if failRelease {
				if err := <-done; !errors.Is(err, injected) {
					t.Fatalf("error = %v", err)
				}
			}
			_, err := eng.executeChecks(ctx, store.RunnerLanding{Checks: []string{"check"}, CheckTimeout: 1}, secondDir, os.Getuid(), os.Getgid(), func(_ context.Context, project string) (checkLanding, error) {
				if project == first {
					t.Error("active or unreleased sandbox path reused")
				}
				return &parallelLanding{run: func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error) {
					return &provider.ExecResult{}, nil
				}}, nil
			}, func([]byte) error { return nil })
			close(unblock)
			if !failRelease {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
