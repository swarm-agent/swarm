package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Purpose: runArgsImage is the narrow container boundary for project cache
// isolation. Jobs/reviews/custom images must share only their project's cache;
// agent containers must not gain capabilities from initialization helpers.
func TestProjectCacheMounts(t *testing.T) {
	m := newTestManager(t, ModeRequired, readyRunner(), Config{})
	root := "/project"
	for _, image := range []EnvironmentImage{{Image: m.cfg.Image}, {Image: "custom-env"}} {
		for _, scope := range []Scope{{Root: root}, {Root: root, JobCheckout: "/jobs/a"}, {Root: root, JobCheckout: "/jobs/b"}, {Root: root, ReviewCheckout: "/reviews/a"}} {
			args := strings.Join(m.runArgsImage(scopeContainerName(scope), root, "spec", nil, SecretSetup{}, image), " ")
			for _, want := range []string{"type=volume,src=" + ProjectCacheVolume(root) + ",dst=" + projectCachePath, "GOCACHE=" + projectCachePath + "/go-build", "GOMODCACHE=" + projectCachePath + "/go-mod", "npm_config_store_dir=" + projectCachePath + "/pnpm", "GOFLAGS=-buildvcs=false -trimpath"} {
				if !strings.Contains(args, want) {
					t.Fatalf("missing %q: %s", want, args)
				}
			}
			if strings.Contains(args, "--cap-add") {
				t.Fatal("agent capabilities added")
			}
		}
	}
	if ProjectCacheVolume(root) == ProjectCacheVolume("/another-project") {
		t.Fatal("cross-project cache collision")
	}
	if limit, err := m.cacheLimit(context.Background()); err != nil || limit != 10<<30 {
		t.Fatal("incorrect default cap")
	}
}

// Purpose: ensure must fail closed before starting a job if cache preparation
// fails. The fake engine proves lifecycle postconditions without Docker.
func TestProjectCacheInitializationFailure(t *testing.T) {
	r := readyRunner()
	r.fail["run --network"] = errors.New("cache unavailable")
	m := newTestManager(t, ModeRequired, r, Config{})
	_, err := m.ensure(context.Background(), "job", "/project", "spec", nil, SecretSetup{}, EnvironmentImage{Image: m.cfg.Image})
	if err == nil || !strings.Contains(err.Error(), "initialize project cache") {
		t.Fatalf("error: %v", err)
	}
	if r.ran("run -d") {
		t.Fatal("job started after failed initialization")
	}
}

// Purpose: ProjectCacheUsage must report engine errors rather than presenting a
// fabricated zero-size measurement; the runner boundary is the narrowest layer.
func TestProjectCacheUsage(t *testing.T) {
	r := readyRunner()
	r.out["run --network"] = "123\t/opt/swarm-cache\n"
	m := newTestManager(t, ModeRequired, r, Config{ProjectCacheLimit: func(context.Context) (uint64, error) { return 2048, nil }})
	usage, err := m.ProjectCacheUsage(context.Background(), "/project")
	if err != nil || usage.Bytes != 123*1024 || usage.LimitBytes != 2048 {
		t.Fatalf("usage=%+v error=%v", usage, err)
	}
	r.fail["volume inspect"] = errors.New("not found")
	before := len(r.calls)
	if _, err = m.ProjectCacheUsage(context.Background(), "/project"); err == nil {
		t.Fatal("missing volume not reported")
	}
	if len(r.calls) != before+1 {
		t.Fatal("missing volume was created")
	}
}

// Purpose: trimProjectCache must read live limits on every admission and reject
// store failures without invoking cleanup. The runner boundary proves the exact
// current cap reaches the trim command without needing a Docker daemon.
func TestProjectCacheLiveLimit(t *testing.T) {
	r := readyRunner()
	cap := uint64(2048)
	var failure error
	m := newTestManager(t, ModeRequired, r, Config{ProjectCacheLimit: func(context.Context) (uint64, error) { return cap, failure }})
	for _, value := range []uint64{2048, 4096} {
		cap = value
		if err := m.trimProjectCache(context.Background(), "job"); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(strings.Join(r.calls[len(r.calls)-1], " "), fmt.Sprint(value/1024)) {
			t.Fatalf("wrong cap: %v", r.calls)
		}
	}
	before := len(r.calls)
	failure = errors.New("store unavailable")
	if err := m.trimProjectCache(context.Background(), "job"); err == nil {
		t.Fatal("ignored store failure")
	}
	if len(r.calls) != before {
		t.Fatal("trimmed after failed read")
	}
}
