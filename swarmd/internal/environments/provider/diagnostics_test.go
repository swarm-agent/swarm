package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Purpose: OSCommandRunner.Run must retain actionable bounded/redacted failure
// stderr without corrupting structured stdout. A short hermetic shell process is
// the narrowest layer that proves the actual os/exec pipe and exit-code boundary;
// no engine, daemon, network, or provider-backed workload is involved.
func TestCommandRunnerDiagnosticSeparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := &OSCommandRunner{MaxOutputBytes: 2048}
	for _, exit := range []string{"0", "7"} {
		out, err := r.Run(ctx, "sh", "-c", `printf '{"version":"5"}'; printf 'cgroup permission denied\npassword=fixture-secret token=fixture-token https://user:fixture-pass@example.invalid/?key=fixture-key\n' >&2; exit "$1"`, "test", exit)
		if !json.Valid(out) || string(out) != `{"version":"5"}` {
			t.Fatalf("stdout corrupted: %q", out)
		}
		if exit == "0" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 7 {
			t.Fatalf("lost exit status: %v", err)
		}
		if !strings.Contains(err.Error(), "cgroup permission denied") {
			t.Fatalf("lost diagnostic: %v", err)
		}
		for _, secret := range []string{"fixture-secret", "fixture-token", "fixture-pass", "fixture-key"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("diagnostic leaks %s", secret)
			}
		}
	}
	_, err := r.Run(ctx, "sh", "-c", `exec 1>&2; printf 'permission denied '; i=0; while [ "$i" -lt 300 ]; do printf 'padding '; i=$((i+1)); done; exit 1`)
	if err == nil || len(err.Error()) > 1050 || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("unbounded diagnostic: %v", err)
	}
}

// Purpose: LocalDockerProvider.Capabilities must never convert failed discovery
// into assumed engine support. The injected command boundary proves both zero
// capability claims on failure and preservation of useful, redacted errors.
func TestLocalDockerCapabilitiesFailClosed(t *testing.T) {
	r := newMockRunner()
	r.handlers["version"] = func([]string) ([]byte, error) {
		return []byte("token=fixture-secret"), errors.New("daemon unreachable")
	}
	p := NewLocalDockerProvider(r)
	caps, err := p.Capabilities(context.Background(), podmanConnectionForTest("local_docker"))
	if err == nil || caps.SupportsDocker || caps.EngineVersion != "" || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}
}
