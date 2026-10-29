package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Purpose: initial and continued V3 requests must preserve assignment/rules while
// advertising only each regular agent's resolved tools and lifecycle authority
// (Designer: swarm_design, never artifact authoring, write/edit or Bash).
// Threat: the second request builder bypasses role scoping or regains parent
// tools on continuation. Owners: resolveSessionV3Runtime, ComposeRuntimeInstructions
// and ScopeDurableRunStateInstructions. This real executor/temporary-store layer
// with a recording provider is the narrowest proof across those boundaries; it
// proves request assembly, not model behavior or provider-backed task completion.
func TestHeadlessV3RequestsPreserveRoleAuthorityAcrossContinuation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	t.Setenv("SWARM_CONFIG", "")
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "requests"), nil)
	defer f.close()
	// The fixture's canonical account settings resolve system roles to codex.
	// Register a recorder under that ID; no external provider is contacted.
	f.runner.id = "codex"
	f.server.providers.RegisterRunner(f.runner)
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "Request recorded; no work or validation performed.", StopReason: "stop"}, nil
	}
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, tool.NewRuntime(1), f.permissions, f.server.agents, discovery.NewServiceWithUserHome(home), nil)
	for _, role := range []string{"swarm", "system-coder", "system-finder", "system-designer"} {
		t.Run(role, func(t *testing.T) {
			f.t = t
			workspace := t.TempDir()
			const rule = "SYNTHETIC-REPOSITORY-RULE-keep-acceptance"
			if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(rule+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			binding := seedSessionsV3PrimaryAuthority(t, f.server, workspace)
			raw, _ := json.Marshal(map[string]any{"client_request_id": "role-" + role, "workspace_path": workspace, "workspace_name": filepath.Base(workspace), "swarm_id": "host-swarm-id", "workspace_binding_id": binding, "target_kind": "host", "target_relationship": "self", "title": role, "mode": sessionruntime.ModeAuto, "agent_name": role, "preference": pebblestore.ModelPreference{Provider: "test-provider", Model: "test-model"}})
			code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
			if code != http.StatusOK {
				t.Fatalf("create: %d %s", code, body)
			}
			var created struct {
				Session pebblestore.SessionSnapshot `json:"session"`
			}
			if err := json.Unmarshal([]byte(body), &created); err != nil {
				t.Fatal(err)
			}
			for turn, brief := range []string{"SYNTHETIC-BRIEF preserve exact outputs and acceptance criteria", "SYNTHETIC-CONTINUATION retain the original acceptance criteria"} {
				requestID := role + "-initial"
				if turn == 1 {
					requestID = role + "-continue"
				}
				f.say(created.Session.ID, requestID, brief)
				f.runner.mu.Lock()
				req := f.runner.lastRequest
				f.runner.mu.Unlock()
				// The primary Swarm uses its canonical account action slot. Direct
				// specialist sessions preserve their explicit preference snapshot.
				wantModel, wantThinking := "test-model", "xhigh"
				if role == "swarm" {
					wantModel, wantThinking = "action", "high"
				}
				if req.Model != wantModel || req.Thinking != wantThinking {
					t.Fatalf("account/explicit preference selection changed: model=%q thinking=%q", req.Model, req.Thinking)
				}
				if !strings.Contains(req.Instructions, rule) {
					t.Fatal("repository rules missing")
				}
				input, _ := json.Marshal(req.Input)
				if !strings.Contains(string(input), "SYNTHETIC-BRIEF") || !strings.Contains(string(input), brief) {
					t.Fatal("brief/continuation context lost")
				}
				names := map[string]bool{}
				for _, def := range req.Tools {
					names[def.Name] = true
					if len(def.Parameters) == 0 {
						t.Fatalf("missing schema: %s", def.Name)
					}
				}
				for _, name := range []string{"manage_workers", "manage_automation", "manage_design", "plan_manage"} {
					if names[name] {
						t.Fatalf("unexpected capability %s", name)
					}
				}
				if strings.Contains(req.Instructions, "Use manage_design") {
					t.Fatal("unavailable design delegation advertised")
				}
				if role == "swarm" {
					if !names["task"] || !names["bash"] {
						t.Fatalf("parent delegation/validation tools missing: %v", names)
					}
				} else {
					if names["task"] {
						t.Fatal("specialist gained parent delegation")
					}
					if !strings.Contains(req.Instructions, "checkpoint transitions belong to the authorized owner") {
						t.Fatal("specialist ownership guidance missing")
					}
					if role == "system-coder" && (!names["bash"] || names["ask_user"] || !names["task_progress"]) {
						t.Fatalf("regular coder contract changed (want scoped bash and task_progress, no ask_user): %v", names)
					}
					if role == "system-finder" && (names["find"] || names["write"] || names["edit"]) {
						t.Fatalf("Finder gained unsupported tools: %v", names)
					}
					if role == "system-designer" && (!names[tool.SwarmDesignToolName] || names["artifact_v3_author"] || names["manage_artifact"] || names["write"] || names["edit"] || names["bash"]) {
						t.Fatalf("Designer schema boundary changed (want swarm_design, no artifact authoring, write/edit or bash): %v", names)
					}
				}
				t.Logf("%s turn=%d instructions_bytes=%d tools=%d", role, turn, len(req.Instructions), len(req.Tools))
			}
			if _, exists, err := f.sessions.GetActivePlan(created.Session.ID); err != nil || exists {
				t.Fatalf("ordinary request created a plan: exists=%v err=%v", exists, err)
			}
		})
	}
}

// Purpose: the actual V3 provider loop's final MaxSteps request must remove tool
// invocation guidance with its tools while retaining repository rules and brief.
// Threat: resolving instructions before the last-step filter leaves stale parent
// delegation/checkpoint instructions in a request that cannot execute them.
// Owners: runProviderToolLoop and composeSessionV3ResolvedInstructions. An isolated
// routed session and recording runner exercise production request assembly; this
// is not provider-backed execution, and the ephemeral limit changes no profile.
func TestHeadlessV3FinalMaxStepsRequestHasNoToolGuidance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("STATE_DIRECTORY", t.TempDir())
	t.Setenv("SWARM_CONFIG", "")
	f := newConfirmationFixture(t, filepath.Join(t.TempDir(), "final-request"), nil)
	defer f.close()
	f.runner.id = "codex"
	f.server.providers.RegisterRunner(f.runner)
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "Recorded final request; no work performed.", StopReason: "stop"}, nil
	}
	f.server.runner = runruntime.NewService(f.sessions, f.server.model, f.server.providers, tool.NewRuntime(1), f.permissions, f.server.agents, discovery.NewServiceWithUserHome(home), nil)
	workspace := t.TempDir()
	const rule = "SYNTHETIC-FINAL-REQUEST-RULE preserve acceptance"
	const brief = "SYNTHETIC-FINAL-REQUEST-BRIEF preserve exact outputs"
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte(rule+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binding := seedSessionsV3PrimaryAuthority(t, f.server, workspace)
	raw, _ := json.Marshal(map[string]any{"client_request_id": "final-tools-create", "workspace_path": workspace, "workspace_name": filepath.Base(workspace), "swarm_id": "host-swarm-id", "workspace_binding_id": binding, "target_kind": "host", "target_relationship": "self", "title": "Final tools", "mode": sessionruntime.ModeAuto, "agent_name": "swarm"})
	code, body := f.do(http.MethodPost, "/v3/sessions", string(raw))
	if code != http.StatusOK {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	f.say(created.Session.ID, "final-tools-initial", brief)
	f.runner.mu.Lock()
	initial := f.runner.lastRequest
	f.runner.mu.Unlock()
	if len(initial.Tools) == 0 || !strings.Contains(initial.Instructions, "Use task to deploy Coder, Finder and Designer") {
		t.Fatal("positive control lacks parent tool authority/guidance")
	}
	executor := f.server.v3SessionExecutor
	job := sessionV3ExecutorJob{Principal: testPrincipal(), SessionID: created.Session.ID, RunID: "synthetic-final-tools-loop"}
	prior, found, err := f.sessions.GetSessionRunIntent(created.Session.ID, stableSessionsV3PrimaryRunID(created.Session.ID, "final-tools-initial"))
	if err != nil || !found {
		t.Fatalf("initial run intent: found=%t error=%v", found, err)
	}
	job.EpochID = prior.EpochID
	// This test is the executor for its synthetic run. Claim the run as in
	// flight before marking it pending. Otherwise the initial run's completion
	// refill (refillPendingBacklog) can pick the pending run up and execute it
	// concurrently. Two sinks then record its provider first event with
	// different timestamps: a timing-dependent idempotency conflict.
	runKey := sessionV3ExecutorRunKey(job.SessionID, job.RunID)
	executor.mu.Lock()
	executor.inFlightRuns[runKey] = true
	executor.mu.Unlock()
	defer func() {
		executor.mu.Lock()
		delete(executor.inFlightRuns, runKey)
		executor.mu.Unlock()
	}()
	if _, err := executor.recordRunStatus(job, sessionruntime.RunIntentPendingExecutor, "", "session.assistant.queued"); err != nil {
		t.Fatal(err)
	}
	resolved, err := executor.resolveSessionV3Runtime(job)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Limits.MaxSteps = 1
	base, err := executor.sessionV3ProviderBaseRequest(job, resolved, initial.Input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newSessionV3DurableProgressSink(executor, job, cancel)
	_, loopErr := executor.runProviderToolLoop(ctx, job, resolved, f.runner, base, sink, nil)
	closeErr := sink.CloseAndFlush(context.Background())
	if loopErr != nil || closeErr != nil {
		t.Fatalf("final provider loop: loop=%v flush=%v", loopErr, closeErr)
	}
	f.runner.mu.Lock()
	final := f.runner.lastRequest
	f.runner.mu.Unlock()
	if len(final.Tools) != 0 || final.ToolChoice != "none" || final.ToolInvoker != nil {
		t.Fatal("final MaxSteps request retains executable tools")
	}
	for _, stale := range []string{"Use task to deploy", "Use task to launch", "pass that tuple as bash", "call plan_manage", "call exit_plan_mode", "start_session_checkpoint", "Delegation and validation ownership:"} {
		if strings.Contains(final.Instructions, stale) {
			t.Fatalf("final request retains unavailable invocation guidance %q", stale)
		}
	}
	if !strings.Contains(final.Instructions, rule) {
		t.Fatal("final request dropped repository rule")
	}
	input, _ := json.Marshal(final.Input)
	if !strings.Contains(string(input), brief) {
		t.Fatal("final request dropped brief")
	}
	if len(resolved.Tools) == 0 || len(base.Tools) == 0 {
		t.Fatal("final request filter mutated original runtime authority")
	}
	t.Logf("actual V3 final request: tools=%d choice=%s instructions_bytes=%d; original tools=%d", len(final.Tools), final.ToolChoice, len(final.Instructions), len(base.Tools))
}
