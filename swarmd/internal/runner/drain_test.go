package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Purpose: cancellation from the runner signal context must stop admission, not
// cancel a held lease. The fake hub and Engine boundary prove successful result
// delivery after SIGTERM in an isolated test subprocess, without requiring Docker.
func TestRunnerDrain(t *testing.T) {
	if os.Getenv("SWARM_TEST_RUNNER_DRAIN") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerDrain$", "-test.timeout=8s")
		cmd.Env = append(os.Environ(), "SWARM_TEST_RUNNER_DRAIN=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("signal subprocess: %v\n%s", err, out)
		}
		return
	}
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
			defer cancel()
			ready := make(chan struct{})
			close(ready)
			result := make(chan update, 1)
			claims := 0
			stateFile := filepath.Join(t.TempDir(), "state")
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/status"):
					if !busy {
						if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
							t.Error(err)
						}
						<-ctx.Done()
					}
				case strings.HasSuffix(req.URL.Path, "/claim"):
					claims++
					json.NewEncoder(w).Encode(map[string]any{"landing": map[string]any{"id": "drain-landing", "lease_id": "lease", "checks": []string{"true"}, "check_timeout_seconds": 1, "payload_format": "archive-v1", "archive_bytes": 7, "archive_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("archive")))}})
				case strings.HasSuffix(req.URL.Path, "/payload"):
					state, err := os.ReadFile(stateFile)
					fields := strings.Fields(string(state))
					if err != nil || len(fields) != 3 || fields[0] != "busy" || fields[1] != "drain-landing" {
						t.Errorf("busy state: %q %v", state, err)
					} else if _, err := time.Parse(time.RFC3339, fields[2]); err != nil {
						t.Error(err)
					}
					if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
						t.Error(err)
					}
					<-ctx.Done()
					fmt.Fprint(w, "archive")
				case strings.HasSuffix(req.URL.Path, "/result"):
					var u update
					json.NewDecoder(req.Body).Decode(&u)
					result <- u
				}
			}))
			defer hub.Close()
			r := Runner{StateFile: stateFile, Hub: hub.URL, Credential: "test", WorkDir: t.TempDir(), Engine: &fakeEngine{heartbeat: ready}}
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown failed: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown blocked")
			}
			state, err := os.ReadFile(stateFile)
			if err != nil || string(state) != "idle\n" {
				t.Fatalf("final state: %q %v", state, err)
			}
			if busy {
				select {
				case u := <-result:
					if len(u.ExitCodes) != 1 || u.ExitCodes[0] != 0 || u.State != "" {
						t.Fatalf("landing canceled: %+v", u)
					}
				default:
					t.Fatal("result not reported")
				}
				if claims != 1 {
					t.Fatalf("claims=%d", claims)
				}
			} else if claims != 0 {
				t.Fatalf("idle shutdown claimed work: %d", claims)
			}
		})
	}
}
