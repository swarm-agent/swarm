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

// Purpose: a provider that sends null for every unset optional field (a
// status or kill call with wait_seconds: null) is served as if wait_seconds
// were omitted, as backgroundWaitSeconds already reads null; a real
// wait_seconds on status or kill is still refused.
// Threat (review note): `_, hasWait := args["wait_seconds"]` counted null as
// present, so such a provider could never read or stop its background
// command ("wait_seconds goes with command=wait").
// Owner: Runtime.backgroundCommandAction. Layer: the bash tool boundary over
// the real background registry (newBGFixture), the narrowest layer that
// parses the call's arguments.
func TestBackgroundBashNullWaitSecondsIsOmitted(t *testing.T) {
	f := newBGFixture(t, "s-null")
	payload, err := f.bash(t, "s-null", map[string]any{"command": "sleep 30", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	// A real value on status is refused and changes nothing.
	if _, err := f.bash(t, "s-null", map[string]any{"background_id": id, "command": "status", "wait_seconds": 5}); err == nil || !strings.Contains(err.Error(), "wait_seconds goes with command=wait") {
		t.Fatalf("status with wait_seconds 5: %v", err)
	}
	out, err := f.bash(t, "s-null", map[string]any{"background_id": id, "command": "status", "wait_seconds": nil})
	if err != nil {
		t.Fatalf("status with null wait_seconds refused: %v", err)
	}
	if out["status"] != backgroundStateRunning || out["still_running"] == false {
		t.Fatalf("status answer: %#v", out)
	}
	if f.command(t, "s-null", id).finished() {
		t.Fatal("a status read ended the command")
	}
	// No command and a null wait_seconds is a status read, not a wait.
	if out, err := f.bash(t, "s-null", map[string]any{"background_id": id, "wait_seconds": nil}); err != nil || out["status"] != backgroundStateRunning {
		t.Fatalf("implicit status with null wait_seconds: %v %#v", err, out)
	} else if _, waited := out["waited_ms"]; waited {
		t.Fatalf("null wait_seconds was taken as a wait: %#v", out)
	}
	if _, err := f.bash(t, "s-null", map[string]any{"background_id": id, "command": "kill", "wait_seconds": nil, "category": "update"}); err != nil {
		t.Fatalf("kill with null wait_seconds refused: %v", err)
	}
	c := f.command(t, "s-null", id)
	c.mu.Lock()
	killed := c.killed
	c.mu.Unlock()
	if !killed {
		t.Fatal("kill with null wait_seconds did not stop the command")
	}
}

// Purpose: the bash schema leaves the wait cap to the server: wait_seconds
// keeps minimum 1 but has no maximum, and its description states that a
// larger value is accepted and waits the cap (maxBackgroundWaitSeconds),
// which backgroundWaitSeconds applies (TestBackgroundBashWaitRespectsTheServerCap
// runs a 1_000_000 wait through the tool).
// Threat (review note): a schema maximum of 600 contradicts "a larger value
// uses the cap", so a strict provider rejects a call the server would serve.
// Owner: Runtime.Definitions (bash parameters). Layer: the definition the
// provider receives.
func TestBackgroundBashWaitSchemaLeavesTheCapToTheServer(t *testing.T) {
	var waitField map[string]any
	for _, d := range NewRuntime(1).Definitions() {
		if d.Name != "bash" {
			continue
		}
		props, _ := d.Parameters["properties"].(map[string]any)
		waitField, _ = props["wait_seconds"].(map[string]any)
	}
	if waitField == nil {
		t.Fatal("bash has no wait_seconds field")
	}
	if _, ok := waitField["maximum"]; ok {
		t.Fatalf("wait_seconds schema sets a maximum: %#v", waitField)
	}
	if waitField["minimum"] != 1 {
		t.Fatalf("wait_seconds minimum = %#v, want 1", waitField["minimum"])
	}
	desc, _ := waitField["description"].(string)
	if !strings.Contains(desc, "caps it at 600") || !strings.Contains(desc, "larger value is accepted") || maxBackgroundWaitSeconds != 600 {
		t.Fatalf("wait_seconds description does not state the cap %d: %q", maxBackgroundWaitSeconds, desc)
	}
	if got, err := backgroundWaitSeconds(map[string]any{"wait_seconds": float64(601)}); err != nil || got != maxBackgroundWaitSeconds {
		t.Fatalf("601 = %d, %v; want the cap", got, err)
	}
}

// Purpose: stopping the background registry (Runtime.Close) drains every
// command goroutine, including its end report, before it returns, so no
// report reaches the session store after the daemon (or a test) closes it.
// Threat (gate finding): a wait returns when its command's done channel
// closes, but the report runs after that on the command goroutine; the test
// returned, its cleanup closed the Pebble store, and the late report
// panicked in deliverBackgroundReport. Negative: a command launched after
// shutdown began is stopped and never reports. Authority:
// backgroundCommands.launch (active), backgroundCommands.stopAll. Layer:
// the background registry over the real fixture store.
func TestBackgroundBashWaitThenStopDrainsTheReport(t *testing.T) {
	f := newBGFixture(t, "s-drain")
	payload, err := f.bash(t, "s-drain", map[string]any{"command": "echo drained", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	id := payload["background_id"].(string)
	if _, err := f.bash(t, "s-drain", map[string]any{"background_id": id, "command": "wait", "wait_seconds": 10}); err != nil {
		t.Fatal(err)
	}
	f.runtime.background.stopAll()
	select {
	case got := <-f.reported:
		if got != id {
			t.Fatalf("reported %s, want %s", got, id)
		}
	default:
		t.Fatal("stopAll returned before the command's report finished")
	}
	// After shutdown began, a new command is stopped and does not report.
	if _, err := f.bash(t, "s-drain", map[string]any{"command": "echo late", "background": true}); err == nil {
		t.Fatal("a command was adopted after shutdown began")
	}
	f.runtime.background.stopAll()
	select {
	case got := <-f.reported:
		t.Fatalf("a command reported after shutdown: %s", got)
	default:
	}
}
