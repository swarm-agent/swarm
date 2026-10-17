package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type slotsEngine struct {
	started chan string
	release chan struct{}
}

func (e *slotsEngine) Health(context.Context, string) (bool, bool, error) { return true, true, nil }
func (e *slotsEngine) Execute(ctx context.Context, l store.RunnerLanding, _ string, emit func([]byte) error) ([]int, error) {
	e.started <- l.ID
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []int{0}, emit([]byte(l.ID))
}

// Purpose: Runner.Run bounds concurrent leases, publishes all active claims and
// drains on cancellation. The HTTP/Engine boundary proves lifecycle behavior
// without Docker or external services, including no early exit during drain.
func TestRunnerSlots(t *testing.T) {
	for _, n := range []int{1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := &slotsEngine{make(chan string, n+1), make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(e.release) }) }
			defer release()
			var mu sync.Mutex
			claims, results := 0, 0
			sum := sha256.Sum256([]byte("payload"))
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.URL.Path == "/v3/runners/claim":
					var capacity struct {
						Slots int `json:"slots"`
					}
					if err := json.NewDecoder(req.Body).Decode(&capacity); err != nil || capacity.Slots != n {
						t.Errorf("capacity: %+v %v", capacity, err)
					}
					mu.Lock()
					claims++
					id := fmt.Sprint(claims)
					mu.Unlock()
					json.NewEncoder(w).Encode(map[string]any{"landing": landingRequest{RunnerLanding: store.RunnerLanding{ID: id, LeaseID: "lease-" + id}, PayloadFormat: "archive-v1", ArchiveSHA256: hex.EncodeToString(sum[:]), ArchiveBytes: 7}})
				case strings.HasSuffix(req.URL.Path, "/payload"):
					fmt.Fprint(w, "payload")
				case strings.HasSuffix(req.URL.Path, "/result"):
					var result update
					json.NewDecoder(req.Body).Decode(&result)
					if len(result.ExitCodes) != 1 || result.ExitCodes[0] != 0 || result.Output == "" {
						t.Errorf("invalid drained result: %+v", result)
					}
					mu.Lock()
					results++
					mu.Unlock()
				}
			}))
			defer hub.Close()
			r := &Runner{Slots: n, Hub: hub.URL, Credential: "test", WorkDir: t.TempDir(), StateFile: filepath.Join(t.TempDir(), "state"), Engine: e}
			done := make(chan error, 1)
			go func() { done <- r.Run(ctx) }()
			defer func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
			}()
			for i := 0; i < n; i++ {
				select {
				case <-e.started:
				case <-time.After(5 * time.Second):
					t.Fatal("slots did not fill")
				}
			}
			data, err := os.ReadFile(r.StateFile)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != n {
				t.Fatalf("state=%q", data)
			}
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) != 3 || fields[0] != "busy" {
					t.Fatalf("invalid line %q", line)
				}
				if _, err := time.Parse(time.RFC3339, fields[2]); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				t.Fatalf("returned before drain: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			mu.Lock()
			count := claims
			mu.Unlock()
			if count != n {
				t.Fatalf("claims=%d slots=%d", count, n)
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				done <- nil
			case <-time.After(5 * time.Second):
				t.Fatal("drain hung")
			}
			mu.Lock()
			count = results
			mu.Unlock()
			if count != n {
				t.Fatalf("results=%d", count)
			}
			data, err = os.ReadFile(r.StateFile)
			if err != nil || string(data) != "idle\n" {
				t.Fatalf("final state=%q err=%v", data, err)
			}
		})
	}
}
