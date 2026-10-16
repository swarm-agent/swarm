package environment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type scriptStorage struct{ objects map[string][]byte }

func (s *scriptStorage) Put(_ context.Context, _, name string, r io.Reader) error {
	b, err := io.ReadAll(r)
	s.objects[name] = b
	return err
}
func (s *scriptStorage) Get(_ context.Context, _, name string) ([]byte, error) {
	return s.objects[name], nil
}
func (s *scriptStorage) DeletePrefix(_ context.Context, _, prefix string) error {
	for name := range s.objects {
		if strings.HasPrefix(name, prefix) {
			delete(s.objects, name)
		}
	}
	return nil
}

type scriptBuilds struct {
	timeoutBuilds
	mu     sync.Mutex
	builds []Build
}

func (b *scriptBuilds) Create(_ context.Context, build Build) (Build, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.builds = append(b.builds, build)
	return Build{ID: fmt.Sprint(len(b.builds))}, nil
}

// Purpose: Acquire must submit bounded arguments regardless of check count/size,
// upload the exact integrity-bound runner, and remove all landing objects on release.
// Real payload creation with captured storage/build calls is the narrowest boundary
// that reproduces the API argument-limit regression without a live cloud service.
func TestCloudBuildUploadedScript(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	checkout := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = checkout
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	storage := &scriptStorage{objects: map[string][]byte{}}
	builds := &scriptBuilds{}
	p := CloudBuild{Store: db, Storage: storage, Builds: builds, Config: CloudBuildConfig{Project: "project", Bucket: "bucket"}}
	checks := make([]string, 100)
	for i := range checks {
		checks[i] = "echo '$HOME $$' #" + strings.Repeat("long command ", 1000)
	}
	spec := Spec{Account: "account", Folder: "folder", Checkout: checkout, Commit: git("rev-parse", "HEAD"), Checks: checks, CheckTimeout: time.Minute, Definition: Definition{BaseImage: "image"}}
	e, err := p.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Release()
	args := builds.builds[0].Steps[0].Args
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("unexpected args: %q", args)
	}
	if len(args[1]) >= 2000 {
		t.Fatalf("step argument too long: %d bytes", len(args[1]))
	}
	prefix := e.(*cloudEnvironment).prefix
	script := storage.objects[prefix+"script.sh"]
	if len(script) == 0 {
		t.Fatal("script.sh not uploaded")
	}
	var request struct {
		SHA256            string   `json:"sha256"`
		ScriptSHA256      string   `json:"script_sha256"`
		CheckScriptSHA256 []string `json:"check_script_sha256"`
		Checks            []string `json:"checks"`
	}
	if err := json.Unmarshal(storage.objects[prefix+"request.json"], &request); err != nil {
		t.Fatal(err)
	}
	if request.ScriptSHA256 != fmt.Sprintf("%x", sha256.Sum256(script)) {
		t.Fatal("script digest mismatch")
	}
	if string(script) != cloudBuildScriptWithCache("bucket", prefix, request.SHA256, p.cacheKey(spec)) {
		t.Fatal("uploaded runner changed")
	}
	if len(request.Checks) != len(checks) || request.Checks[0] != checks[0] {
		t.Fatal("check request changed")
	}
	if len(builds.builds) != len(checks) || len(request.CheckScriptSHA256) != len(checks) {
		t.Fatal("missing check builds or digests")
	}
	seen := make(map[string]bool)
	for _, build := range builds.builds {
		args := build.Steps[0].Args
		if len(args) != 2 || args[0] != "-c" || len(args[1]) >= 2000 {
			t.Fatal("invalid or oversized check bootstrap")
		}
		cfg := speedScriptConfig(t, strings.ReplaceAll(args[1], "$$", "$"))
		childPrefix := cfg["prefix"].(string)
		seen[childPrefix] = true
		childScript := storage.objects[childPrefix+"script.sh"]
		digest := fmt.Sprintf("%x", sha256.Sum256(childScript))
		if len(childScript) == 0 || cfg["sha256"] != digest {
			t.Fatal("check script digest mismatch")
		}
		if strings.ReplaceAll(args[1], "$$", "$") != cloudBuildBootstrap("bucket", childPrefix, digest) {
			t.Fatal("bootstrap escaping changed script")
		}
	}
	for i := range checks {
		childPrefix := fmt.Sprintf("%schecks/%d/", prefix, i)
		childScript := storage.objects[childPrefix+"script.sh"]
		if !seen[childPrefix] || request.CheckScriptSHA256[i] != fmt.Sprintf("%x", sha256.Sum256(childScript)) {
			t.Fatal("missing check script or recorded digest")
		}
		if string(childScript) != cloudBuildCheckScript("bucket", prefix, request.SHA256, p.cacheKey(spec), childPrefix, i, i == 0) {
			t.Fatal("check runner changed")
		}
	}
	if !strings.Contains(string(script), "'runuser','-u','swarmcheck','--preserve-environment','--','bash','-c',command") {
		t.Fatal("runner lost unprivileged checks")
	}
	if strings.Contains(strings.ReplaceAll(args[1], "$$", ""), "$") {
		t.Fatal("unescaped dollar")
	}
	if err := e.Release(); err != nil {
		t.Fatal(err)
	}
	if len(storage.objects) != 0 {
		t.Fatal("landing objects not removed")
	}
}

// Purpose: the actual bootstrap must reject corrupt downloads before executing
// any runner, and preserve literal dollars on successful execution. Running its
// Python body with intercepted metadata/storage transport isolates integrity and
// credential handling without cloud access; bash execution remains real.
func TestCloudBuildBootstrapIntegrity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	harness := `import io, json, hashlib, base64, subprocess, urllib.request, sys, os
body = sys.argv[1]
runner = b"test '$HOME $$' = '$HOME $$' && printf executed > marker\n"
real_run = subprocess.run
urllib.request.urlopen = lambda req, timeout: io.BytesIO(b'{"access_token":"fixture-token"}')
def transfer(args, **kwargs):
    if args[0] != 'curl': return real_run(args, **kwargs)
    assert 'fixture-token' not in ' '.join(args)
    assert kwargs['input'] == b'header = "Authorization: Bearer fixture-token"\n'
    assert args[-1].endswith('/o/landings%2Ftest%2Fscript.sh?alt=media')
    with open(args[args.index('-o')+1], 'wb') as f: f.write(download)
subprocess.run = transfer
for corrupt in (True, False):
    download = runner + (b'corrupt' if corrupt else b'')
    cfg = {'bucket':'bucket', 'prefix':'landings/test/', 'sha256':hashlib.sha256(runner).hexdigest()}
    sys.argv = ['bootstrap', base64.b64encode(json.dumps(cfg).encode()).decode()]
    try:
        exec(body, {})
        assert not corrupt
    except ValueError as err:
        assert corrupt and str(err) == 'script SHA256 mismatch'
    assert os.path.exists('marker') == (not corrupt)
`
	cmd := exec.CommandContext(ctx, "python3", "-c", harness, cloudBuildBootstrapPython)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap integrity: %v\n%s", err, out)
	}
}
