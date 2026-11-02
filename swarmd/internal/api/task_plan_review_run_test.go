package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/auth"
	"swarm/packages/swarmd/internal/identity"
	modelruntime "swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
)

// equipRemoteRunner gives the pair's machine what a real acceptance needs to
// start its run: the account's canonical agent model settings, the agent and
// model services, and the V3 executor over a run service whose only provider
// is a recording one. Nothing in the approve path is replaced: the machine's
// own approve route, plan acceptance commit, plan lifecycle and executor run
// as in a daemon; only the model's answer is scripted, so the provider calls
// a started run makes are counted.
// remoteRunner is the machine's scripted provider: called closes on its
// first call.
type remoteRunner struct {
	*sessionsV3RecordingProviderRunner
	called chan struct{}
}

func (p *planReviewPair) equipRemoteRunner(t *testing.T) remoteRunner {
	t.Helper()
	remote := p.remote.s
	db := remote.sessions.Store().Underlying()
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	models := modelruntime.NewService(pebblestore.NewModelStore(db), events, modelruntime.NewCatalogService(pebblestore.NewModelCatalogStore(db)))
	if err := models.EnsureBootDefaults(); err != nil {
		t.Fatal(err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(db), events)
	if err := agents.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	perms := permission.NewService(pebblestore.NewPermissionStore(db), events, nil)
	perms.SetSessionResolver(remote.sessions)
	called := make(chan struct{})
	var first sync.Once
	recorder := &sessionsV3RecordingProviderRunner{handler: func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		first.Do(func() { close(called) })
		return provideriface.Response{Text: "Checkpoint done.", StopReason: "stop"}, nil
	}}
	providers := registry.New()
	providers.RegisterRunner(recorder)
	settingsStore := pebblestore.NewAgentModelSettingsStore(db)
	record := testAgentModelSettingsRecord(p.remote.account)
	record.Swarm.Action = pebblestore.AgentModelAssignment{Provider: recorder.ID(), Model: "test-model", Thinking: "low"}
	record.Swarm.Plan = record.Swarm.Action
	if _, err := settingsStore.PutForAccount(record); err != nil {
		t.Fatal(err)
	}
	settings := agentmodelsettings.NewService(settingsStore)
	remote.agents, remote.model, remote.providers, remote.perm = agents, models, providers, perms
	remote.SetAgentModelSettingsService(settings)
	run := runruntime.NewService(remote.sessions, models, providers, tool.NewRuntime(1), perms, agents, nil, nil)
	run.SetAgentModelSettingsService(settings)
	remote.runner = run
	exec := newSessionV3Executor(remote)
	exec.startDelay = 0
	remote.v3SessionExecutor = exec
	t.Cleanup(func() {
		remote.CancelInFlightRuns()
		remote.WaitForInFlightRuns(5 * time.Second)
	})
	return remoteRunner{sessionsV3RecordingProviderRunner: recorder, called: called}
}

// restartHome closes the home's stores and opens them again under a new
// Server, as a daemon restart does: every in-memory service, cache and link
// client is new; only what Swarm wrote durably remains.
func (p *planReviewPair) restartHome(t *testing.T) {
	t.Helper()
	old := p.home.s
	sessionsPath, fleetPath := old.sessions.Store().Underlying().Path(), p.home.fleetDB.Path()
	old.CloseFleetListener()
	old.CancelInFlightRuns()
	old.WaitForInFlightRuns(5 * time.Second)
	if err := old.sessions.Store().Underlying().Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.home.fleetDB.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := pebblestore.Open(sessionsPath)
	if err != nil {
		t.Fatal(err)
	}
	fdb, err := pebblestore.Open(fleetPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(auth.NewService(pebblestore.NewAuthStore(db), events), nil, nil, nil, sessionruntime.NewService(pebblestore.NewSessionStore(db), nil), nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	s.SetSwarmStore(pebblestore.NewSwarmStore(fdb))
	s.SetFleet(pebblestore.NewFleetStore(fdb), pebblestore.NewAuthStore(fdb))
	t.Cleanup(func() {
		s.CloseFleetListener()
		s.CancelInFlightRuns()
		s.WaitForInFlightRuns(5 * time.Second)
		_ = fdb.Close()
		_ = db.Close()
	})
	p.home.s, p.home.fleetDB = s, fdb
}

// linkedRecords reads the home's records of what a conversation sent to
// linked machines: the console's origin feed (GET /v3/fleet/linked-tasks).
func (p *planReviewPair) linkedRecords(t *testing.T, sessionID string) []map[string]any {
	t.Helper()
	req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodGet, "/v3/fleet/linked-tasks?session_id="+sessionID, nil), p.home.user, p.home.account)
	w := httptest.NewRecorder()
	p.home.s.handleFleetLinkedTasks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("linked tasks: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		LinkedTasks []map[string]any `json:"linked_tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.LinkedTasks
}

// planRuns lists the task session's run intents on the machine.
func (p *planReviewPair) planRuns(t *testing.T, sessionID string) []pebblestore.V3SessionRunIntent {
	t.Helper()
	runs, err := p.remote.s.sessions.Store().ListRunIntents(sessionID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// providerReached reports whether the started run reached the provider: it
// waits for the provider's first call, with a safety bound only.
func providerReached(runner remoteRunner) bool {
	select {
	case <-runner.called:
		return true
	case <-time.After(30 * time.Second):
		return false
	}
}

// within waits for ch, failing after a safety bound.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never happened", what)
		var zero T
		return zero
	}
}

func (p *planReviewPair) decide(t *testing.T, binding map[string]any, decision, feedback string) (int, map[string]any, tool.ProjectTaskPlanReview) {
	t.Helper()
	body := map[string]any{"machine": "box", "project_id": planReviewProject, "task_id": planReviewTask, "decision": decision}
	if binding != nil {
		body["plan_binding"] = binding
	}
	if feedback != "" {
		body["feedback"] = feedback
	}
	return p.route(t, p.home.user, http.MethodPost, taskPlanReviewDecisionPath, body)
}

var planReviewExact = map[string]any{"session_id": planReviewSession, "plan_id": planReviewPlan, "definition_revision": 2}

// Purpose: the person's explicit acceptance of the exact version they read,
// made from the home for a plan on a linked machine, starts the machine's
// plan run exactly once, and the home's durable records and review follow
// it. Requirements: (1) the exact binding through the home's decision route,
// the link and the machine's own approve route commits the plan acceptance
// and starts one run that reaches the provider; (2) the answer names the
// version decided on and the task as read after it (in progress, nothing to
// decide); (3) a double click (two concurrent identical decisions) and a
// later replay start no second run and answer 409 with the current review;
// (4) the home's linked-task record for the sending conversation shows the
// task started. Threat: an acceptance that never starts, starts twice, or
// is reported as waiting after it started. Owners: Server.DecideTaskPlan,
// taskPlanDecisionFailure, Server.CallFleetProject, Server.ApproveProjectTask,
// the session executor. Narrowest layer: two in-process daemons over the real
// pinned link with the machine's real executor; only the model is scripted.
func TestTaskPlanReviewLinkedAcceptStartsOneRun(t *testing.T) {
	p := newPlanReviewPair(t)
	recorder := p.equipRemoteRunner(t)
	if status, out, review := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil); status != http.StatusOK || review.Decision != tool.ProjectTaskReviewAcceptPlan {
		t.Fatalf("read: %d %v", status, out)
	}

	type answer struct {
		status int
		out    map[string]any
		review tool.ProjectTaskPlanReview
	}
	answers := make([]answer, 2)
	var wg sync.WaitGroup
	for i := range answers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, out, review := p.decide(t, planReviewExact, "accept", "")
			answers[i] = answer{status, out, review}
		}(i)
	}
	wg.Wait()
	accepted, refused := 0, 0
	for _, a := range answers {
		switch a.status {
		case http.StatusOK:
			accepted++
			decided, _ := a.out["decided"].(map[string]any)
			binding, _ := decided["plan_binding"].(map[string]any)
			if a.out["ok"] != true || a.out["review_current"] != true || decided["machine"] != "box" || binding["plan_id"] != planReviewPlan || binding["definition_revision"] != float64(2) {
				t.Fatalf("accepted answer: %v", a.out)
			}
			// Read after the decision: started (the scripted run may already
			// have ended), never waiting.
			if a.review.RunStatus == "pending_approval" || a.review.Decision != tool.ProjectTaskReviewNone {
				t.Fatalf("accepted but the review after it says %s/%s", a.review.RunStatus, a.review.Decision)
			}
		case http.StatusConflict:
			refused++
			if a.out["review_current"] != true || a.review.Decision != tool.ProjectTaskReviewNone {
				t.Fatalf("the second click's refusal: %v", a.out)
			}
		default:
			t.Fatalf("decision: %d %v", a.status, a.out)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatalf("two clicks: %d accepted, %d refused", accepted, refused)
	}

	db := p.remote.s.sessions.Store()
	task, _, _ := db.GetProjectTask(p.remote.account, planReviewProject, planReviewTask)
	plan, _, _ := db.GetPlan(planReviewSession, planReviewPlan)
	if task.Status == "pending_approval" || plan.ApprovalState != "approved" {
		t.Fatalf("machine after accept: task %s plan %s", task.Status, plan.ApprovalState)
	}
	runs := p.planRuns(t, planReviewSession)
	if len(runs) != 1 || runs[0].PlanID != planReviewPlan {
		t.Fatalf("runs after accept: %+v", runs)
	}
	if !providerReached(recorder) {
		t.Fatalf("the started run never reached the provider; run %+v", p.planRuns(t, planReviewSession))
	}

	// A replay of the same decision later: refused, still one run.
	if status, out, current := p.decide(t, planReviewExact, "accept", ""); status != http.StatusConflict || out["review_current"] != true || current.RunStatus == "pending_approval" || current.Decision != tool.ProjectTaskReviewNone {
		t.Fatalf("replay: %d %v", status, out)
	}
	if runs := p.planRuns(t, planReviewSession); len(runs) != 1 {
		t.Fatalf("replay started another run: %d", len(runs))
	}

	// The sending conversation's record shows it no longer waits (started;
	// the scripted run may already have ended its checkpoint).
	records := p.linkedRecords(t, planReviewOrigin)
	if len(records) != 1 || records[0]["task_id"] != planReviewTask || records[0]["status"] == "pending_approval" || asStringValue(records[0]["status"]) == "" {
		t.Fatalf("home record after accept: %v", records)
	}
}

// Purpose: a linked plan stays discoverable and decidable from the
// conversation that sent it across a home restart, and its settled decision
// survives the next one. Requirements: after the home's stores are closed
// and reopened under a new Server, (1) the origin feed lists the waiting
// task for the sending conversation and its review is the full plan at its
// exact version; (2) the person's exact acceptance there starts the
// machine's run once; (3) after another restart the record says started and
// the review offers nothing to decide. Threat: a plan card that exists only
// in memory, or a decision a restart forgets and offers again. Owners:
// Server.RecordFleetLinkedTask / the fleet store, Server.ReadTaskPlanReview,
// Server.DecideTaskPlan. Narrowest layer: real stores closed and reopened;
// the machine stays up as a linked machine would.
func TestTaskPlanReviewSurvivesHomeRestart(t *testing.T) {
	p := newPlanReviewPair(t)
	recorder := p.equipRemoteRunner(t)

	p.restartHome(t)
	records := p.linkedRecords(t, planReviewOrigin)
	if len(records) != 1 || records[0]["machine"] != "box" || records[0]["project_id"] != planReviewProject || records[0]["status"] != "pending_approval" {
		t.Fatalf("origin feed after restart: %v", records)
	}
	status, out, review := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil)
	if status != http.StatusOK || review.Decision != tool.ProjectTaskReviewAcceptPlan || review.PlanBinding == nil || review.PlanBinding.DefinitionRevision != 2 || review.PlanDocument == nil || review.PlanDocument.Info.Goal != p.document.Info.Goal {
		t.Fatalf("review after restart: %d %v", status, out)
	}
	if status, out, _ := p.decide(t, planReviewExact, "accept", ""); status != http.StatusOK {
		t.Fatalf("accept after restart: %d %v", status, out)
	}
	if !providerReached(recorder) {
		t.Fatal("the run never reached the provider")
	}

	p.restartHome(t)
	records = p.linkedRecords(t, planReviewOrigin)
	if len(records) != 1 || records[0]["status"] == "pending_approval" || asStringValue(records[0]["status"]) == "" {
		t.Fatalf("settled record after restart: %v", records)
	}
	if status, out, review := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil); status != http.StatusOK || review.Decision != tool.ProjectTaskReviewNone || review.RunStatus == "pending_approval" {
		t.Fatalf("settled review after restart: %d %v", status, out)
	}
	if runs := p.planRuns(t, planReviewSession); len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
}

// Purpose: a decision's answer never claims more than is known.
// Requirements: (1) accepted, but the read after it failed: 200 with ok,
// the exact version decided, review_current false and the read's error, and
// no review (never the pending one from before); (2) the link timed out
// before the machine answered: 502 outcome_unknown, nothing retried, and a
// later read shows what really happened (here, the machine did start it,
// once); (3) linked work off: 403 for the read and the decision, nothing
// done; (4) a principal of another account or another user of the account:
// 404 and nothing done. Threat: "Accepted ... pending approval" with accept
// offered again, an automatic retry after an ambiguous send, or every
// failure mapped to 409. Owners: Server.DecideTaskPlan,
// taskPlanDecisionFailure, handleTaskPlanReview. Narrowest layer: the
// two-daemon link, since the ambiguity is the link's.
func TestTaskPlanReviewDecisionAnswersWhatIsKnown(t *testing.T) {
	t.Run("refresh failed", func(t *testing.T) {
		p := newPlanReviewPair(t)
		p.equipRemoteRunner(t)
		p.home.s.taskPlanRefresh = func(context.Context, identity.Principal, string, string, string) (tool.ProjectTaskPlanReview, error) {
			return tool.ProjectTaskPlanReview{}, linkedTaskFail(http.StatusBadGateway, "the machine stopped answering")
		}
		status, out, _ := p.decide(t, planReviewExact, "accept", "")
		decided, _ := out["decided"].(map[string]any)
		binding, _ := decided["plan_binding"].(map[string]any)
		if status != http.StatusOK || out["ok"] != true || out["review_current"] != false || binding["definition_revision"] != float64(2) || !strings.Contains(asStringValue(out["refresh_error"]), "stopped answering") {
			t.Fatalf("refresh failed: %d %v", status, out)
		}
		if _, has := out["review"]; has {
			t.Fatalf("a review from before the decision was answered: %v", out["review"])
		}
		if runs := p.planRuns(t, planReviewSession); len(runs) != 1 {
			t.Fatalf("runs: %d", len(runs))
		}
	})

	t.Run("outcome unknown", func(t *testing.T) {
		p := newPlanReviewPair(t)
		p.equipRemoteRunner(t)
		// The machine holds the accept as it arrives; the home stops waiting
		// once the machine has it, before any answer: an ambiguous send.
		arrived, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		p.remote.s.beforeProjectTaskApprove = func(taskID string) {
			if taskID == planReviewTask {
				close(arrived)
				<-release
			}
		}
		p.remote.s.afterProjectTaskApprove = func(taskID string, err error) {
			if taskID == planReviewTask {
				finished <- err
			}
		}
		raw, _ := json.Marshal(map[string]any{"machine": "box", "project_id": planReviewProject, "task_id": planReviewTask, "decision": "accept", "plan_binding": planReviewExact})
		req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, taskPlanReviewDecisionPath, strings.NewReader(string(raw))), p.home.user, p.home.account)
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		answered := make(chan struct{})
		go func() {
			p.home.s.handleTaskPlanReview(w, req)
			close(answered)
		}()
		within(t, arrived, "the accept reaching the machine")
		cancel()
		within(t, answered, "the home's answer")
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		decided, _ := out["decided"].(map[string]any)
		binding, _ := decided["plan_binding"].(map[string]any)
		if w.Code != http.StatusBadGateway || out["ok"] != false || out["outcome_unknown"] != true || binding["definition_revision"] != float64(2) || out["review"] != nil {
			t.Fatalf("a timed-out send: %d %v", w.Code, out)
		}
		// The machine carries out what it received; its approve returns with
		// the task moved. The home did not retry.
		close(release)
		if err := within(t, finished, "the machine's approve"); err != nil {
			t.Fatalf("the machine's approve: %v", err)
		}
		if runs := p.planRuns(t, planReviewSession); len(runs) != 1 {
			t.Fatalf("runs after an ambiguous send: %d", len(runs))
		}
		if status, out, review := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil); status != http.StatusOK || review.Decision != tool.ProjectTaskReviewNone {
			t.Fatalf("the read after it: %d %v", status, out)
		}
	})

	t.Run("linked work off and other principals", func(t *testing.T) {
		p := newPlanReviewPair(t)
		p.equipRemoteRunner(t)
		account, err := p.home.s.sessions.Store().GetAccountPermissions(p.home.account)
		if err != nil {
			t.Fatal(err)
		}
		account.Linked["box"] = pebblestore.LinkedPermissionRule{Work: "off"}
		if _, err := p.home.s.sessions.Store().Underlying().SetAccountPermissions(p.home.account, account.Revision, account); err != nil {
			t.Fatal(err)
		}
		if status, out, _ := p.route(t, p.home.user, http.MethodGet, planReviewReadPath, nil); status != http.StatusForbidden || !strings.Contains(asStringValue(out["error"]), "linked work on box is off") {
			t.Fatalf("read with linked work off: %d %v", status, out)
		}
		if status, out, _ := p.decide(t, planReviewExact, "accept", ""); status != http.StatusForbidden {
			t.Fatalf("decide with linked work off: %d %v", status, out)
		}
		p.remoteUnchanged(t, "linked work off")

		account, _ = p.home.s.sessions.Store().GetAccountPermissions(p.home.account)
		account.Linked["box"] = pebblestore.LinkedPermissionRule{Work: "auto"}
		if _, err := p.home.s.sessions.Store().Underlying().SetAccountPermissions(p.home.account, account.Revision, account); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"machine": "box", "project_id": planReviewProject, "task_id": planReviewTask, "decision": "accept", "plan_binding": planReviewExact}
		raw, _ := json.Marshal(body)
		for _, c := range []struct{ user, account string }{{"someone-else", p.home.account}, {p.home.user, "another-account"}} {
			req := requestWithTestPrincipalForAccount(httptest.NewRequest(http.MethodPost, taskPlanReviewDecisionPath, strings.NewReader(string(raw))), c.user, c.account)
			w := httptest.NewRecorder()
			p.home.s.handleTaskPlanReview(w, req)
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), p.document.Info.Goal) {
				t.Fatalf("%s/%s decided: %d %s", c.user, c.account, w.Code, w.Body.String())
			}
		}
		p.remoteUnchanged(t, "other principals")
	})
}

// Purpose: an ordinary linked task (no plan) keeps its start decision
// through the same review route. Requirements: (1) its review says start,
// with no plan or binding; (2) a decision that names a plan binding is
// refused before anything crosses (409, nothing run); (3) the person's start
// with no binding starts the machine's task run exactly once and reaches the
// provider; the answer's "decided" names no plan; (4) a second start is
// refused. Threat: plan semantics breaking plain tasks, or a plan binding
// smuggled onto a task that has none. Owners: tool.ProjectTaskPlanReviewMatches,
// Server.DecideTaskPlan, Server.ApproveProjectTask (direct-agent path).
// Narrowest layer: the two-daemon link with the machine's real executor.
func TestTaskPlanReviewLinkedOrdinaryTaskStarts(t *testing.T) {
	p := newPlanReviewPair(t)
	recorder := p.equipRemoteRunner(t)
	const plainTask, plainSession = "plain-task", "plain-task-session"
	worktree := newRemoteTaskSession(t, p.remote, p.source, plainSession, plainTask, "agent/plain-task", "auto")
	db := p.remote.s.sessions.Store()
	// The session's agent profile, as task deployment stores it: the Coder,
	// resolved through the canonical system-agent registry.
	coder, err := p.remote.s.agents.ResolveSystemAgent(agentruntime.CoderAgentID, pebblestore.AgentProfile{Provider: recorder.ID(), Model: "test-model", Thinking: "low"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := db.GetSession(plainSession)
	if err != nil {
		t.Fatal(err)
	}
	session.Metadata["agent_profile"] = cloneSessionsV3AgentProfile(coder)
	if err := db.UpdateSession(session); err != nil {
		t.Fatal(err)
	}
	// Its brief, as task deployment appends it.
	if _, err := p.remote.s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{Kind: sessionruntime.SessionMutationAppendMessage, EventType: "session.message.appended", SessionID: plainSession, UserID: p.remote.user, AccountScopeID: p.remote.account,
		ClientRequestID: "plain-brief", IdempotencyKey: "plain-brief", PayloadHash: "plain-brief", Message: &pebblestore.MessageSnapshot{Role: "user", Content: "Do the plain step."}, NowUnixMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutProjectTask(p.remote.account, &pebblestore.ProjectTaskRecord{
		ID: plainTask, ProjectID: planReviewProject, AccountID: p.remote.account, Title: "Plain task", Agent: "coder", OutcomeType: "code_pr",
		Status: "pending_approval", SessionID: plainSession, WorkspacePath: worktree, SourceWorkspace: p.source, OriginSessionID: "",
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := p.home.s.CallFleetProject(ctx, p.hp, "box", "get_task", map[string]any{"project_id": planReviewProject, "task_id": plainTask})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.home.s.RecordFleetLinkedTask(ctx, p.hp, tool.FleetLinkedTaskOrigin{SessionID: planReviewOrigin}, "box", planReviewProject, raw); err != nil {
		t.Fatal(err)
	}
	read := "/v3/task-plan-review?machine=box&project_id=" + planReviewProject + "&task_id=" + plainTask
	if status, out, review := p.route(t, p.home.user, http.MethodGet, read, nil); status != http.StatusOK || review.Decision != tool.ProjectTaskReviewStart || review.PlanBound || review.PlanBinding != nil || review.PlanDocument != nil {
		t.Fatalf("ordinary review: %d %v", status, out)
	}
	decide := func(binding map[string]any) (int, map[string]any, tool.ProjectTaskPlanReview) {
		body := map[string]any{"machine": "box", "project_id": planReviewProject, "task_id": plainTask, "decision": "accept"}
		if binding != nil {
			body["plan_binding"] = binding
		}
		return p.route(t, p.home.user, http.MethodPost, taskPlanReviewDecisionPath, body)
	}
	if status, out, _ := decide(planReviewExact); status != http.StatusConflict || !strings.Contains(asStringValue(out["error"]), "no plan to accept") {
		t.Fatalf("a plan binding on an ordinary task: %d %v", status, out)
	}
	if runs := p.planRuns(t, plainSession); len(runs) != 0 {
		t.Fatalf("a refused start ran: %d", len(runs))
	}
	status, out, after := decide(nil)
	decided, _ := out["decided"].(map[string]any)
	if status != http.StatusOK || out["review_current"] != true || decided["task_id"] != plainTask || decided["plan_binding"] != nil || after.RunStatus == "pending_approval" || after.Decision != tool.ProjectTaskReviewNone {
		t.Fatalf("ordinary start: %d %v", status, out)
	}
	runs := p.planRuns(t, plainSession)
	if len(runs) != 1 || runs[0].RunID != "desktop-v3-run:task-"+plainTask || runs[0].PlanID != "" {
		t.Fatalf("ordinary runs: %+v", runs)
	}
	if !providerReached(recorder) {
		t.Fatalf("the ordinary run never reached the provider: %+v", p.planRuns(t, plainSession))
	}
	if status, out, _ := decide(nil); status != http.StatusConflict {
		t.Fatalf("a second start: %d %v", status, out)
	}
	if runs := p.planRuns(t, plainSession); len(runs) != 1 {
		t.Fatalf("a second start ran: %d", len(runs))
	}
	// The plan task beside it is untouched.
	p.remoteUnchanged(t, "starting the ordinary task")
}
