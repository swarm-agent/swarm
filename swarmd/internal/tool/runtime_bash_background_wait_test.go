//go:build !windows

package tool

import (
	"strings"
	"testing"
	"time"
)

// Purpose (shared): an agent waits for its background command with one
// blocking bash call (background_id + command=wait + wait_seconds) instead of
// sleep loops. The call returns at once when the command ends (exit code and
// output tail), answers still_running with elapsed time when its bound
// passes, and never waits past the server cap.
// Threat: agents poll with sleep for many minutes (the observed stall), a
// wait that returns before its command ends claims an end it did not see, or
// a model-supplied wait_seconds holds the call without bound.
// Owners: Runtime.backgroundCommandAction, backgroundWaitSeconds,
// waitBackgroundCommand, maxBackgroundWaitSeconds.
// Layer: the bash tool boundary over the real background registry and a
// temporary Pebble store (newBGFixture); host processes are bounded fixture
// commands, never providers.

func TestBackgroundBashWaitReturnsWhenCommandEnds(t *testing.T) {
	f := newBGFixture(t, "s-wait")
	payload, err := f.bash(t, "s-wait", map[string]any{"command": "sleep 0.3; echo waited-end", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := payload["background_id"].(string)
	if id == "" || !strings.Contains(payload["note"].(string), "command=wait") {
		t.Fatalf("start answer does not point at command=wait: %#v", payload)
	}
	start := time.Now()
	out, err := f.bash(t, "s-wait", map[string]any{"background_id": id, "command": "wait", "wait_seconds": 10})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("wait did not return when the command ended: %s", elapsed)
	}
	if out["still_running"] != false || out["status"] != backgroundStateExited || out["exit_code"] != float64(0) {
		t.Fatalf("wait did not report the end: %#v", out)
	}
	if !strings.Contains(out["output"].(string), "waited-end") {
		t.Fatalf("wait lacks the output tail: %#v", out)
	}
	if !f.command(t, "s-wait", id).finished() {
		t.Fatal("wait reported an end the command had not reached")
	}
}

func TestBackgroundBashWaitAnswersStillRunningAtItsBound(t *testing.T) {
	f := newBGFixture(t, "s-bound")
	defer func(unit time.Duration) { backgroundWaitUnit = unit }(backgroundWaitUnit)
	backgroundWaitUnit = 50 * time.Millisecond
	payload, err := f.bash(t, "s-bound", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	start := time.Now()
	out, err := f.bash(t, "s-bound", map[string]any{"background_id": id, "command": "wait", "wait_seconds": 4})
	if err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	if waited < 150*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("wait did not hold for its bound (4 units of 50ms): %s", waited)
	}
	if out["still_running"] != true || out["status"] != backgroundStateRunning {
		t.Fatalf("bound passed but the answer is not still_running: %#v", out)
	}
	if _, ok := out["exit_code"]; ok {
		t.Fatalf("a running command has no exit code: %#v", out)
	}
	if ms, _ := out["elapsed_ms"].(float64); ms <= 0 {
		t.Fatalf("no elapsed time: %#v", out)
	}
	if !strings.Contains(out["summary"].(string), "still running") {
		t.Fatalf("summary: %#v", out["summary"])
	}
	if f.command(t, "s-bound", id).finished() {
		t.Fatal("the wait ended the command")
	}
	if _, err := f.bash(t, "s-bound", map[string]any{"background_id": id, "command": "kill", "category": "update"}); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundBashWaitRespectsTheServerCap(t *testing.T) {
	f := newBGFixture(t, "s-cap", "s-other")
	defer func(unit time.Duration) { backgroundWaitUnit = unit }(backgroundWaitUnit)
	backgroundWaitUnit = time.Millisecond
	payload, err := f.bash(t, "s-cap", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	defer func() {
		_, _ = f.bash(t, "s-cap", map[string]any{"background_id": id, "command": "kill", "category": "update"})
	}()

	start := time.Now()
	out, err := f.bash(t, "s-cap", map[string]any{"background_id": id, "command": "wait", "wait_seconds": 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if out["wait_seconds"] != float64(maxBackgroundWaitSeconds) || out["still_running"] != true {
		t.Fatalf("wait_seconds was not capped at %d: %#v", maxBackgroundWaitSeconds, out)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("capped wait (600 units of 1ms) held for %s", waited)
	}
	// Omitted wait_seconds is the default, below the cap.
	if out, err := f.bash(t, "s-cap", map[string]any{"background_id": id, "command": "wait"}); err != nil || out["wait_seconds"] != float64(defaultBackgroundWaitSeconds) {
		t.Fatalf("default wait: %v %#v", err, out)
	}
	// Refusals change nothing: a zero bound, wait_seconds on kill, and
	// another conversation's command.
	for name, args := range map[string]map[string]any{
		"zero":    {"background_id": id, "command": "wait", "wait_seconds": 0},
		"on_kill": {"background_id": id, "command": "kill", "wait_seconds": 5, "category": "update"},
	} {
		if _, err := f.bash(t, "s-cap", args); err == nil {
			t.Fatalf("%s was not refused", name)
		}
	}
	if _, err := f.bash(t, "s-other", map[string]any{"background_id": id, "command": "wait", "wait_seconds": 1}); err == nil || !strings.Contains(err.Error(), "no background command") {
		t.Fatalf("another conversation waited on the command: %v", err)
	}
	c := f.command(t, "s-cap", id)
	c.mu.Lock()
	killed := c.killed
	c.mu.Unlock()
	if killed || c.finished() {
		t.Fatal("a refused call changed the command")
	}
}
