package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/buildinfo"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Shared setup for the machine update watch tests: the linked two-daemon
// fixture (real stores, real pinned TLS link) and the installer's update
// state file (SWARM_UPDATE_STATE), which the tests write as the installer
// does. The daemon's own build commit is left empty, so the running commit
// is the one the installer reports and changes only through that file (no
// shared variable is written while followers read it). No provider, network
// or ambient state is used.

// writeMachineUpdateState writes the installer's update state file.
func writeMachineUpdateState(t *testing.T, path string, state map[string]any) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func machineUpdateStateFile(t *testing.T) string {
	t.Helper()
	before := buildinfo.Commit
	buildinfo.Commit = ""
	t.Cleanup(func() { buildinfo.Commit = before })
	path := filepath.Join(t.TempDir(), "update-state.json")
	t.Setenv("SWARM_UPDATE_STATE", path)
	return path
}

func (w *envWatchRuntime) machineOutcomes(t *testing.T) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := w.server.sessions.ListSessionMessages(w.session, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.MessageSnapshot
	for _, m := range messages {
		if m.Metadata["source"] == pebblestore.SessionWakeSourceMachineWatch {
			out = append(out, m)
		}
	}
	return out
}

func (w *envWatchRuntime) machineWatches(t *testing.T) []pebblestore.V3MachineWatchRecord {
	t.Helper()
	watches, err := w.server.sessions.Store().ListV3MachineWatches(w.session)
	if err != nil {
		t.Fatal(err)
	}
	return watches
}

// Purpose:
//   - Requirement: the home's Orchestrator watches a linked machine's update
//     the way it watches tasks: wait_machine with machine returns at once
//     (state watching, a durable watch), and when the machine runs another
//     commit a new turn wakes the conversation with old -> new and the
//     update's reason; a second wait_machine is the same watch. The read is
//     the machine's own summary over the link (machine_status's data).
//   - Threat: the observed dead end, a factory that updated after hours with
//     nothing telling the AI, which found out only by calling machine_status.
//   - Boundary: Runtime.executeMachineWatch/runMachineWatch,
//     Server.ReadMachineUpdate (linked: ReadFleetMachineStatusView over the
//     link to the machine's handleBoxSummary), the session wake
//     (SessionWakeSourceMachineWatch) and V3MachineWatchRecord. Two real
//     daemons over the real link; only the installer's file is written here.
func TestMachineWatchWakesWhenALinkedMachineUpdates(t *testing.T) {
	statePath := machineUpdateStateFile(t)
	writeMachineUpdateState(t, statePath, map[string]any{"auto": "on", "channel": "dev", "state": "waiting", "reason": "busy (2 running); tries again at the next update", "running": "aaaaaaaaa1111", "candidate": "bbbbbbbbb2222", "at": time.Now().UnixMilli()})
	f := newLinkedEnvironmentFixture(t)
	home := newEnvWatchRuntime(t, f.home.s, f.homeP, "machine-watch-home")
	home.runtime.SetMachineWatchInterval(20 * time.Millisecond)

	watching, err := home.call(t, map[string]any{"action": "wait_machine", "machine": "box"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := watching["data"].(map[string]any)
	id, _ := data["watch_id"].(string)
	if watching["state"] != "watching" || !strings.HasPrefix(id, tool.MachineWatchIDPrefix) || data["running"] != "aaaaaaaaa1111" || !strings.Contains(fmt.Sprint(watching["next_action"]), "cancel_watch") || len(home.machineWatches(t)) != 1 {
		t.Fatalf("linked machine watch: %v", watching)
	}
	if again, err := home.call(t, map[string]any{"action": "wait_machine", "machine": "box"}); err != nil || again["data"].(map[string]any)["already_watching"] != true || len(home.machineWatches(t)) != 1 {
		t.Fatalf("second watch of the same machine: %v %v", again, err)
	}
	// No wake while the machine keeps its commit.
	select {
	case woke := <-home.controller.enqueued:
		t.Fatalf("woken before the machine updated: %s", woke)
	case <-time.After(200 * time.Millisecond):
	}

	// The installer switches: the machine runs the new commit.
	writeMachineUpdateState(t, statePath, map[string]any{"auto": "on", "channel": "dev", "state": "installed", "reason": "aaaaaaaaa -> bbbbbbbbb", "running": "bbbbbbbbb2222", "candidate": "bbbbbbbbb2222", "at": time.Now().UnixMilli()})
	home.awaitWake(t)
	outcomes := home.machineOutcomes(t)
	if len(outcomes) != 1 {
		t.Fatalf("want one outcome, got %d", len(outcomes))
	}
	for _, want := range []string{"[Machine updated]", "linked machine box", "Commit: aaaaaaaaa -> bbbbbbbbb", "Update: aaaaaaaaa -> bbbbbbbbb"} {
		if !strings.Contains(outcomes[0].Content, want) {
			t.Fatalf("outcome lacks %q: %s", want, outcomes[0].Content)
		}
	}
	if outcomes[0].Metadata["machine_watch_outcome"] != tool.MachineWatchUpdated || outcomes[0].Metadata["machine"] != "box" || outcomes[0].Metadata["untrusted_content"] != true || len(home.machineWatches(t)) != 0 {
		t.Fatalf("outcome metadata or watch left: %v %d", outcomes[0].Metadata, len(home.machineWatches(t)))
	}
}

// Purpose:
//   - Requirement: a watch on this machine wakes when an update waits to be
//     installed longer than stuck_minutes, saying why (here: runs blocking),
//     and when an update fails (rolled back), saying the install error;
//     cancel_watch with the watch_id stops a watch (no wake); a task's
//     session cannot watch (the Orchestrator's), and stuck_minutes is
//     bounded. How long a candidate has waited is durable state of the watch
//     (a restart does not reset it), so the test sets that time in the record
//     as a candidate seen earlier.
//   - Threat: an update stuck for hours behind busy work with nobody told, a
//     rolled-back update reported as nothing, or a cancelled watch that still
//     wakes.
//   - Boundary: Runtime.executeMachineWatch/runMachineWatch/
//     cancelMachineWatches, EvaluateMachineWatch, Server.ReadMachineUpdate
//     (local: buildinfo, boxUpdateState, the drain), V3MachineWatchRecord.
func TestMachineWatchWakesWhenAnUpdateIsStuckOrFails(t *testing.T) {
	statePath := machineUpdateStateFile(t)
	waiting := map[string]any{"auto": "on", "channel": "dev", "state": "waiting", "reason": "busy (2 running); tries again at the next update", "running": "aaaaaaaaa1111", "candidate": "ccccccccc3333", "at": time.Now().UnixMilli()}
	writeMachineUpdateState(t, statePath, waiting)
	f := newLinkedEnvironmentFixture(t)
	local := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "machine-watch-local")
	local.runtime.SetMachineWatchInterval(20 * time.Millisecond)
	store := f.remote.s.sessions.Store()

	// Refusals: stuck_minutes out of range; a task's session.
	if _, err := local.call(t, map[string]any{"action": "wait_machine", "stuck_minutes": float64(1)}); err == nil || !strings.Contains(err.Error(), "stuck_minutes") {
		t.Fatalf("stuck_minutes 1: %v", err)
	}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "machine-watch-task", AccountScopeID: f.remoteP.AccountScopeID, UserID: f.remoteP.UserID, Metadata: map[string]any{"task_id": "t1"}}, f.remoteP.UserID, f.remoteP.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	task := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "machine-watch-task")
	if _, err := task.call(t, map[string]any{"action": "wait_machine"}); err == nil || !strings.Contains(err.Error(), "Orchestrator") {
		t.Fatalf("task session watch: %v", err)
	}

	// Stuck: the candidate was first seen waiting 45 minutes ago.
	watching, err := local.call(t, map[string]any{"action": "wait_machine", "stuck_minutes": float64(30)})
	if err != nil || watching["state"] != "watching" {
		t.Fatalf("local watch: %v %v", watching, err)
	}
	records := local.machineWatches(t)
	if len(records) != 1 || records[0].Candidate != "ccccccccc3333" || records[0].CandidateSince == 0 || records[0].StuckAfterMinutes != 30 {
		t.Fatalf("watch record: %+v", records)
	}
	select {
	case woke := <-local.controller.enqueued:
		t.Fatalf("woken before the update was stuck: %s", woke)
	case <-time.After(200 * time.Millisecond):
	}
	record := records[0]
	record.CandidateSince = time.Now().Add(-45 * time.Minute).UnixMilli()
	if err := store.PutV3MachineWatch(record); err != nil {
		t.Fatal(err)
	}
	local.awaitWake(t)
	stuck := local.machineOutcomes(t)
	if len(stuck) != 1 || stuck[0].Metadata["machine_watch_outcome"] != tool.MachineWatchStuck {
		t.Fatalf("stuck outcome: %+v", stuck)
	}
	for _, want := range []string{"[Machine update stuck]", "this machine", "Waiting to install: ccccccccc", "Why: runs blocking or waiting: busy (2 running)", "request_update"} {
		if !strings.Contains(stuck[0].Content, want) {
			t.Fatalf("stuck outcome lacks %q: %s", want, stuck[0].Content)
		}
	}
	if len(local.machineWatches(t)) != 0 {
		t.Fatal("a delivered watch was left")
	}

	// cancel_watch with the watch_id: no wake when the update then fails.
	// (Each part uses its own conversation: the wake above left a pending
	// turn on the first, which would take later outcomes as notes.)
	quiet := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "machine-watch-cancel")
	quiet.runtime.SetMachineWatchInterval(20 * time.Millisecond)
	watching, err = quiet.call(t, map[string]any{"action": "wait_machine"})
	if err != nil {
		t.Fatal(err)
	}
	id := watching["data"].(map[string]any)["watch_id"].(string)
	cancelled, err := quiet.call(t, map[string]any{"action": "cancel_watch", "watch_id": id})
	if err != nil || cancelled["state"] != "cancelled" || len(quiet.machineWatches(t)) != 0 {
		t.Fatalf("cancel: %v %v", cancelled, err)
	}
	if _, err := quiet.call(t, map[string]any{"action": "cancel_watch", "watch_id": id}); err == nil || !strings.Contains(err.Error(), "nothing was cancelled") {
		t.Fatalf("second cancel: %v", err)
	}
	failed := map[string]any{"auto": "on", "channel": "dev", "state": "rolled_back", "reason": "ccccccccc did not come up; back on aaaaaaaaa", "running": "aaaaaaaaa1111", "candidate": "ccccccccc3333", "at": time.Now().Add(time.Second).UnixMilli()}
	writeMachineUpdateState(t, statePath, failed)
	select {
	case woke := <-quiet.controller.enqueued:
		t.Fatalf("a cancelled watch woke the conversation: %s", woke)
	case <-time.After(300 * time.Millisecond):
	}
	if n := len(quiet.machineOutcomes(t)); n != 0 {
		t.Fatalf("a cancelled watch delivered %d outcomes", n)
	}

	// Failed: a watch made before the rollback wakes with the install error.
	writeMachineUpdateState(t, statePath, waiting)
	broken := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "machine-watch-failed")
	broken.runtime.SetMachineWatchInterval(20 * time.Millisecond)
	if _, err := broken.call(t, map[string]any{"action": "wait_machine"}); err != nil {
		t.Fatal(err)
	}
	failed["at"] = time.Now().Add(time.Second).UnixMilli()
	writeMachineUpdateState(t, statePath, failed)
	broken.awaitWake(t)
	all := broken.machineOutcomes(t)
	if len(all) != 1 || all[0].Metadata["machine_watch_outcome"] != tool.MachineWatchFailed || !strings.Contains(all[0].Content, "[Machine update failed: rolled_back]") || !strings.Contains(all[0].Content, "Why: install error: ccccccccc did not come up") {
		t.Fatalf("failed outcome: %+v", all)
	}
}
