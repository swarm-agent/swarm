package environment

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Purpose: cloudBuildScript must preserve image tools across login shells and
// record failing checks without aborting. Execute its setup/check body with real
// shells in a temporary workspace; only remote transfers are replaced locally.
func TestCloudBuildScriptLoginPathAndCheckFailure(t *testing.T) {
	script := cloudBuildScript("bucket", "prefix/", "sha")
	for _, want := range []string{`IMAGE_PATH="$PATH"`, "export IMAGE_PATH", "export GOPATH=/workspace/go", "export GOCACHE=/workspace/.cache/go-build", "export GOMODCACHE=/workspace/go/pkg/mod"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %s", want)
		}
	}
	root := t.TempDir()
	tool := filepath.Join(root, "image-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	body := cloudBuildPython[strings.Index(cloudBuildPython, "env = os.environ.copy()"):]
	prelude := `import collections, json, os, re, subprocess, time
root = os.getcwd()
def transfer(*args): pass
request = {'definition': {'Setup': ['image-tool']}, 'timeout': 5, 'head': 'head', 'base': 'base', 'checks': ['image-tool; exit 7', 'image-tool']}
`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\npython3 - <<'SWARM_TEST_PY'\n"+prelude+body+"\nSWARM_TEST_PY\n")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "IMAGE_PATH="+root, "PATH="+root+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"exit_code": 7`) || !strings.Contains(string(data), `"exit_code": 0`) {
		t.Fatalf("lost check results: %s", data)
	}
}

// Purpose: cloudEnvironment.Exec must retain bounded build diagnostics when no
// result exists. A fake HTTP boundary exercises the real Builds client and GCS
// object lookup, including unreadable-log fallback, without cloud credentials.
func TestCloudBuildFailureLogTail(t *testing.T) {
	for _, readable := range []bool{true, false} {
		t.Run(map[bool]string{true: "log", false: "status_detail"}[readable], func(t *testing.T) {
			fetched := false
			rest := GoogleREST{Client: &http.Client{Transport: cloudTransport(func(r *http.Request) (*http.Response, error) {
				body := `{"id":"test-build","status":"FAILURE","logsBucket":"gs://logs-bucket","statusDetail":"setup exited 127"}`
				code := 200
				if r.URL.Host == "storage.googleapis.com" {
					fetched = true
					if !strings.Contains(r.URL.Path, "log-test-build.txt") {
						t.Errorf("wrong log object: %s", r.URL)
					}
					if r.Header.Get("Range") != "bytes=-8192" {
						t.Error("unbounded log request")
					}
					body = strings.Repeat("old line\n", 45) + "go: command not found\n"
					if !readable {
						code = 403
						body = "denied"
					}
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			e := &cloudEnvironment{provider: CloudBuild{Builds: rest}, id: "test-build", spec: Spec{Checks: []string{"go version"}}}
			_, err := e.Exec(context.Background(), ExecRequest{Command: []string{"bash", "-lc", "go version"}})
			want := "setup exited 127"
			if readable {
				want = "go: command not found"
			}
			if !fetched || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("fetched=%v error=%v", fetched, err)
			}
			if len(err.Error()) > 8192 || strings.Count(err.Error(), "old line") > 39 {
				t.Fatalf("unbounded reason: %v", err)
			}
		})
	}
}

// Purpose: bound the diagnostic persisted by cloudEnvironment.Exec even when
// statusDetail or a single log line is oversized; direct helper assertions are
// the narrowest layer for byte/line limits and preservation of the final error.
func TestCloudBuildDiagnosticBounds(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 20000) + "final error", strings.Repeat("line\n", 100) + "final error"} {
		got := boundedBuildTail(input, 8000)
		if len(got) > 8000 || strings.Count(got, "\n") > 39 || !strings.HasSuffix(got, "final error") {
			t.Fatalf("invalid diagnostic bounds: bytes=%d", len(got))
		}
	}
}
