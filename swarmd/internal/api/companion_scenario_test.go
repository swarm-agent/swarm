package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

const (
	scenarioProject  = "research"
	scenarioTask     = "battery"
	scenarioResearch = "Research the best replacement battery for my ThinkPad laptop and compare capacity and price"
	scenarioWifi     = "My laptop wifi keeps dropping every few minutes, can you help me troubleshoot the wireless adapter"
	scenarioRevise   = "Please revise the battery research for my ThinkPad: only replacement batteries under 100 dollars"
	scenarioBackToPC = "Back to the laptop wifi: the wireless adapter keeps dropping again after the driver update"
	scenarioMore     = "One more pass on the battery research please: include the warranty terms"
	scenarioStop     = "Actually stop the battery research, I no longer need it"
	scenarioAfter    = "Is the wireless adapter fix on my laptop still holding after the restart"

	scenarioFirstResult   = "Found three compatible batteries; best value 57Wh at 89 dollars."
	scenarioRevisedResult = "Revised: two batteries under 100 dollars."
)

// companionScenario plays both models of the scenario: the Companion (its
// turns call the native manage_projects actions) and the delegated Finder
// task's own runs, which wait until the test releases them, so the order of
// events is the test's, not the scheduler's. It records what the Companion's
// model saw at each step and every tool answer it got.
type companionScenario struct {
	f         *queuedNoteFixture
	companion string

	mu       sync.Mutex
	seen     map[string][]map[string]any
	outputs  map[string]string
	workers  int
	release  chan struct{}
	watchIDs []string
	errs     []error
}

func (s *companionScenario) record(step string, input []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[step] = input
}

func (s *companionScenario) input(t *testing.T, step string) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	input, ok := s.seen[step]
	if !ok {
		t.Fatalf("the model never ran step %q", step)
	}
	return input
}

func (s *companionScenario) seenStep(step string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[step]
	return ok
}

func (s *companionScenario) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *companionScenario) checkErrs(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) > 0 {
		t.Fatalf("scenario callbacks failed: %v", s.errs)
	}
}

// contextBlock is the Companion context block the run sent: one system item.
func contextBlock(t *testing.T, input []map[string]any) string {
	t.Helper()
	var blocks []string
	for _, item := range input {
		raw, _ := json.Marshal(item["content"])
		if strings.Contains(string(raw), "Companion context bank") {
			blocks = append(blocks, string(raw))
		}
	}
	if len(blocks) != 1 {
		t.Fatalf("context blocks in the request = %d, want 1: %v", len(blocks), input)
	}
	return blocks[0]
}

// projects is one native manage_projects call of the Companion's model.
func (s *companionScenario) projects(id string, args map[string]any) provideriface.Response {
	args["project_id"] = scenarioProject
	raw, _ := json.Marshal(args)
	return provideriface.Response{FunctionCalls: []provideriface.FunctionCall{{CallID: id, Name: "manage_projects", Arguments: string(raw)}}}
}

// answered records a tool answer the Companion's model received and, for a
// wait_tasks watch, its watch id.
func (s *companionScenario) answered(callID, output string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.outputs[callID]; seen || callID == "" {
		return
	}
	s.outputs[callID] = output
	var envelope struct {
		Action string `json:"action"`
		State  string `json:"state"`
		Data   struct {
			WatchID string `json:"watch_id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(output), &envelope) == nil && envelope.Action == "wait_tasks" && envelope.Data.WatchID != "" {
		s.watchIDs = append(s.watchIDs, envelope.Data.WatchID)
	}
}

func (s *companionScenario) output(t *testing.T, callID string) map[string]any {
	t.Helper()
	s.mu.Lock()
	raw := s.outputs[callID]
	var steps, calls []string
	for step := range s.seen {
		steps = append(steps, step)
	}
	for call := range s.outputs {
		calls = append(calls, call)
	}
	s.mu.Unlock()
	var envelope map[string]any
	if raw == "" || json.Unmarshal([]byte(raw), &envelope) != nil {
		var recent []string
		for _, message := range s.f.messages() {
			recent = append(recent, fmt.Sprintf("%s: %.200s", message.Role, message.Content))
		}
		if len(recent) > 10 {
			recent = recent[len(recent)-10:]
		}
		t.Fatalf("no tool answer for %s: %q (steps %v, answers %v); recent:\n%s", callID, raw, steps, calls, strings.Join(recent, "\n"))
	}
	return envelope
}

// lastToolCall is the call id and action of the tool answer the request ends
// with.
func lastToolCall(input []map[string]any) (string, string) {
	if !lastItemIsToolOutput(input) {
		return "", ""
	}
	item := input[len(input)-1]
	var envelope struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(stringMapValue(item, "output")), &envelope)
	return stringMapValue(item, "call_id"), envelope.Action
}

// worker is the delegated Finder task's run: it waits for the test's
// release (or its run being stopped) and reports its result.
func (s *companionScenario) worker(ctx context.Context) (provideriface.Response, error) {
	s.mu.Lock()
	s.workers++
	n := s.workers
	s.mu.Unlock()
	select {
	case <-s.release:
	case <-ctx.Done():
		return provideriface.Response{}, ctx.Err()
	case <-time.After(15 * time.Second):
		return provideriface.Response{}, errors.New("worker was never released")
	}
	text := scenarioFirstResult
	if n > 1 {
		text = scenarioRevisedResult
	}
	return provideriface.Response{Text: text, StopReason: "stop"}, nil
}

func (s *companionScenario) model(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	if !strings.Contains(req.Instructions, "Companion operating contract") {
		return s.worker(ctx)
	}
	last := lastConfirmationTestUserText(req.Input)
	for _, item := range req.Input {
		if stringMapValue(item, "type") == "function_call_output" {
			s.answered(stringMapValue(item, "call_id"), stringMapValue(item, "output"))
		}
	}
	if callID, action := lastToolCall(req.Input); callID != "" {
		switch action {
		case "propose_task", "message_task":
			return s.projects("watch-"+callID, map[string]any{"action": "wait_tasks", "task_ids": []string{scenarioTask}, "until": "finished", "milestones": false}), nil
		case "cancel_watch":
			// No longer followed: stopping the task now brings no outcome.
			return s.projects("stop", map[string]any{"action": "stop_task", "task_id": scenarioTask}), nil
		}
		return provideriface.Response{Text: "Done; I will tell you when it is back.", StopReason: "stop"}, nil
	}
	switch {
	case strings.Contains(last, "Delegated project task outcomes"):
		step := "outcome-1"
		if s.seenStep("outcome-1") {
			step = "outcome-2"
		}
		s.record(step, req.Input)
		return provideriface.Response{Text: "The battery research finished; summary below. Back to your wifi.", StopReason: "stop"}, nil
	case strings.Contains(last, "Research the best replacement battery"):
		s.record("research", req.Input)
		return s.projects("propose", map[string]any{"action": "propose_task", "task_id": scenarioTask, "title": "Battery research", "agent": "finder", "prompt": "Find replacement batteries for a ThinkPad laptop; compare capacity and price.", "client_request_id": "battery-research-1"}), nil
	case strings.Contains(last, "wifi keeps dropping every few minutes"):
		s.record("pc", req.Input)
		return provideriface.Response{Text: "Let's check the wireless adapter power saving setting first.", StopReason: "stop"}, nil
	case strings.Contains(last, "revise the battery research"):
		s.record("revise", req.Input)
		return s.projects("revise", map[string]any{"action": "message_task", "task_id": scenarioTask, "message": "Revise: only replacement batteries under 100 dollars."}), nil
	case strings.Contains(last, "Back to the laptop wifi"):
		s.record("back-to-pc", req.Input)
		return provideriface.Response{Text: "Roll back the adapter driver, then re-check.", StopReason: "stop"}, nil
	case strings.Contains(last, "One more pass on the battery research"):
		s.record("more", req.Input)
		return s.projects("more", map[string]any{"action": "message_task", "task_id": scenarioTask, "message": "One more pass: include the warranty terms."}), nil
	case strings.Contains(last, "stop the battery research"):
		s.record("stop", req.Input)
		return s.projects("cancel", map[string]any{"action": "cancel_watch", "task_ids": []string{scenarioTask}}), nil
	case strings.Contains(last, "still holding after the restart"):
		s.record("after-restart", req.Input)
		return provideriface.Response{Text: "Yes, the adapter has stayed connected.", StopReason: "stop"}, nil
	}
	return provideriface.Response{Text: "ok", StopReason: "stop"}, nil
}

// releaseWorker lets the Finder task's current run report, waits for the
// task to finish, reconciles the watches twice (a task publication and a
// startup pass racing) and waits for the outcome turn of watchID.
func (s *companionScenario) releaseWorker(t *testing.T, watchID string) {
	t.Helper()
	f, account := s.f, testPrincipal().AccountScopeID
	s.release <- struct{}{}
	deadline := time.Now().Add(10 * time.Second)
	for {
		task, ok, err := f.sessions.Store().GetProjectTask(account, scenarioProject, scenarioTask)
		if err == nil && ok && (task.Status == "needs_review" || task.Status == "completed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delegated task never finished: %+v %v %v", task, ok, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.server.reconcileProjectTaskWaits(account, scenarioProject)
	f.server.reconcileProjectTaskWaits(account, scenarioProject)
	f.waitRun(s.companion, pebblestore.SessionWakeRunID(s.companion, watchID+":outcome"), sessionruntime.RunIntentCompleted)
}

func (s *companionScenario) outcomes(t *testing.T, watchID string) []pebblestore.MessageSnapshot {
	t.Helper()
	messages, err := s.f.sessions.ListSessionMessages(s.companion, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []pebblestore.MessageSnapshot
	for _, m := range messages {
		if m.Metadata["task_watch_id"] == watchID && m.Metadata["task_watch_delivery"] == "outcome" {
			out = append(out, m)
		}
	}
	return out
}

func (s *companionScenario) personSeq(t *testing.T, content string) uint64 {
	t.Helper()
	messages, err := s.f.sessions.ListSessionMessages(s.companion, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Role == "user" && m.Content == content {
			return m.GlobalSeq
		}
	}
	t.Fatalf("person message %q not found", content)
	return 0
}

func (s *companionScenario) watch(t *testing.T, i int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.watchIDs) <= i {
		t.Fatalf("watch %d was never registered: %v", i, s.watchIDs)
	}
	return s.watchIDs[i]
}

// Purpose: acceptance of non-voice Companion mode end to end, through the
// Companion's own native tools. Requirement: in one persistent Companion the
// person starts research, which the Companion delegates with manage_projects
// propose_task (a real Finder task whose origin is the Companion) and follows
// with wait_tasks; it gets foreground PC troubleshooting while that runs;
// the research result arrives as its own turn without losing the PC topic
// (the focus stays on the person's message); a revision goes to the same
// task with message_task and is watched again (the earlier research topic is
// retrieved with its source although it left the recent tail); the revised
// result arrives once; the person returns to the PC topic (retrieved by
// source, not by reloading history); another pass is sent and then stopped
// with stop_task and cancel_watch, after which no outcome arrives; after a
// daemon restart over the same store the same Companion has the same task,
// context bank and idle activity. Each watch delivers its outcome exactly
// once even when reconciled again, before and after the restart; a retried
// client_request_id replays instead of starting a turn; every turn carries
// one bounded context block and at most the recent tail; the ordinary chat
// beside it is untouched. Threat: a Companion whose prompt promises
// delegation, watches, revision and cancellation its runtime does not give
// (the reviewed failure: its manage_projects was task-history-only), outcomes
// that never arrive, background results hijacking the conversation,
// duplicate outcome turns on reconcile/restart, topic loss once history
// grows, or a new Companion or lost context after restart. Boundary:
// handleCompanion, the V3 executor and run loop, the provider tool invoker and
// permission gate, Runtime.executeManageProjects (propose_task, wait_tasks,
// message_task, stop_task, cancel_watch) over Server.CreateProjectTask and
// the watch store, reconcileProjectTaskWaits, CompanionV3ContextMessages.
// Narrowest layer: the daemon's HTTP routes, executor, run loop, task runs and
// store over a real Pebble path reopened for the restart, with a scripted
// provider for both the Companion and the Finder task (no live model). The
// task's runs are real; only their model is scripted, and each waits for the
// test's release so the order is deterministic. The person's standing
// "allow manage_projects" rule stands in for their approval of the
// Companion's task calls, as in their app (Start tasks is auto).
func TestCompanionResearchPCHelpRevisionAndRestartScenario(t *testing.T) {
	scenario := &companionScenario{seen: map[string][]map[string]any{}, outputs: map[string]string{}, release: make(chan struct{})}
	f, _ := companionToolFixture(t, func(*queuedNoteFixture, provideriface.Request) provideriface.Response {
		return provideriface.Response{Text: "unused", StopReason: "stop"}
	})
	f.runner.handler = func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return scenario.model(ctx, req)
	}
	scenario.f, scenario.companion = f, f.sessionID
	account, user := testPrincipal().AccountScopeID, testPrincipal().UserID
	store := f.sessions.Store()
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	if _, err := f.server.agentModelSettings.UpdateSystemAgent(ctx, "finder", pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "low"}); err != nil {
		t.Fatal(err)
	}
	chatBefore, _, _ := f.sessions.GetSession(f.createSession("ordinary-chat"))
	// The research project has one registered repository folder.
	f.server.workspace = workspace.NewService(pebblestore.NewWorkspaceStore(store.Underlying()))
	// A follow-up attempt (message_task) starts in a worktree, as on a daemon.
	f.server.SetWorktreeService(worktree.NewService(pebblestore.NewWorktreeStore(store.Underlying()), f.server.workspace, nil))
	for ready := false; !ready; {
		var err error
		if ready, err = store.BackfillRepositoryHistory(100); err != nil {
			t.Fatal(err)
		}
	}
	folder := t.TempDir()
	outboxPinGit(t, folder, "init", "-q", "-b", "dev")
	outboxPinGit(t, folder, "commit", "-q", "--allow-empty", "-m", "start")
	added, err := f.server.workspace.AddForPrincipal(testPrincipal(), folder, "research", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Its canonical local binding, as the daemon records for a registered
	// folder (a task's follow-up attempt session binds to it).
	bindingID := "binding-companion-research"
	if _, err := f.server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID: bindingID, UserID: user, AccountScopeID: account,
		SourceWorkspaceID: added.WorkspaceID, SourceWorkspaceGeneration: added.WorkspaceGeneration, SourceWorkspacePath: folder, SourceWorkspaceName: "research",
		DestinationRuntimeSwarmID: "host-swarm-id", DestinationAuthorityHostSwarmID: "host-swarm-id", DestinationHostSwarmID: "host-swarm-id",
		DestinationRuntimeKind: pebblestore.TopologyRuntimeKindHost, DestinationWorkspacePath: folder, PlacementGeneration: 1, BindingGeneration: 1,
		State: pebblestore.TopologyWorkspaceBindingStateBound, AccessMode: pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind: pebblestore.TopologyWorkspaceBindingMaterializationSource, AttestedByHostSwarmID: "host-swarm-id", Writable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: scenarioProject, AccountID: account, Name: "Research", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: added.WorkspaceID, Path: folder, Role: "primary_code"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.permissions.UpsertRuleForAccount(account, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_projects"}); err != nil {
		t.Fatal(err)
	}

	// 1. Research is delegated with propose_task and watched with wait_tasks.
	companionSay(t, f, "research-1", scenarioResearch)
	scenario.checkErrs(t)
	proposed := scenario.output(t, "propose")
	if proposed["state"] != "ok" {
		t.Fatalf("propose_task: %v", proposed)
	}
	if task, ok, err := store.GetProjectTask(account, scenarioProject, scenarioTask); err != nil || !ok || task.OriginSessionID != scenario.companion || task.Agent != "finder" {
		t.Fatalf("research task: %+v %v %v", task, ok, err)
	}
	if watched := scenario.output(t, "watch-propose"); watched["state"] != "watching" {
		t.Fatalf("wait_tasks: %v", watched)
	}
	firstWatch := scenario.watch(t, 0)
	if activity := f.companionActivity(); activity.Run.Active || len(activity.TaskWatches) != 1 || activity.TaskWatches[0].WatchID != firstWatch {
		t.Fatalf("activity after delegating = %+v, want idle with the research watch", activity)
	}

	// 2. Foreground PC troubleshooting while the research runs.
	companionSay(t, f, "pc-1", scenarioWifi)
	wifiSeq := scenario.personSeq(t, scenarioWifi)
	if block := contextBlock(t, scenario.input(t, "pc")); !strings.Contains(block, fmt.Sprintf("(message #%d)", wifiSeq)) || !strings.Contains(block, "tasks "+scenarioTask) {
		t.Fatalf("PC turn context lacks the wifi focus or the watched research task:\n%s", block)
	}

	// 3. The research result arrives as its own turn; the focus stays on PC.
	scenario.releaseWorker(t, firstWatch)
	if got := scenario.outcomes(t, firstWatch); len(got) != 1 || !strings.Contains(got[0].Content, "best value 57Wh") {
		t.Fatalf("research outcome = %+v, want exactly one with the result", got)
	}
	outcomeBlock := contextBlock(t, scenario.input(t, "outcome-1"))
	if !strings.Contains(outcomeBlock, fmt.Sprintf("(message #%d)", wifiSeq)) {
		t.Fatalf("the research outcome moved the focus off the person's PC topic:\n%s", outcomeBlock)
	}
	if focus, _, _ := store.GetCompanionFocus(account, user); focus.MessageSeq != wifiSeq {
		t.Fatalf("focus after the outcome = %d, want the wifi message %d", focus.MessageSeq, wifiSeq)
	}

	// A long conversation in between: the earlier messages leave the tail.
	for i := 0; i < 45; i++ {
		key := fmt.Sprintf("filler-%d", i)
		if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: scenario.companion, UserID: user, AccountScopeID: account, Kind: pebblestore.V3SessionMutationAppendMessage, ClientRequestID: key, PayloadHash: key, Message: &pebblestore.MessageSnapshot{Role: "assistant", Content: "noted " + key}}); err != nil {
			t.Fatal(err)
		}
	}

	// 4. Revision with message_task on the same task, watched again; the
	// research topic is retrieved by source, not reloaded.
	researchSeq := scenario.personSeq(t, scenarioResearch)
	companionSay(t, f, "revise-1", scenarioRevise)
	scenario.checkErrs(t)
	if revised := scenario.output(t, "revise"); revised["state"] != "ok" {
		t.Fatalf("message_task: %v", revised)
	}
	reviseInput := scenario.input(t, "revise")
	if block := contextBlock(t, reviseInput); !strings.Contains(block, "Relevant earlier context:") || !strings.Contains(block, fmt.Sprintf("message #%d", researchSeq)) {
		t.Fatalf("revision turn did not retrieve the research topic (message #%d):\n%s", researchSeq, block)
	}
	for _, item := range reviseInput {
		raw, _ := json.Marshal(item["content"])
		if strings.Contains(string(raw), scenarioResearch) && !strings.Contains(string(raw), "Companion context bank") {
			t.Fatalf("the original research request was reloaded as history instead of retrieved: %v", item)
		}
	}
	secondWatch := scenario.watch(t, 1)
	if secondWatch == firstWatch {
		t.Fatalf("revision reused the first watch %s", firstWatch)
	}
	scenario.releaseWorker(t, secondWatch)
	if got := scenario.outcomes(t, secondWatch); len(got) != 1 || !strings.Contains(got[0].Content, "Revised: two batteries") {
		t.Fatalf("revised outcome = %+v, want exactly one with the revision", got)
	}
	if got := scenario.outcomes(t, firstWatch); len(got) != 1 {
		t.Fatalf("first outcome redelivered: %d", len(got))
	}

	// 5. Back to the PC topic.
	companionSay(t, f, "back-1", scenarioBackToPC)
	backInput := scenario.input(t, "back-to-pc")
	if block := contextBlock(t, backInput); !strings.Contains(block, fmt.Sprintf("message #%d", wifiSeq)) {
		t.Fatalf("return to PC did not retrieve the earlier wifi topic (message #%d):\n%s", wifiSeq, block)
	}
	backSeq := scenario.personSeq(t, scenarioBackToPC)
	if focus, _, _ := store.GetCompanionFocus(account, user); focus.MessageSeq != backSeq {
		t.Fatalf("focus = %d, want the person's return to PC %d", focus.MessageSeq, backSeq)
	}
	for step, input := range map[string][]map[string]any{"revise": reviseInput, "back-to-pc": backInput} {
		history := 0
		for _, item := range input {
			if item["role"] == "user" || item["role"] == "assistant" {
				history++
			}
		}
		if history > 41 {
			t.Fatalf("%s turn sent %d history items, want at most the recent tail", step, history)
		}
	}

	// 6. Another pass, then the person cancels it: stop_task and cancel_watch.
	companionSay(t, f, "more-1", scenarioMore)
	thirdWatch := scenario.watch(t, 2)
	companionSay(t, f, "stop-1", scenarioStop)
	scenario.checkErrs(t)
	if cancelled := scenario.output(t, "cancel"); cancelled["state"] != "cancelled" {
		t.Fatalf("cancel_watch: %v", cancelled)
	}
	if stopped := scenario.output(t, "stop"); stopped["state"] != "ok" {
		t.Fatalf("stop_task: %v", stopped)
	}
	if watches, err := store.ListV3SessionTaskWatches(scenario.companion); err != nil || len(watches) != 0 {
		t.Fatalf("watches after cancel_watch: %+v %v", watches, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		task, _, _ := store.GetProjectTask(account, scenarioProject, scenarioTask)
		if task.Status != "in_progress" && task.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stopped task still runs: %s", task.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stoppedTask, _, _ := store.GetProjectTask(account, scenarioProject, scenarioTask)
	// The scripted provider call of the stopped pass may still be waiting
	// (a stop does not interrupt a provider call already in flight); let it
	// answer, so its run ends before the restart.
	select {
	case scenario.release <- struct{}{}:
		t.Logf("released the stopped pass's in-flight provider call (task %s)", stoppedTask.Status)
	case <-time.After(500 * time.Millisecond):
		t.Logf("the stopped pass had no provider call waiting (task %s)", stoppedTask.Status)
	}
	f.server.reconcileProjectTaskWaits(account, scenarioProject)
	if got := scenario.outcomes(t, thirdWatch); len(got) != 0 {
		t.Fatalf("a cancelled watch delivered %d outcomes", len(got))
	}
	if watches, err := store.ListV3SessionTaskWatches(scenario.companion); err != nil || len(watches) != 0 {
		t.Fatalf("a watch came back after the stop: %+v %v", watches, err)
	}
	before, _ := json.Marshal(scenario.outcomes(t, firstWatch))
	// Daemon shutdown: in-flight work (task runs, watch followers) ends
	// before the store closes.
	f.server.CancelInFlightRuns()
	if !f.server.WaitForInFlightRuns(10 * time.Second) {
		t.Fatal("runs did not stop on shutdown")
	}

	// 7. Restart over the same store: same Companion, work and context.
	f.close()
	g := newConfirmationFixture(t, f.path, nil)
	for _, slot := range []string{"action", "plan"} {
		if _, err := g.server.agentModelSettings.UpdateSwarmSlot(ctx, slot, pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "low"}); err != nil {
			t.Fatal(err)
		}
	}
	after := &queuedNoteFixture{confirmationFixture: g, sessionID: scenario.companion}
	scenario.f = after
	g.runner.handler = func(ctx context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return scenario.model(ctx, req)
	}
	code, body := g.do(http.MethodPost, CompanionPath, "")
	var reopened struct {
		Created   bool          `json:"created"`
		Companion CompanionView `json:"companion"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &reopened) != nil || reopened.Created || reopened.Companion.SessionID != scenario.companion || reopened.Companion.Generation != 1 {
		t.Fatalf("Companion after restart: %d %s", code, body)
	}
	if activity := after.companionActivity(); activity.Run.Active || len(activity.QueuedInputs) != 0 || len(activity.TaskWatches) != 0 {
		t.Fatalf("activity after restart = %+v, want idle, nothing queued, no open watches", activity)
	}
	if task, ok, _ := g.sessions.Store().GetProjectTask(account, scenarioProject, scenarioTask); !ok || task.OriginSessionID != scenario.companion {
		t.Fatalf("research task lost its Companion origin across restart: %+v", task)
	}
	code, body = g.do(http.MethodGet, CompanionContextPath+"?q="+strings.ReplaceAll("wifi wireless adapter", " ", "+"), "")
	if code != http.StatusOK || !strings.Contains(body, fmt.Sprintf(`"message_seq":%d`, wifiSeq)) {
		t.Fatalf("context bank after restart lacks the wifi topic: %d %s", code, body)
	}
	g.server.reconcileProjectTaskWaits(account, scenarioProject)
	if again, _ := json.Marshal(scenario.outcomes(t, firstWatch)); string(again) != string(before) || len(scenario.outcomes(t, secondWatch)) != 1 || len(scenario.outcomes(t, thirdWatch)) != 0 {
		t.Fatal("an outcome was redelivered (or a cancelled watch delivered) after restart")
	}
	intents := len(after.runIntents())
	raw, _ := json.Marshal(map[string]any{"client_request_id": "back-1", "role": "user", "content": scenarioBackToPC})
	if code, out := g.do(http.MethodPost, "/v3/sessions/"+scenario.companion+"/messages", string(raw)); code != http.StatusOK || len(after.runIntents()) != intents {
		t.Fatalf("retried message after restart started a turn: %d %s", code, out)
	}

	// 8. A new turn after restart keeps the same bounded context.
	g.say(scenario.companion, "after-1", scenarioAfter)
	if block := contextBlock(t, scenario.input(t, "after-restart")); !strings.Contains(block, "Older messages are not loaded") {
		t.Fatalf("turn after restart is not bounded:\n%s", block)
	}

	// The ordinary chat beside the Companion is unchanged.
	chatAfter, ok, err := g.sessions.GetSession(chatBefore.ID)
	if err != nil || !ok || sessionsV3IsCompanion(chatAfter) || chatAfter.Metadata["navigation_hidden"] == true || chatAfter.Title != chatBefore.Title {
		t.Fatalf("ordinary chat changed: %+v %v %v", chatAfter.Metadata, ok, err)
	}
}
