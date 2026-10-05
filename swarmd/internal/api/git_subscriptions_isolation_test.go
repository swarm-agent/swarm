package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/gitwatch"
)

// Requirement: at the real HTTP/native boundary, unauthorized selectors must
// allocate nothing, one exhausted listener budget must not terminate an admitted
// lane, and cancellation must free its slot. Subscriptions.Acquire and
// handleGitSubscriptions own these bounds. Saturating listeners on one real
// native root is the narrowest deterministic way to exercise the actual cap
// without opening 256 filesystem trees. No callback/backend is substituted.
func testGitSubscriptionCapacityHTTP(t *testing.T, base, path string, subscriptions *gitwatch.Subscriptions, logPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	paths, err := gitstatus.ResolveWatchPaths(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	config := gitwatch.Config{WorktreeRoot: paths.RepoRoot, GitDir: paths.GitDir, CommonDir: paths.CommonDir}
	for i := 0; i < 255; i++ {
		_, release, err := subscriptions.Acquire(config, "agent/candidate")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	post := func(ctx context.Context, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/workspace/git/subscriptions", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	bad := post(ctx, []byte(`{"repositories":[]}`))
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest || strings.Contains(bad.Header.Get("Content-Type"), "event-stream") {
		t.Fatal("invalid request must fail before SSE headers")
	}
	selector := gitSubscriptionSelector{WorkspacePath: path, SessionID: "candidate", Branch: "agent/candidate"}
	foreign := selector
	foreign.SessionID = "foreign-selector"
	payload, _ := json.Marshal(map[string]any{"repositories": []gitSubscriptionSelector{foreign, selector, selector}})
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	response := post(streamCtx, payload)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "event-stream") {
		t.Fatal("mixed selectors must open a stream")
	}
	notices := make(chan gitSubscriptionNotice, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if !strings.HasPrefix(scanner.Text(), "data: ") {
				continue
			}
			var notice gitSubscriptionNotice
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &notice) != nil {
				return
			}
			select {
			case notices <- notice:
			case <-streamCtx.Done():
				return
			}
		}
	}()
	next := func() gitSubscriptionNotice {
		t.Helper()
		select {
		case notice := <-notices:
			return notice
		case <-ctx.Done():
			t.Fatal("native subscription notice timed out")
		case <-done:
			t.Fatal("one failed selector closed the shared stream")
		}
		return gitSubscriptionNotice{}
	}
	seen := map[int]gitSubscriptionNotice{}
	for len(seen) < 3 {
		notice := next()
		seen[notice.Index] = notice
	}
	if seen[0].Kind != "lost" || seen[0].ReasonCode != "selector_unavailable" || strings.Contains(seen[0].Error, path) || strings.Contains(seen[0].Error, foreign.SessionID) {
		t.Fatalf("foreign selector leaked data: %+v", seen[0])
	}
	if seen[1].Kind != "ready" || seen[2].Kind != "lost" || seen[2].ReasonCode != "watch_capacity" {
		t.Fatalf("admission must skip foreign, retain healthy and reject only overflow: %+v", seen)
	}
	if _, release, err := subscriptions.Acquire(config, selector.Branch); !errors.Is(err, gitwatch.ErrSubscriptionCapacity) {
		if release != nil {
			release()
		}
		t.Fatalf("expected full listener budget, got %v", err)
	}
	count := func() int64 {
		t.Helper()
		stat, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		return stat.Size()
	}
	before := count()
	select {
	case notice := <-notices:
		t.Fatalf("idle failed selector retried: %+v", notice)
	case <-done:
		t.Fatal("failed selectors closed the transport")
	case <-time.After(1200 * time.Millisecond):
	}
	if count() != before {
		t.Fatal("failed selectors caused recurring healthy Git reads")
	}
	file := filepath.Join(path, "capacity-notice")
	if err := os.WriteFile(file, []byte("native event\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if notice := next(); notice.Index != 1 || notice.Kind != "changed" {
		t.Fatalf("admitted lane stopped receiving native events: %+v", notice)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	stop()
	response.Body.Close()
	<-done
	// Handler cleanup is asynchronous to client cancellation. Wait only for the
	// exact slot release (no Git commands); every acquired probe is released.
	for {
		_, release, err := subscriptions.Acquire(config, selector.Branch)
		if err == nil {
			release()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("stream leaked its watcher slot")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
