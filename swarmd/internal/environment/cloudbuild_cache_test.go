package environment

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Purpose: exercise the actual generated cache helpers with local archives and a
// fake object transport. Missing/corrupt/traversing archives must leave a cold
// cache, and save must publish via a temporary object without affecting checks.
// This narrow script boundary requires neither credentials nor Cloud Build.
func TestCloudBuildPersistentCache(t *testing.T) {
	script := cloudBuildScript("bucket", "landing/", "sha")
	start := strings.Index(script, "# Persistent cache helpers")
	end := strings.Index(script, "# End persistent cache helpers")
	if start < 0 || end < start {
		t.Fatal("persistent cache helpers absent")
	}
	body := script[start:end]
	prelude := `import os, io, tarfile, shutil, tempfile, urllib.parse, urllib.request, json, uuid
root = os.getcwd()
cfg = {'bucket':'bucket', 'prefix':'landings/test/', 'cache_key':'folder'}
objects = {}
calls = []
def transfer(name, path, upload=False, object_name=None):
    calls.append((object_name, upload))
    if upload:
        with open(path,'rb') as f: objects[object_name] = f.read()
    else:
        if object_name not in objects: raise FileNotFoundError(object_name)
        with open(path,'wb') as f: f.write(objects[object_name])
def cache_publish(source, destination):
    calls.append(('publish', source, destination))
    objects[destination] = objects.pop(source)
`
	assertions := `
restore_cache()
assert not os.path.exists(root+'/.cache/go-build/item')
os.makedirs(root+'/.cache/go-build', exist_ok=True)
os.makedirs(root+'/go/pkg/mod', exist_ok=True)
os.makedirs(root+'/.cache/pnpm', exist_ok=True)
with open(root+'/.cache/go-build/item','w') as f: f.write('test result')
save_cache()
assert calls[-1][0] == 'publish'
assert calls[-1][1].startswith('cache/folder/tmp-')
assert calls[-1][2] == 'cache/folder/latest.tar.gz'
shutil.rmtree(root+'/.cache'); shutil.rmtree(root+'/go')
restore_cache()
assert open(root+'/.cache/go-build/item').read() == 'test result'
objects['cache/folder/latest.tar.gz'] = b'broken'
restore_cache()
assert not os.path.exists(root+'/.cache/go-build/item')
buf = io.BytesIO()
with tarfile.open(fileobj=buf,mode='w:gz') as archive:
    member = tarfile.TarInfo('../escaped'); member.size = 4
    archive.addfile(member, io.BytesIO(b'evil'))
objects['cache/folder/latest.tar.gz'] = buf.getvalue()
restore_cache()
assert not os.path.exists(root+'/escaped')
# Oversize caches must not publish.
CACHE_LIMIT = 1
before = len(calls)
save_cache()
assert len(calls) == before
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", prelude+body+assertions)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cache script: %v\n%s", err, out)
	}
}

// Purpose: Cloud Build substitution escaping must retain the exact script,
// including cache code, without consuming shell dollars.
func TestCloudBuildCacheDollarRoundTrip(t *testing.T) {
	script := cloudBuildScriptWithCache("bucket", "landing/", "sha", "folder")
	args := cloudBuildCachedStepArgs("bucket", "landing/", "sha", "folder")
	if len(args) != 2 || strings.ReplaceAll(args[1], "$$", "$") != script {
		t.Fatal("substitution changed script")
	}
}

// Purpose: the provider cache namespace must be stable and account/folder scoped,
// and explicit opt-out must suppress remote cache use. Direct configuration
// assertions isolate this policy without creating remote resources.
func TestCloudBuildCacheNamespaceAndOptOut(t *testing.T) {
	p := CloudBuild{Config: CloudBuildConfig{Project: "project"}}
	spec := Spec{Account: "account", Folder: "folder"}
	key := p.cacheKey(spec)
	if len(key) != 64 || key != p.cacheKey(spec) {
		t.Fatal("unstable cache key")
	}
	other := spec
	other.Account = "other"
	if key == p.cacheKey(other) {
		t.Fatal("cross-account namespace")
	}
	other = spec
	other.Folder = "other"
	if key == p.cacheKey(other) {
		t.Fatal("cross-folder namespace")
	}
	disabled := false
	p.Config.Cache = &disabled
	if p.cacheKey(spec) != "" {
		t.Fatal("cache opt-out ignored")
	}
}
