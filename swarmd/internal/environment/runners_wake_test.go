package environment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Runners.Acquire must wake sleeping registered runners once across
// concurrent landings without changing claim timeout/fallback or waking a live
// idle runner. Real store, Git archive and executable fixtures exercise the
// provider boundary without cloud infrastructure or a daemon.
func TestRunnerWakeAcquire(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	git := func(args ...string) string {
		t.Helper()
		out, err := sandbox.Command(context.Background(), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", source)
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", source, "add", "file")
	git("-C", source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "source")
	head := git("-C", source, "rev-parse", "HEAD")
	for _, mode := range []string{"sleeping", "stale_idle", "idle", "failure", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			auth := pebblestore.NewClientAuthStore(db)
			token, err := auth.CreateRunnerJoinToken("account", "user", "runner", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = auth.ConsumeRunnerJoinToken(token, time.Now()); err != nil {
				t.Fatal(err)
			}
			if mode == "idle" || mode == "stale_idle" {
				seen := time.Now()
				if mode == "stale_idle" {
					seen = seen.Add(-91 * time.Second)
				}
				if err := db.SetRunnerStatus("account", pebblestore.RunnerStatus{Name: "runner", State: "idle", LastSeen: seen.UnixMilli()}); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			marker := filepath.Join(dir, "calls")
			command := filepath.Join(dir, "wake")
			script := "#!/bin/sh\nprintf 'wake\\n' >> \"$1\"\n"
			if mode == "failure" {
				script += "exit 7\n"
			}
			if err := os.WriteFile(command, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			w := &RunnerWake{Command: command + " " + marker}
			if mode == "disabled" {
				w.Command = ""
			}
			p := Runners{Store: db, Wait: 200 * time.Millisecond, Wake: w}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					start := time.Now()
					env, err := p.Acquire(ctx, Spec{Account: "account", Checkout: source, BaseCommit: head, Commit: head, Checks: []string{"true"}, CheckTimeout: time.Minute})
					if env != nil || !errors.Is(err, errNoRunnerAvailable) || time.Since(start) < p.Wait {
						t.Errorf("claim wait changed: %v %v", env, err)
					}
				}()
			}
			wg.Wait()
			if mode == "idle" || mode == "disabled" {
				w.mu.Lock()
				started := !w.lastStart.IsZero()
				w.mu.Unlock()
				if started {
					t.Fatal("unexpected wake start")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("unexpected wake output: %v", err)
				}
			} else {
				deadline := time.Now().Add(3 * time.Second)
				for {
					data, err := os.ReadFile(marker)
					if err == nil && string(data) == "wake\n" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("wake count: %q %v", data, err)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		})
	}
}
