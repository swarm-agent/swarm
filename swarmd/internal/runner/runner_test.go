package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	store "swarm/packages/swarmd/internal/store/pebble"
	"sync"
	"testing"
	"time"
)

type fakeEngine struct {
	unhealthy bool
	heartbeat <-chan struct{}
	calls     int
}

func (e *fakeEngine) Health(context.Context, string) (bool, bool, error) {
	if e.unhealthy {
		return false, false, errors.New("engine unavailable")
	}
	return true, true, nil
}
func (e *fakeEngine) Execute(ctx context.Context, l store.RunnerLanding, dir string, emit func([]byte) error) ([]int, error) {
	e.calls++
	if _, err := os.ReadFile(filepath.Join(dir, "source.tar.gz")); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.heartbeat:
	}
	codes := []int{}
	for range l.Checks {
		if err := emit([]byte("check output\n")); err != nil {
			return nil, err
		}
		codes = append(codes, 0)
	}
	return codes, nil
}

// Purpose: Runner.Run must carry a lease through verified archive payload, execution, bounded
// output and result, renew it while running, and remove disposable files.
// httptest plus the Engine boundary is the narrowest deterministic loop test;
// it does not claim Docker/gVisor isolation or live hub compatibility.
func TestRunnerLifecycle(t *testing.T) {
	for _, scenario := range []string{"checks", "fetch_failure", "sha_mismatch", "size_mismatch", "unsupported_format"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			heartbeat := make(chan struct{})
			var once sync.Once
			engine := &fakeEngine{heartbeat: heartbeat}
			var got update
			chunks := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-credential" {
					t.Error("missing auth")
					w.WriteHeader(401)
					return
				}
				switch {
				case r.URL.Path == "/v3/runners/status":
				case r.URL.Path == "/v3/runners/claim":
					hash := fmt.Sprintf("%x", sha256.Sum256([]byte("archive bytes")))
					if scenario == "sha_mismatch" {
						hash = strings.Repeat("0", 64)
					}
					size := 13
					if scenario == "size_mismatch" {
						size++
					}
					format := "archive-v1"
					if scenario == "unsupported_format" {
						format = "bundle"
					}
					json.NewEncoder(w).Encode(map[string]any{"landing": map[string]any{"id": "test-landing", "lease_id": "test-lease", "checks": []string{"true", "true"}, "payload_format": format, "archive_sha256": hash, "archive_bytes": size}})
				case strings.HasSuffix(r.URL.Path, "/payload"):
					if scenario == "fetch_failure" {
						w.WriteHeader(503)
					} else {
						w.Write([]byte("archive bytes"))
					}
				case strings.HasSuffix(r.URL.Path, "/heartbeat"):
					once.Do(func() { close(heartbeat) })
				case strings.HasSuffix(r.URL.Path, "/output"):
					var u update
					json.NewDecoder(r.Body).Decode(&u)
					if len(u.Output) > 16<<10 || u.LeaseID != "test-lease" {
						t.Error("invalid output")
					}
					chunks++
				case strings.HasSuffix(r.URL.Path, "/result"):
					json.NewDecoder(r.Body).Decode(&got)
					cancel()
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			r := Runner{Hub: server.URL, Credential: "test-credential", WorkDir: dir, Engine: engine, heartbeat: time.Millisecond, statusInterval: time.Hour}
			_ = r.Run(ctx)
			if got.LeaseID != "test-lease" {
				t.Fatal("result not delivered")
			}
			if scenario != "checks" {
				reason := map[string]string{"fetch_failure": "fetch payload", "sha_mismatch": "archive_sha256 mismatch", "size_mismatch": "archive_bytes mismatch", "unsupported_format": "invalid archive-v1"}[scenario]
				if !strings.Contains(got.Output, reason) {
					t.Fatalf("missing failure reason %q: %+v", reason, got)
				}
				if got.State != "environment_failed" || len(got.ExitCodes) != 0 || engine.calls != 0 || got.Output == "" || chunks != 0 {
					t.Fatalf("payload failure misclassified: %+v", got)
				}
			} else {
				if len(got.ExitCodes) != 2 || chunks != 2 || engine.calls != 1 {
					t.Fatalf("incomplete run: %+v chunks=%d", got, chunks)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("work not cleaned: %v %v", entries, err)
			}
		})
	}
}

// Purpose: engine failure must report unhealthy and prevent claiming work at
// Runner.Run's health gate; checking requests proves absence of admission.
func TestRunnerUnhealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	claims := 0
	unhealthy := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claims++
		}
		if strings.HasSuffix(r.URL.Path, "/status") {
			var s store.RunnerStatus
			json.NewDecoder(r.Body).Decode(&s)
			unhealthy = s.State == "unhealthy" && s.Reason != ""
			cancel()
		}
	}))
	defer server.Close()
	r := Runner{Hub: server.URL, Credential: "test", WorkDir: t.TempDir(), Engine: &fakeEngine{unhealthy: true}}
	_ = r.Run(ctx)
	if claims != 0 || !unhealthy {
		t.Fatalf("claims=%d unhealthy=%v", claims, unhealthy)
	}
}
