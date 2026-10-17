package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type parallelLanding struct {
	run      func(context.Context, sandbox.ExecRequest) (*provider.ExecResult, error)
	released bool
	prep     int
}

func (f *parallelLanding) Exec(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
	if req.Command[0] != "bash" {
		f.prep++
		return &provider.ExecResult{}, nil
	}
	return f.run(ctx, req)
}
func (f *parallelLanding) Release() error { f.released = true; return nil }

// Purpose: executeChecks must start independent check leases concurrently, retain
// input ordering, serialize raw output and release all leases even on stream
// failure. A barrier at the Exec seam proves overlap without timing assumptions
// or Docker, while exercising real archive preparation and timeout orchestration.
func TestEngineParallel(t *testing.T) {
	for _, mode := range []string{"order", "stream", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source.tar.gz"), []byte("verified archive fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := make(chan struct{}, 2)
			barrier := make(chan struct{})
			go func() {
				for i := 0; i < 2; i++ {
					select {
					case <-started:
					case <-ctx.Done():
						return
					}
				}
				close(barrier)
			}()
			var mu sync.Mutex
			leases := map[string]*parallelLanding{}
			streamFailure := errors.New("stream failed")
			acquire := func(_ context.Context, project string) (checkLanding, error) {
				f := &parallelLanding{run: func(ctx context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
					started <- struct{}{}
					select {
					case <-barrier:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if mode == "timeout" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					req.OnOutput([]byte(req.Command[2]))
					if mode == "stream" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					code, _ := strconv.Atoi(req.Command[2])
					return &provider.ExecResult{ExitCode: code}, nil
				}}
				mu.Lock()
				defer mu.Unlock()
				if leases[project] != nil {
					t.Error("checkout shared")
				}
				leases[project] = f
				return f, nil
			}
			var output []string
			codes, err := (&SandboxEngine{}).executeChecks(ctx, store.RunnerLanding{Checks: []string{"7", "3"}, CheckTimeout: 1}, dir, os.Getuid(), os.Getgid(), acquire, func(p []byte) error {
				output = append(output, string(p))
				if mode == "stream" {
					return streamFailure
				}
				return nil
			})
			if mode == "stream" {
				if !errors.Is(err, streamFailure) {
					t.Fatalf("error = %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := []int{7, 3}
				if mode == "timeout" {
					want = []int{124, 124}
				}
				if !reflect.DeepEqual(codes, want) {
					t.Fatalf("codes %v want %v", codes, want)
				}
			}
			if len(leases) != 2 {
				t.Fatalf("leases: %d", len(leases))
			}
			for project, f := range leases {
				if !f.released || f.prep != len(archiveCommands(project, filepath.Join(project, "checkout"))) {
					t.Fatalf("lease not prepared/released: %+v", f)
				}
				data, err := os.ReadFile(filepath.Join(project, "source.tar.gz"))
				if err != nil || string(data) != "verified archive fixture" {
					t.Fatal("independent archive missing")
				}
			}
			if mode == "order" && len(output) != 2 {
				t.Fatalf("output %v", output)
			}
		})
	}
}

// Purpose: definition-scoped cache names must remain stable across requests and
// differ across image definitions; relative values must not become mounts.
func TestEngineParallelCacheNames(t *testing.T) {
	env := map[string]string{"GOCACHE": "/opt/go/cache", "GOMODCACHE": "/opt/go/mod", "npm_config_store_dir": "relative"}
	got := runnerCacheVolumes("0123456789abcdef0123456789abcdef", env)
	want := []sandbox.NamedVolume{{Name: "swarm-runner-cache-0123456789abcdef-go-build", Dest: "/opt/go/cache"}, {Name: "swarm-runner-cache-0123456789abcdef-go-mod", Dest: "/opt/go/mod"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("volumes %v", got)
	}
	if reflect.DeepEqual(got, runnerCacheVolumes("abcdef0123456789abcdef0123456789", env)) {
		t.Fatal("definition caches collide")
	}
}
