package environment

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"sync"
	"testing"
	"time"
)

type speedBuilds struct {
	mu     sync.Mutex
	builds []Build
	ready  chan struct{}
}

func (b *speedBuilds) Create(ctx context.Context, v Build) (Build, error) {
	b.mu.Lock()
	n := len(b.builds)
	b.builds = append(b.builds, v)
	if len(b.builds) == 2 {
		close(b.ready)
	}
	b.mu.Unlock()
	select {
	case <-b.ready:
		return Build{ID: fmt.Sprint(n)}, nil
	case <-ctx.Done():
		return Build{}, ctx.Err()
	}
}
func (b *speedBuilds) Get(_ context.Context, id string) (Build, error) {
	return Build{ID: id, Status: "SUCCESS"}, nil
}

type speedStorage struct{ checkFake }

func (s *speedStorage) Get(_ context.Context, _, object string) ([]byte, error) {
	if strings.Contains(object, "checks/0/") {
		return []byte(`{"checks":[{"exit_code":1,"output":"FAIL first"}]}`), nil
	}
	return []byte(`{"checks":[{"exit_code":0,"output":"second"}]}`), nil
}
func speedScriptConfig(t *testing.T, script string) map[string]any {
	t.Helper()
	encoded := strings.Split(strings.Split(script, "python3 - '")[1], "'")[0]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Purpose: Acquire must dispatch independent builds over one payload and Exec
// must preserve ordered outputs even after a check failure. A barrier at the
// fake build boundary proves concurrent creation without timing comparisons.
func TestCloudBuildParallelSpeed(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	git := func(args ...string) string {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		o, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s", e, o)
		}
		return strings.TrimSpace(string(o))
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	head := git("rev-parse", "HEAD")
	storage := &speedStorage{checkFake{objects: map[string]bool{}}}
	builds := &speedBuilds{ready: make(chan struct{})}
	p := CloudBuild{Store: db, Storage: storage, Builds: builds, Config: CloudBuildConfig{Project: "project", Bucket: "bucket"}}
	e, err := p.Acquire(ctx, Spec{Account: "account", Folder: "folder", Checkout: dir, Commit: head, Checks: []string{"first", "second"}, CheckTimeout: time.Minute, Definition: Definition{BaseImage: "image"}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Release()
	if len(builds.builds) != 2 {
		t.Fatalf("builds=%d", len(builds.builds))
	}
	payloads := 0
	for object := range storage.objects {
		if strings.HasSuffix(object, "payload.tar.gz") {
			payloads++
		}
	}
	if payloads != 1 {
		t.Fatalf("payload uploads=%d", payloads)
	}
	saves := 0
	prefix := ""
	for _, b := range builds.builds {
		cfg := speedScriptConfig(t, strings.ReplaceAll(b.Steps[0].Args[1], "$$", "$"))
		if prefix != "" && cfg["prefix"] != prefix {
			t.Fatal("builds use different payloads")
		}
		prefix = cfg["prefix"].(string)
		if cfg["cache_save"] == true {
			saves++
		}
		if cfg["cache_key"] == "" {
			t.Fatal("no restore")
		}
	}
	if saves != 1 {
		t.Fatalf("cache writers=%d", saves)
	}
	for i, command := range []string{"first", "second"} {
		r, err := e.Exec(ctx, ExecRequest{Command: []string{"bash", "-lc", command}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"FAIL first", "second"}[i]
		if r.Stdout != want || r.ExitCode != 1-i {
			t.Fatalf("result=%+v", r)
		}
	}
}

// Purpose: generated cache configuration and dollar escaping must retain exact
// shell text, and setup must omit installed packages without dropping missing
// ones or rewriting complex shell commands. Execute the Python setup helper at
// its subprocess boundary; no packages or network services are touched.
func TestCloudBuildScriptSpeed(t *testing.T) {
	disabled := false
	p := CloudBuild{Config: CloudBuildConfig{Cache: &disabled}}
	script := cloudBuildScriptWithCache("bucket", "prefix/", "sha", p.cacheKey(Spec{Folder: "folder"}))
	cfg := speedScriptConfig(t, script)
	if cfg["cache_key"] != "" {
		t.Fatal("cache enabled")
	}
	args := cloudBuildCachedStepArgs("bucket", "prefix/", "sha", "")
	if strings.ReplaceAll(args[1], "$$", "$") != script {
		t.Fatal("dollar round trip")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	program := `import re, shutil, subprocess
` + cloudBuildSetupPython + `
class Result:
    returncode = 0
    stdout = b'Status: install ok installed\n'
seen = []
def run(args, **kwargs):
    seen.append(args)
    r = Result()
    if args[-1] == 'missing': r.returncode = 1
    return r
subprocess.run = run
shutil.which = lambda name: '/usr/bin/' + name
assert needed_setup('apt-get update && apt-get install -y git curl') is None
assert needed_setup('apt-get install -y git missing') == 'apt-get install -y missing'
assert needed_setup('apt-get update && apt-get install -y missing') == 'apt-get update && apt-get install -y missing'
assert needed_setup('apt-get install -y git; echo done') == 'apt-get install -y git; echo done'
assert all(args[:2] == ['dpkg', '-s'] for args in seen)
`
	out, err := exec.CommandContext(ctx, "python3", "-c", program).CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %v: %s", err, out)
	}
}

// Purpose: each infrastructure failure must remain scoped to its check. Exec
// must advance to the next independent build rather than repeat or conceal the
// failed one. The fake storage/build boundaries isolate result mapping.
func TestCloudBuildFailureSpeed(t *testing.T) {
	for _, mode := range []string{"create", "build", "storage"} {
		t.Run(mode, func(t *testing.T) {
			builds := &failureSpeedBuilds{mode: mode}
			storage := &speedStorage{checkFake{objects: map[string]bool{}}}
			p := CloudBuild{Builds: builds, Storage: storage}
			e := &cloudEnvironment{spec: Spec{Checks: []string{"first", "second"}}}
			for i, command := range e.spec.Checks {
				e.children = append(e.children, &cloudEnvironment{provider: p, spec: Spec{Checks: []string{command}}, id: fmt.Sprint(i), prefix: fmt.Sprintf("checks/%d/", i)})
			}
			if mode == "create" {
				e.children[0].createErr = &Failure{"create failed"}
			}
			if mode == "storage" {
				e.children[0].provider.Storage = &checkFake{objects: map[string]bool{}}
			}
			if _, err := e.Exec(context.Background(), ExecRequest{Command: []string{"bash", "-lc", "first"}}); err == nil {
				t.Fatal("lost infrastructure error")
			}
			r, err := e.Exec(context.Background(), ExecRequest{Command: []string{"bash", "-lc", "second"}})
			if err != nil || r.Stdout != "second" {
				t.Fatalf("other result: %+v %v", r, err)
			}
		})
	}
}

type failureSpeedBuilds struct {
	timeoutBuilds
	mode string
}

func (b *failureSpeedBuilds) Get(_ context.Context, id string) (Build, error) {
	if id == "0" && b.mode == "build" {
		return Build{ID: id, Status: "FAILURE", StatusDetail: "setup failed"}, nil
	}
	return Build{ID: id, Status: "SUCCESS"}, nil
}
