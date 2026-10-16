package environment

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Purpose: cloudEnvironment.Exec must consume a complete receipt before cache
// work ends, without masking invalid receipts or check failures. Fake remote
// boundaries are the narrowest layer proving this without a Cloud Build service.
func TestCloudBuildResultFirstLatency(t *testing.T) {
	// Without a receipt, a build failure must still fail the landing.
	e := &cloudEnvironment{provider: CloudBuild{Storage: &checkFake{}, Builds: &latencyBuilds{status: "FAILURE"}}, spec: Spec{Checks: []string{"check"}}}
	if _, err := e.Exec(context.Background(), ExecRequest{Command: []string{"bash", "-lc", "check"}}); err == nil || e.results != nil {
		t.Fatal("missing receipt concealed build failure")
	}
	for _, status := range []string{"WORKING", "FAILURE"} {
		for _, data := range []string{`{"checks":[{"exit_code":7,"output":"failed"}],"phases":{"setup":0.25,"cache_save":null}}`, `{"checks":[]}`, `{"checks":[{"exit_code":0}],"phases":{"setup":-1}}`, `broken`} {
			s := &latencyStorage{data: []byte(data)}
			e := &cloudEnvironment{provider: CloudBuild{Storage: s, Builds: &latencyBuilds{status: status}}, spec: Spec{Checks: []string{"check"}}, started: time.Now(), timeout: time.Minute}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			r, err := e.Exec(ctx, ExecRequest{Command: []string{"bash", "-lc", "check"}})
			cancel()
			if strings.Contains(data, "failed") {
				if err != nil || r.ExitCode != 7 || !strings.Contains(r.Stdout, "setup=0.250s") || !strings.Contains(r.Stdout, "cache_save=pending") {
					t.Fatalf("status=%s result=%+v error=%v", status, r, err)
				}
			} else if err == nil || e.results != nil {
				t.Fatalf("accepted invalid receipt: %s", data)
			}
		}
	}
}

type latencyStorage struct {
	checkFake
	data []byte
}

func (s *latencyStorage) Get(context.Context, string, string) ([]byte, error) { return s.data, nil }

type latencyBuilds struct {
	timeoutBuilds
	status string
}

func (b *latencyBuilds) Get(context.Context, string) (Build, error) {
	return Build{Status: b.status}, nil
}

// Purpose: the generated runner owns phase ordering, background cache work,
// bounded safe archives and quiet Git. Inspect its contract and compile Python;
// this is not a live latency benchmark or proof of image package availability.
func TestCloudBuildRunnerLatency(t *testing.T) {
	s := cloudBuildPython
	for _, want := range []string{"SWARM_PHASE", "phase('setup'", "phase('payload'", "phase('cache_restore'", "phase('cache_save'", "phase('check_'", "cache_thread.start()", "cache_thread.join()", "core.fsmonitor=false", "gc.auto=0", "'commit','-q'", "'-T0'", "cache_manifest()", "cache_before == cache_manifest()"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Index(s, "transfer('result.json',root+'/result.json',True)") > strings.LastIndex(s, "save_cache()") {
		t.Error("cache save delays receipt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", "import sys; compile(sys.stdin.read(), '<runner>', 'exec')")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python: %v %s", err, out)
	}
}

// Purpose: cache helpers must skip unchanged content and preserve changed data
// through the real archive writer/reader; failed publication stays nonfatal.
// Local object fakes isolate save_cache/restore_cache without cloud credentials.
func TestCloudBuildCacheManifestLatency(t *testing.T) {
	start := strings.Index(cloudBuildPython, "# Persistent cache helpers")
	end := strings.Index(cloudBuildPython, "# End persistent cache helpers")
	program := `import os, shutil, tarfile, uuid
root = os.getcwd()
cfg = {'cache_key':'fixture'}
objects = {}
def transfer(name, path, upload=False, object_name=None):
    if upload:
        with open(path,'rb') as f: objects[object_name] = f.read()
    else:
        with open(path,'wb') as f: f.write(objects[object_name])
def cache_publish(source, destination): objects[destination] = objects.pop(source)
` + cloudBuildPython[start:end] + `
cold_cache()
with open(root+'/.cache/go-build/item','w') as f: f.write('before')
cache_before = cache_manifest()
save_cache()
assert not objects
with open(root+'/.cache/go-build/item','w') as f: f.write('changed content')
save_cache()
assert cache_object() in objects
cold_cache()
restore_cache()
assert open(root+'/.cache/go-build/item').read() == 'changed content'
cache_before = cache_manifest()
before = dict(objects)
save_cache()
assert objects == before
with open(root+'/.cache/go-build/item','w') as f: f.write('another update')
def transfer(*args, **kwargs): raise RuntimeError('injected upload failure')
save_cache()
assert objects == before
assert not os.path.exists(root+'/save-cache.tar.gz')
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", program)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cache: %v %s", err, out)
	}
}

// Purpose: needed_setup must use actual image tools, not the build-essential
// metapackage, while preserving missing packages and arbitrary shell semantics.
// Execute the helper with deterministic process/tool probes; no apt runs.
func TestCloudBuildMissingPackagesLatency(t *testing.T) {
	program := `import re, shutil, subprocess
` + cloudBuildSetupPython + `
class Result:
    returncode = 1
    stdout = b''
subprocess.run = lambda *a, **kw: Result()
shutil.which = lambda name: '/bin/'+name if name in ('gcc','g++','make','dpkg','bzip2') else None
assert needed_setup('apt-get update && apt-get install -y build-essential bzip2') is None
assert needed_setup('apt-get update && apt-get install -y build-essential jq') == 'apt-get update && apt-get install -y jq'
assert needed_setup('apt-get install -y gcc=1') == 'apt-get install -y gcc=1'
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "python3", "-c", program).CombinedOutput(); err != nil {
		t.Fatalf("setup: %v %s", err, out)
	}
}
