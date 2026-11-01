package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Shared fixture for the environment build watch, log and wait tests: the
// linked two-daemon fixture (newLinkedEnvironmentFixture: real stores, real
// pinned TLS link) with a gated builder in place of the machine's Docker
// manager. The builder writes its log and steps through the same callbacks
// the Docker manager uses (EnvironmentBuild.OnLog/OnStep), then holds until
// the test releases it, so a build is observably "building" for exactly as
// long as the test wants. No provider, network or ambient state is used.

const envWatchTestWait = 20 * time.Second

type gatedEnvironmentBuilder struct {
	lines   int
	started chan string
	release chan struct{}
}

func newGatedEnvironmentBuilder(lines int) *gatedEnvironmentBuilder {
	return &gatedEnvironmentBuilder{lines: lines, started: make(chan string, 8), release: make(chan struct{})}
}

func (g *gatedEnvironmentBuilder) BuildEnvironment(ctx context.Context, b sandbox.EnvironmentBuild) (sandbox.EnvironmentBuildResult, error) {
	var log strings.Builder
	emit := func(text string) {
		log.WriteString(text)
		if b.OnLog != nil {
			b.OnLog(text)
		}
	}
	failing := false
	for i, command := range b.Setup {
		if b.OnStep != nil {
			b.OnStep("setup", i+1, len(b.Setup), command)
		}
		emit("$ " + command + "\n")
		failing = failing || strings.Contains(command, "FAIL")
	}
	for i := 0; i < g.lines; i++ {
		emit(fmt.Sprintf("line %03d %s\n", i, strings.Repeat("x", 200)))
	}
	g.started <- b.Hash
	select {
	case <-g.release:
	case <-ctx.Done():
		return sandbox.EnvironmentBuildResult{Log: log.String()}, ctx.Err()
	}
	if failing {
		emit("warm-build FAILED\n")
		return sandbox.EnvironmentBuildResult{Log: log.String()}, errors.New("setup command 2 failed (warm FAIL)")
	}
	emit("warm-build OK\n")
	return sandbox.EnvironmentBuildResult{Image: b.Image, Path: "/usr/bin:/bin", Log: log.String()}, nil
}

func (g *gatedEnvironmentBuilder) awaitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(envWatchTestWait):
		t.Fatal("the build did not start")
	}
}

func (g *gatedEnvironmentBuilder) finish(t *testing.T) {
	t.Helper()
	select {
	case g.release <- struct{}{}:
	case <-time.After(envWatchTestWait):
		t.Fatal("no build waited to be released")
	}
}

type envWatchController struct{ enqueued chan string }

func (c *envWatchController) CancelSessionRun(identity.Principal, string, string, string) (bool, error) {
	return false, nil
}

func (c *envWatchController) EnqueueSessionRun(_ identity.Principal, sessionID, runID, _ string) bool {
	c.enqueued <- sessionID + "|" + runID
	return true
}

func (c *envWatchController) CompactSession(context.Context, identity.Principal, string, string) (map[string]any, error) {
	return nil, nil
}

// envWatchRuntime is the tool runtime of one daemon of the fixture, as the
// daemon wires it: its sessions, its API server, its durable background work
// store and a run controller that records the turns a wake starts.
type envWatchRuntime struct {
	runtime    *tool.Runtime
	server     *Server
	principal  identity.Principal
	session    string
	controller *envWatchController
}

func newEnvWatchRuntime(t *testing.T, server *Server, p identity.Principal, session string) *envWatchRuntime {
	t.Helper()
	r := tool.NewRuntime(1)
	t.Cleanup(func() { _ = r.Close() })
	controller := &envWatchController{enqueued: make(chan string, 16)}
	r.SetManageSessionService(server.sessions)
	r.SetManageSessionController(controller)
	r.SetBackgroundWorkStore(server.sessions.Store())
	r.SetProjectTaskLifecycleService(server)
	if _, found, _ := server.sessions.GetSession(session); !found {
		if err := server.sessions.Store().CreateSessionForAccount(pebblestore.SessionSnapshot{ID: session, AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{}}, p.UserID, p.AccountScopeID); err != nil {
			t.Fatal(err)
		}
	}
	return &envWatchRuntime{runtime: r, server: server, principal: p, session: session, controller: controller}
}

func (w *envWatchRuntime) call(t *testing.T, args map[string]any) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: w.session, RunID: "run-1"}), envWatchTestWait)
	defer cancel()
	output, err := w.runtime.ExecuteForWorkspaceScopeWithRuntime(ctx, tool.WorkspaceScope{SessionID: w.session, Principal: w.principal}, tool.Call{Name: "manage_projects", Arguments: string(raw)})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(output), &out); err != nil {
		t.Fatalf("answer %s: %v", output, err)
	}
	return out, nil
}

func (w *envWatchRuntime) watches(t *testing.T) []pebblestore.V3EnvironmentWatchRecord {
	t.Helper()
	watches, err := w.server.sessions.Store().ListV3EnvironmentWatches(w.session)
	if err != nil {
		t.Fatal(err)
	}
	return watches
}

func (w *envWatchRuntime) awaitWake(t *testing.T) {
	t.Helper()
	select {
	case got := <-w.controller.enqueued:
		if !strings.HasPrefix(got, w.session+"|") {
			t.Fatalf("woke the wrong conversation: %s", got)
		}
	case <-time.After(envWatchTestWait):
		t.Fatal("the conversation was not woken")
	}
}

func (w *envWatchRuntime) outcomes(t *testing.T) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := w.server.sessions.ListSessionMessages(w.session, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.MessageSnapshot
	for _, m := range messages {
		if m.Metadata["source"] == pebblestore.SessionWakeSourceEnvironmentWatch {
			out = append(out, m)
		}
	}
	return out
}

// Purpose: get_environment include_log returns the build log while it runs,
// in pages addressed by byte offset (log_page offset/next_offset/total_bytes/
// more), with the setup step and command it is on, never silently cut;
// after the build it returns the last build's outcome (status, duration,
// image) and its whole log, here and over the link. Threat: the observed
// dead end, a building environment read with no log or progress, so a setup
// step's output (a warm-build OK/FAILED line) cannot be seen; or a log cut
// without saying so. Boundary: Server.ManageFolderEnvironment (home and
// machine), GetSandboxEnvironmentLog, sandboxEnvironmentDetail/LogPage and
// the build's OnLog/OnStep progress. The two daemons talk over the real link;
// only the Docker build is a test double.
func TestEnvironmentLogPagesWhileBuildingAndKeepsTheLastBuild(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	builder := newGatedEnvironmentBuilder(100)
	f.remote.s.envBuilder = builder
	if _, err := f.call(t, "", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "base_image": "debian:12", "setup": []any{"apt-get install -y git", "make warm && echo warm-build OK"}}); err != nil {
		t.Fatal(err)
	}
	builder.awaitStarted(t)

	for _, machine := range []string{"", "box"} {
		// Without include_log a running build carries its step, not its log.
		plain, err := f.call(t, machine, tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder})
		if err != nil {
			t.Fatalf("machine %q get: %v", machine, err)
		}
		view := plain["environment"].(map[string]any)
		if view["status"] != pebblestore.SandboxEnvironmentBuilding || view["log"] != nil {
			t.Fatalf("machine %q plain view: %v", machine, view)
		}
		progress, _ := view["progress"].(map[string]any)
		if progress["phase"] != "setup" || progress["setup_step"] != float64(2) || progress["setup_steps"] != float64(2) || !strings.Contains(fmt.Sprint(progress["command"]), "warm-build OK") {
			t.Fatalf("machine %q progress: %v", machine, progress)
		}

		// The last page by default, then every page from 0: together the
		// whole log so far, nothing skipped.
		last, err := f.call(t, machine, tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "include_log": true})
		if err != nil {
			t.Fatal(err)
		}
		lastView := last["environment"].(map[string]any)
		page := lastView["log_page"].(map[string]any)
		total := int(page["total_bytes"].(float64))
		if total < 20000 || page["more"] != false || page["complete"] != false || int(page["next_offset"].(float64)) != total || !strings.HasSuffix(lastView["log"].(string), "line 099 "+strings.Repeat("x", 200)+"\n") {
			t.Fatalf("machine %q last page: %v", machine, page)
		}
		var whole strings.Builder
		offset := 0
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatal("log pages never ended")
			}
			got, err := f.call(t, machine, tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "log_offset": float64(offset)})
			if err != nil {
				t.Fatal(err)
			}
			v := got["environment"].(map[string]any)
			p := v["log_page"].(map[string]any)
			if int(p["offset"].(float64)) != offset || len(v["log"].(string)) > sandboxEnvironmentLogPageBytes {
				t.Fatalf("machine %q page at %d: %v", machine, offset, p)
			}
			whole.WriteString(v["log"].(string))
			offset = int(p["next_offset"].(float64))
			if p["more"] == false {
				break
			}
		}
		if whole.Len() != total || !strings.HasPrefix(whole.String(), "$ apt-get install -y git\n") || strings.Count(whole.String(), "\nline ") != 100 {
			t.Fatalf("machine %q paged log is not the whole log: %d of %d bytes", machine, whole.Len(), total)
		}
	}

	// A bad offset is refused, and log_offset pages get_environment only.
	if _, err := f.call(t, "", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "log_offset": float64(-1)}); err == nil || !strings.Contains(err.Error(), "log_offset") {
		t.Fatalf("negative log_offset: %v", err)
	}
	if _, err := f.call(t, "", tool.FolderEnvironmentRebuild, map[string]any{"workspace_path": f.folder, "log_offset": float64(0)}); err == nil || !strings.Contains(err.Error(), "log_offset") {
		t.Fatalf("log_offset on rebuild: %v", err)
	}

	builder.finish(t)
	for _, machine := range []string{"", "box"} {
		done, err := f.call(t, machine, tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "include_log": true, "log_offset": float64(0), "wait_seconds": float64(10)})
		if err != nil {
			t.Fatal(err)
		}
		view := done["environment"].(map[string]any)
		lastBuild, _ := view["last_build"].(map[string]any)
		if view["status"] != pebblestore.SandboxEnvironmentReady || view["progress"] != nil || lastBuild["status"] != pebblestore.SandboxEnvironmentReady || !strings.HasPrefix(fmt.Sprint(lastBuild["image"]), pebblestore.SandboxEnvironmentImagePrefix) || lastBuild["duration_ms"] == nil {
			t.Fatalf("machine %q after the build: %v", machine, view)
		}
		page := view["log_page"].(map[string]any)
		if page["complete"] != true || page["offset"] != float64(0) || page["more"] != true {
			t.Fatalf("machine %q stored log first page: %v", machine, page)
		}
		tail, err := f.call(t, machine, tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "include_log": true})
		if err != nil || !strings.HasSuffix(tail["environment"].(map[string]any)["log"].(string), "warm-build OK\n") {
			t.Fatalf("machine %q last build's log tail: %v %v", machine, tail, err)
		}
	}
	stored := f.stored(t)
	if stored.LogDroppedBytes != 0 || !strings.HasSuffix(stored.Log, "warm-build OK\n") || stored.BuildStartedAt == 0 || stored.FinishedAt < stored.BuildStartedAt {
		t.Fatalf("stored outcome: dropped=%d started=%d finished=%d", stored.LogDroppedBytes, stored.BuildStartedAt, stored.FinishedAt)
	}
}

// Purpose: update_environment and rebuild_environment with wait_seconds hold
// the call until the build ends, returning as soon as it does, and otherwise
// say it is still building, how long they waited, and (Orchestrator) name
// wait_environment as the next step. Threat: the observed answer, building
// after a wait that did not visibly wait, with no way to know what to do
// next. Boundary: ManageFolderEnvironment's wait report
// (folderEnvironmentAnswer), UpdateSandboxEnvironment/RebuildSandboxEnvironment
// holds, Runtime.executeFolderEnvironments' next_action.
func TestEnvironmentWaitSecondsEndsWithTheBuildOrSaysStillBuilding(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	builder := newGatedEnvironmentBuilder(3)
	f.remote.s.envBuilder = builder

	// A merge and a replace (replace: true is a PUT on a linked machine,
	// which answers at once there; the home must still hold the wait).
	for _, c := range []struct {
		machine, image string
		replace        bool
	}{{"", "debian:12", false}, {"box", "debian:13", false}, {"", "debian:11", true}, {"box", "debian:10", true}} {
		machine := c.machine
		args := map[string]any{"workspace_path": f.folder, "base_image": c.image, "wait_seconds": float64(15)}
		if c.replace {
			args["replace"] = true
		}
		type answer struct {
			out map[string]any
			err error
		}
		got := make(chan answer, 1)
		started := time.Now()
		go func() {
			out, err := f.call(t, machine, tool.FolderEnvironmentUpdate, args)
			got <- answer{out, err}
		}()
		builder.awaitStarted(t)
		builder.finish(t)
		var a answer
		select {
		case a = <-got:
		case <-time.After(envWatchTestWait):
			t.Fatal("the held update did not return")
		}
		if a.err != nil {
			t.Fatal(a.err)
		}
		wait, _ := a.out["wait"].(map[string]any)
		if a.out["environment"].(map[string]any)["status"] != pebblestore.SandboxEnvironmentReady || wait["build_ended"] != true || wait["requested_seconds"] != float64(15) || a.out["build_started"] != true || time.Since(started) > 10*time.Second {
			t.Fatalf("machine %q update (replace %v) that the build ended: %v after %s", machine, c.replace, a.out, time.Since(started))
		}
	}

	// Still building after the wait: said so, with how long it waited.
	for _, machine := range []string{"", "box"} {
		got := make(chan map[string]any, 1)
		go func() {
			out, err := f.call(t, machine, tool.FolderEnvironmentRebuild, map[string]any{"workspace_path": f.folder, "wait_seconds": float64(1)})
			if err != nil {
				t.Error(err)
			}
			got <- out
		}()
		builder.awaitStarted(t)
		var out map[string]any
		select {
		case out = <-got:
		case <-time.After(envWatchTestWait):
			t.Fatal("the held rebuild did not return")
		}
		wait, _ := out["wait"].(map[string]any)
		if out["environment"].(map[string]any)["status"] != pebblestore.SandboxEnvironmentBuilding || wait["build_ended"] != false || wait["waited_seconds"] == nil {
			t.Fatalf("machine %q still building: %v", machine, out)
		}
		builder.finish(t)
		if _, err := f.call(t, "", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "wait_seconds": float64(10)}); err != nil {
			t.Fatal(err)
		}
	}

	// The Orchestrator's answer names the watch as the next step.
	orchestrator := newEnvWatchRuntime(t, f.home.s, f.homeP, "env-wait-orch")
	got := make(chan map[string]any, 1)
	go func() {
		out, err := orchestrator.call(t, map[string]any{"action": "update_environment", "machine": "box", "workspace_path": f.folder, "environment": map[string]any{"append_setup": []any{"echo warm"}, "wait_seconds": 1}})
		if err != nil {
			t.Error(err)
		}
		got <- out
	}()
	builder.awaitStarted(t)
	envelope := <-got
	if next := fmt.Sprint(envelope["next_action"]); !strings.Contains(next, "wait_environment") || !strings.Contains(next, f.folder) || !strings.Contains(next, "machine box") {
		t.Fatalf("still building without the watch as next step: %v", envelope)
	}
	builder.finish(t)
	if _, err := f.call(t, "", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "wait_seconds": float64(10)}); err != nil {
		t.Fatal(err)
	}
}

// Purpose: the Orchestrator watches a folder environment build as it watches
// tasks: wait_environment returns at once (state watching) with a durable
// watch, and when the build ends a new turn wakes the conversation with the
// outcome: status, duration, the new image (ready) or the error (failed), and
// the last 40 log lines; on a linked machine and here. cancel_watch with
// workspace_path stops it (no wake); a build that already ended is answered
// at once without a watch. Threat: the observed dead end, no way to be told
// when a build ends, so the Orchestrator polls (forbidden) or forgets.
// Boundary: Runtime.executeEnvironmentWatch/runEnvironmentWatch/
// cancelEnvironmentWatches, Server.AwaitFolderEnvironmentBuild, the session
// wake (SessionWakeSourceEnvironmentWatch) and V3EnvironmentWatchRecord.
func TestEnvironmentWatchWakesWithTheBuildOutcome(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	builder := newGatedEnvironmentBuilder(100)
	f.remote.s.envBuilder = builder

	// Linked: the home's Orchestrator watches the machine's build (ready).
	home := newEnvWatchRuntime(t, f.home.s, f.homeP, "env-watch-home")
	if _, err := home.call(t, map[string]any{"action": "update_environment", "machine": "box", "workspace_path": f.folder, "environment": map[string]any{"base_image": "debian:12", "setup": []any{"make warm && echo warm-build OK"}}}); err != nil {
		t.Fatal(err)
	}
	builder.awaitStarted(t)
	watching, err := home.call(t, map[string]any{"action": "wait_environment", "machine": "box", "workspace_path": f.folder})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := watching["data"].(map[string]any)
	if watching["state"] != "watching" || data["build_status"] != pebblestore.SandboxEnvironmentBuilding || !strings.Contains(fmt.Sprint(watching["next_action"]), "cancel_watch") || len(home.watches(t)) != 1 {
		t.Fatalf("linked watch: %v", watching)
	}
	if again, err := home.call(t, map[string]any{"action": "wait_environment", "machine": "box", "workspace_path": f.folder}); err != nil || again["data"].(map[string]any)["already_watching"] != true || len(home.watches(t)) != 1 {
		t.Fatalf("second watch of the same build: %v %v", again, err)
	}
	builder.finish(t)
	home.awaitWake(t)
	outcomes := home.outcomes(t)
	if len(outcomes) != 1 {
		t.Fatalf("want one outcome, got %d", len(outcomes))
	}
	text := outcomes[0].Content
	for _, want := range []string{"[Environment build ended: ready]", "on linked machine box", "Duration:", "Image: " + pebblestore.SandboxEnvironmentImagePrefix, "warm-build OK", "line 099"} {
		if !strings.Contains(text, want) {
			t.Fatalf("outcome lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "line 060") || strings.Count(strings.SplitN(text, "```\n", 2)[1], "\n") > tool.EnvironmentWatchLogLines {
		t.Fatalf("outcome carries more than the last %d log lines: %s", tool.EnvironmentWatchLogLines, text)
	}
	if outcomes[0].Metadata["untrusted_content"] != true || outcomes[0].Metadata["machine"] != "box" || outcomes[0].Metadata["environment_status"] != pebblestore.SandboxEnvironmentReady || len(home.watches(t)) != 0 {
		t.Fatalf("outcome metadata or watch left: %v %d", outcomes[0].Metadata, len(home.watches(t)))
	}

	// Here: the machine's own Orchestrator watches a build that fails.
	local := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "env-watch-local")
	if _, err := f.call(t, "", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "append_setup": []any{"warm FAIL"}}); err != nil {
		t.Fatal(err)
	}
	builder.awaitStarted(t)
	if w, err := local.call(t, map[string]any{"action": "wait_environment", "workspace_path": f.folder}); err != nil || w["state"] != "watching" {
		t.Fatalf("local watch: %v %v", w, err)
	}
	builder.finish(t)
	local.awaitWake(t)
	failed := local.outcomes(t)
	if len(failed) != 1 || !strings.Contains(failed[0].Content, "[Environment build ended: failed]") || !strings.Contains(failed[0].Content, "Error: setup command 2 failed") || !strings.Contains(failed[0].Content, "warm-build FAILED") || strings.Contains(failed[0].Content, "Image:") {
		t.Fatalf("failed outcome: %+v", failed)
	}

	// No build running: the last outcome now, nothing watched.
	ended, err := local.call(t, map[string]any{"action": "wait_environment", "workspace_path": f.folder})
	if err != nil || ended["state"] != "ok" || ended["data"].(map[string]any)["watching"] != false || ended["data"].(map[string]any)["build_status"] != pebblestore.SandboxEnvironmentFailed || len(local.watches(t)) != 0 {
		t.Fatalf("watch of an ended build: %v %v", ended, err)
	}

	// cancel_watch stops a watch: no wake when the build ends.
	if _, err := f.call(t, "", tool.FolderEnvironmentRebuild, map[string]any{"workspace_path": f.folder}); err != nil {
		t.Fatal(err)
	}
	builder.awaitStarted(t)
	if _, err := local.call(t, map[string]any{"action": "wait_environment", "workspace_path": f.folder}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := local.call(t, map[string]any{"action": "cancel_watch", "workspace_path": f.folder})
	if err != nil || cancelled["state"] != "cancelled" || len(local.watches(t)) != 0 {
		t.Fatalf("cancel: %v %v", cancelled, err)
	}
	if _, err := local.call(t, map[string]any{"action": "cancel_watch", "workspace_path": f.folder}); err == nil || !strings.Contains(err.Error(), "nothing was cancelled") {
		t.Fatalf("second cancel: %v", err)
	}
	builder.finish(t)
	if _, err := f.call(t, "", tool.FolderEnvironmentGet, map[string]any{"workspace_path": f.folder, "wait_seconds": float64(10)}); err != nil {
		t.Fatal(err)
	}
	select {
	case woke := <-local.controller.enqueued:
		t.Fatalf("a cancelled watch woke the conversation: %s", woke)
	case <-time.After(300 * time.Millisecond):
	}
	if n := len(local.outcomes(t)); n != 1 {
		t.Fatalf("a cancelled watch delivered an outcome: %d outcomes", n)
	}

	// Refusals: a task session, a folder without an environment, no run.
	other := t.TempDir()
	if _, err := local.call(t, map[string]any{"action": "wait_environment", "workspace_path": other}); err == nil || !strings.Contains(err.Error(), "not a registered folder") {
		t.Fatalf("unregistered folder: %v", err)
	}
	task := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "env-watch-task")
	if err := f.remote.s.sessions.Store().CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "env-watch-task-2", AccountScopeID: f.remoteP.AccountScopeID, UserID: f.remoteP.UserID, Metadata: map[string]any{"task_id": "t1"}}, f.remoteP.UserID, f.remoteP.AccountScopeID); err != nil {
		t.Fatal(err)
	}
	task.session = "env-watch-task-2"
	if _, err := task.call(t, map[string]any{"action": "wait_environment", "workspace_path": f.folder}); err == nil || !strings.Contains(err.Error(), "managed by the Orchestrator") {
		t.Fatalf("task session: %v", err)
	}
}

// Purpose: a watch recorded before a daemon restart is followed again at
// start and still delivers: here a build the restart interrupted (the
// store marks it failed) reaches the conversation as failed. Threat: a watch
// lost with the process, leaving the Orchestrator waiting forever. Boundary:
// Runtime.ResumeEnvironmentWatches, V3EnvironmentWatchRecord,
// SandboxEnvironmentStore.InterruptBuilds.
func TestEnvironmentWatchResumesAfterRestart(t *testing.T) {
	f := newLinkedEnvironmentFixture(t)
	builder := newGatedEnvironmentBuilder(1)
	f.remote.s.envBuilder = builder
	if _, err := f.call(t, "", tool.FolderEnvironmentUpdate, map[string]any{"workspace_path": f.folder, "base_image": "debian:12"}); err != nil {
		t.Fatal(err)
	}
	builder.awaitStarted(t)
	first := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "env-watch-restart")
	if _, err := first.call(t, map[string]any{"action": "wait_environment", "workspace_path": f.folder}); err != nil {
		t.Fatal(err)
	}
	// The process stops: its follower ends, the record stays.
	_ = first.runtime.Close()
	if len(first.watches(t)) != 1 {
		t.Fatal("the watch record did not survive its follower")
	}
	// The restart interrupts the build (InterruptBuilds), then resumes the
	// watch in a new runtime.
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { builder.finish(t) }) })
	if err := f.envs.InterruptBuilds(); err != nil {
		t.Fatal(err)
	}
	next := newEnvWatchRuntime(t, f.remote.s, f.remoteP, "env-watch-restart")
	next.runtime.ResumeEnvironmentWatches()
	next.awaitWake(t)
	outcomes := next.outcomes(t)
	if len(outcomes) != 1 || !strings.Contains(outcomes[0].Content, "[Environment build ended: failed]") || !strings.Contains(outcomes[0].Content, "interrupted by a Swarm restart") || len(next.watches(t)) != 0 {
		t.Fatalf("resumed outcome: %+v", outcomes)
	}
	once.Do(func() { builder.finish(t) })
}
