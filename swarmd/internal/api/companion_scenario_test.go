package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const (
	scenarioProject  = "research"
	scenarioTask     = "battery"
	scenarioResearch = "Research the best replacement battery for my ThinkPad laptop and compare capacity and price"
	scenarioWifi     = "My laptop wifi keeps dropping every few minutes, can you help me troubleshoot the wireless adapter"
	scenarioRevise   = "Please revise the battery research for my ThinkPad: only replacement batteries under 100 dollars"
	scenarioBackToPC = "Back to the laptop wifi: the wireless adapter keeps dropping again after the driver update"
	scenarioAfter    = "Is the wireless adapter fix on my laptop still holding after the restart"
)

// companionScenario records what the scripted model saw at each step and the
// errors its callbacks hit (they run on the run's goroutine).
type companionScenario struct {
	f         *queuedNoteFixture
	companion string

	mu       sync.Mutex
	seen     map[string][]map[string]any
	watchIDs []string
	origins  []string
	errs     []error
}

func (s *companionScenario) record(step string, input []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[step] = input
}

func (s *companionScenario) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
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

// delegate is what the Companion's manage_projects create_task and
// wait_tasks do in the running turn: the task records the origin the daemon
// captures for the executing conversation (projectTaskOrigin) and the watch is
// the same durable mutation the native tool commits, then followed.
func (s *companionScenario) delegate(runID string, reopen bool) {
	f, account, user := s.f, testPrincipal().AccountScopeID, testPrincipal().UserID
	store := f.sessions.Store()
	ctx := tool.WithVideoRunContext(context.Background(), tool.VideoRunContext{SessionID: s.companion, RunID: runID})
	origin, err := f.server.projectTaskOrigin(ctx, testPrincipal(), scenarioProject)
	if err != nil {
		s.fail(fmt.Errorf("task origin: %w", err))
		return
	}
	s.mu.Lock()
	s.origins = append(s.origins, origin)
	s.mu.Unlock()
	if reopen {
		_, err = store.UpdateProjectTask(account, scenarioProject, scenarioTask, func(t *pebblestore.ProjectTaskRecord) error {
			t.Status = "in_progress"
			t.Attempts = append(t.Attempts, pebblestore.ProjectTaskAttempt{ID: "a2", SessionID: "battery-worker", Role: "coder", Status: "in_progress"})
			t.ActiveAttemptID = "a2"
			return nil
		})
	} else {
		err = store.PutProjectTask(account, &pebblestore.ProjectTaskRecord{
			ID: scenarioTask, ProjectID: scenarioProject, AccountID: account, Title: "Battery research", Agent: "coder", Status: "in_progress",
			SessionID: "battery-worker", OriginSessionID: origin, ActiveAttemptID: "a1",
			Attempts: []pebblestore.ProjectTaskAttempt{{ID: "a1", SessionID: "battery-worker", Role: "coder", Status: "in_progress"}},
		})
	}
	if err != nil {
		s.fail(fmt.Errorf("task: %w", err))
		return
	}
	off := false
	op := &pebblestore.V3ProjectTaskWaitMutation{RunID: runID, ProjectID: scenarioProject, TaskIDs: []string{scenarioTask}, Until: pebblestore.ProjectTaskWaitUntilFinished, Milestones: &off}
	key := "task-watch:" + runID
	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID: s.companion, UserID: user, AccountScopeID: account,
		Kind: pebblestore.V3SessionMutationAppendMessage, EventType: "session.task_watch.started",
		ClientRequestID: key, PayloadHash: key, TaskWait: op,
	}); err != nil {
		s.fail(fmt.Errorf("watch: %w", err))
		return
	}
	s.mu.Lock()
	s.watchIDs = append(s.watchIDs, op.RegisteredWatchID)
	s.mu.Unlock()
	f.server.FollowTaskWatch(s.companion, op.RegisteredWatchID)
}

func (s *companionScenario) model(f *queuedNoteFixture, req provideriface.Request) provideriface.Response {
	last := lastConfirmationTestUserText(req.Input)
	switch {
	case strings.Contains(last, "Delegated project task outcomes"):
		step := "outcome-1"
		if s.seenStep("outcome-1") {
			step = "outcome-2"
		}
		s.record(step, req.Input)
		return provideriface.Response{Text: "The battery research finished; summary below. Back to your wifi.", StopReason: "stop"}
	case strings.Contains(last, "Research the best replacement battery"):
		s.record("research", req.Input)
		s.delegate(stableSessionsV3PrimaryRunID(s.companion, "research-1"), false)
		return provideriface.Response{Text: "I started the battery research as a task and will report when it is done.", StopReason: "stop"}
	case strings.Contains(last, "wifi keeps dropping every few minutes"):
		s.record("pc", req.Input)
		return provideriface.Response{Text: "Let's check the wireless adapter power saving setting first.", StopReason: "stop"}
	case strings.Contains(last, "revise the battery research"):
		s.record("revise", req.Input)
		s.delegate(stableSessionsV3PrimaryRunID(s.companion, "revise-1"), true)
		return provideriface.Response{Text: "I sent the revision to the research task.", StopReason: "stop"}
	case strings.Contains(last, "Back to the laptop wifi"):
		s.record("back-to-pc", req.Input)
		return provideriface.Response{Text: "Roll back the adapter driver, then re-check.", StopReason: "stop"}
	case strings.Contains(last, "still holding after the restart"):
		s.record("after-restart", req.Input)
		return provideriface.Response{Text: "Yes, the adapter has stayed connected.", StopReason: "stop"}
	}
	return provideriface.Response{Text: "ok", StopReason: "stop"}
}

func (s *companionScenario) seenStep(step string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[step]
	return ok
}

// settle finishes the research task's active attempt and lets the daemon's
// own reconciliation fire the watch, then waits for the outcome turn.
func (s *companionScenario) settle(t *testing.T, watchID, summary string) {
	t.Helper()
	f, account := s.f, testPrincipal().AccountScopeID
	if _, err := f.sessions.Store().UpdateProjectTask(account, scenarioProject, scenarioTask, func(task *pebblestore.ProjectTaskRecord) error {
		task.Status = "needs_review"
		for i := range task.Attempts {
			if task.Attempts[i].ID == task.ActiveAttemptID {
				task.Attempts[i].Status, task.Attempts[i].Summary = "needs_review", summary
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reconciling twice (a task publication and a startup pass racing) must
	// still deliver the outcome once.
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

func (s *companionScenario) checkErrs(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) > 0 {
		t.Fatalf("scenario callbacks failed: %v", s.errs)
	}
}

// Purpose: acceptance of non-voice Companion mode end to end. Requirement:
// in one persistent Companion the person starts research (delegated as a
// project task whose origin is the Companion and which the Companion
// watches), gets foreground PC troubleshooting while that runs, receives the
// research result as its own turn without losing the PC topic (the focus
// stays on the person's message), asks for a revision (the earlier research
// topic is retrieved with its source although it left the recent tail), gets
// the revised result, returns to the PC topic (retrieved by source, not by
// reloading history), and after a daemon restart over the same store reopens
// the same Companion with the same work, context bank and idle activity.
// Each watch delivers its outcome exactly once even when reconciled again,
// before and after the restart; a retried client_request_id replays instead
// of starting a turn; every turn carries one bounded context block and at
// most the recent tail; the ordinary chat beside it is untouched. Threat: a
// Companion that cannot follow work it delegates (outcomes never arrive),
// background results hijacking the conversation, duplicate outcome turns on
// reconcile/restart, topic loss once history grows, a new Companion or lost
// context after restart. Boundary: handleCompanion, projectTaskOrigin,
// prepareProjectTaskWait/prepareProjectTaskWatchWake via
// FollowTaskWatch/reconcileProjectTaskWaits/publishProjectTaskWake, the V3
// executor with CompanionV3ContextMessages, the context bank route. Narrowest
// layer: the daemon's HTTP routes, executor and store over a real Pebble
// path reopened for the restart, with a scripted provider (no live model) and
// the research task's worker replaced by direct task-record transitions.
func TestCompanionResearchPCHelpRevisionAndRestartScenario(t *testing.T) {
	scenario := &companionScenario{seen: map[string][]map[string]any{}}
	f := newCompanionRunFixture(t, scenario.model)
	scenario.f, scenario.companion = f, f.sessionID
	account, user := testPrincipal().AccountScopeID, testPrincipal().UserID
	store := f.sessions.Store()
	if err := store.PutProject(account, &pebblestore.ProjectRecord{ID: scenarioProject, Name: "Research"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID: "battery-worker", UserID: user, AccountScopeID: account, Kind: pebblestore.V3SessionMutationCreateSession, ClientRequestID: "create-battery-worker", PayloadHash: "create-battery-worker", Session: &pebblestore.SessionSnapshot{ID: "battery-worker", Mode: "auto", Metadata: map[string]any{"project_id": scenarioProject, "task_id": scenarioTask}}}); err != nil {
		t.Fatal(err)
	}
	chatBefore, _, _ := f.sessions.GetSession(f.createSession("ordinary-chat"))

	// 1. Research starts as delegated work the Companion watches.
	f.say(scenario.companion, "research-1", scenarioResearch)
	scenario.checkErrs(t)
	if len(scenario.origins) != 1 || scenario.origins[0] != scenario.companion {
		t.Fatalf("task origin = %v, want the Companion %s", scenario.origins, scenario.companion)
	}
	if task, ok, err := store.GetProjectTask(account, scenarioProject, scenarioTask); err != nil || !ok || task.OriginSessionID != scenario.companion {
		t.Fatalf("research task origin: %+v %v %v", task, ok, err)
	}
	firstWatch := scenario.watchIDs[0]
	if activity := f.companionActivity(); activity.Run.Active || len(activity.TaskWatches) != 1 || activity.TaskWatches[0].WatchID != firstWatch {
		t.Fatalf("activity after delegating = %+v, want idle with the research watch", activity)
	}

	// 2. Foreground PC troubleshooting while the research runs.
	f.say(scenario.companion, "pc-1", scenarioWifi)
	wifiSeq := scenario.personSeq(t, scenarioWifi)
	if block := contextBlock(t, scenario.input(t, "pc")); !strings.Contains(block, fmt.Sprintf("(message #%d)", wifiSeq)) || !strings.Contains(block, "tasks "+scenarioTask) {
		t.Fatalf("PC turn context lacks the wifi focus or the watched research task:\n%s", block)
	}

	// 3. The research result arrives as its own turn; the focus stays on PC.
	scenario.settle(t, firstWatch, "Found three compatible batteries; best value 57Wh at 89 dollars.")
	if got := scenario.outcomes(t, firstWatch); len(got) != 1 {
		t.Fatalf("research outcome delivered %d times, want once", len(got))
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

	// 4. Revision: the research topic is retrieved by source, not reloaded.
	researchSeq := scenario.personSeq(t, scenarioResearch)
	f.say(scenario.companion, "revise-1", scenarioRevise)
	scenario.checkErrs(t)
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
	if len(scenario.watchIDs) != 2 || scenario.watchIDs[1] == firstWatch || len(scenario.origins) != 2 || scenario.origins[1] != scenario.companion {
		t.Fatalf("revision watch/origin = %v %v", scenario.watchIDs, scenario.origins)
	}
	secondWatch := scenario.watchIDs[1]
	scenario.settle(t, secondWatch, "Revised: two batteries under 100 dollars.")
	if got := scenario.outcomes(t, secondWatch); len(got) != 1 || !strings.Contains(got[0].Content, "Revised: two batteries") {
		t.Fatalf("revised outcome = %+v, want exactly one with the revision", got)
	}
	if got := scenario.outcomes(t, firstWatch); len(got) != 1 {
		t.Fatalf("first outcome redelivered: %d", len(got))
	}

	// 5. Back to the PC topic.
	f.say(scenario.companion, "back-1", scenarioBackToPC)
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
	before, _ := json.Marshal(scenario.outcomes(t, firstWatch))

	// 6. Restart over the same store: same Companion, work and context.
	f.close()
	g := newConfirmationFixture(t, f.path, nil)
	ctx := identity.ContextWithPrincipal(context.Background(), testPrincipal())
	for _, slot := range []string{"action", "plan"} {
		if _, err := g.server.agentModelSettings.UpdateSwarmSlot(ctx, slot, pebblestore.AgentModelAssignment{Provider: "test-provider", Model: "test-model", Thinking: "low"}); err != nil {
			t.Fatal(err)
		}
	}
	after := &queuedNoteFixture{confirmationFixture: g, sessionID: scenario.companion}
	scenario.f = after
	g.runner.handler = func(_ context.Context, req provideriface.Request, _ func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return scenario.model(after, req), nil
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
	if again, _ := json.Marshal(scenario.outcomes(t, firstWatch)); string(again) != string(before) || len(scenario.outcomes(t, secondWatch)) != 1 {
		t.Fatal("an outcome was redelivered after restart")
	}
	intents := len(after.runIntents())
	raw, _ := json.Marshal(map[string]any{"client_request_id": "back-1", "role": "user", "content": scenarioBackToPC})
	if code, out := g.do(http.MethodPost, "/v3/sessions/"+scenario.companion+"/messages", string(raw)); code != http.StatusOK || len(after.runIntents()) != intents {
		t.Fatalf("retried message after restart started a turn: %d %s", code, out)
	}

	// 7. A new turn after restart keeps the same bounded context.
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
