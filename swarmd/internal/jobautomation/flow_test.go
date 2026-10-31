package jobautomation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The flow report is how a person sees where one job's time and tokens went
// without reading raw events. These tests drive BuildFlowReport,
// FlowDeliveryCause and BuildFlowRollup (flow.go) with records shaped as the
// daemon stores them: V3 session events with the executor's tool payloads
// (recordProviderToolEvent), run records (V3SessionRunIntent), per-run usage
// (SessionTurnUsageSnapshot) and workflow deliveries (Delivery). The pure
// computation is the narrowest layer that owns the phase split, the
// classification and the ranking; the API's gathering of those records is
// covered in api/project_task_flow_test.go.

const flowT0 = int64(1_800_000_000_000)

func flowMin(m float64) int64 { return flowT0 + int64(m*60_000) }

func flowEvent(t *testing.T, seq uint64, eventType string, at int64, payload map[string]any) pebblestore.V3SessionEvent {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return pebblestore.V3SessionEvent{ID: eventType + "-" + string(rune('a'+seq)), SessionID: "s1", Seq: seq, EventType: eventType, Payload: raw, TsUnixMs: at}
}

func flowTool(t *testing.T, seq *uint64, run, call, name, args string, from, to int64, output string) []pebblestore.V3SessionEvent {
	t.Helper()
	*seq += 2
	return []pebblestore.V3SessionEvent{
		flowEvent(t, *seq-1, "session.tool.started", from, map[string]any{"run_id": run, "tool_name": name, "arguments": args, "call_id": call, "tool_instance_id": run + "/" + call}),
		flowEvent(t, *seq, "session.tool.completed", to, map[string]any{"run_id": run, "tool_name": name, "call_id": call, "tool_instance_id": run + "/" + call, "output": output}),
	}
}

// flowRebaseDeliveryFixture is a delivery whose review passed and whose push
// gate failed because dev moved; its reopen step sent the work back.
func flowRebaseDeliveryFixture(id, head string, created, reviewFrom, reviewTo, pushTo int64) Delivery {
	exit := 1
	return Delivery{
		ID: id, AutomationName: "land", JobID: "task/p1/t1", Head: head, Round: 1, State: StateSentBack, CreatedAt: created, StartedAt: created, FinishedAt: pushTo + 60_000,
		Definition: []Step{
			{ID: "review", Type: StepAgent, Worker: "reviewer"},
			{ID: "push", Type: StepRun, Command: "/usr/local/bin/push"},
			{ID: "back", Type: StepReopen, If: []Condition{{Step: "push", Status: StateFailed}}},
		},
		Steps: []StepRecord{
			{ID: "review", Type: StepAgent, State: StateSucceeded, StartedAt: reviewFrom, FinishedAt: reviewTo, Outcome: map[string]any{"verdict": VerdictOK}},
			{ID: "push", Type: StepRun, State: StateFailed, StartedAt: reviewTo, FinishedAt: pushTo, ExitCode: &exit, Reason: "rebase needed: dev moved and changed files this job also changed"},
			{ID: "back", Type: StepReopen, State: StateSucceeded, StartedAt: pushTo, FinishedAt: pushTo + 60_000},
		},
		SentBack: &SentBack{Step: "back", Round: 1, Reason: "push failed"},
	}
}

// flowJobFixture is one coder job: run 1 tests and ends on a provider limit,
// run 2 starts 72 minutes later and waits 5 minutes on a permission, a
// delivery reviews and fails its push because dev moved, the job sits 16
// minutes with nothing running, run 3 rebases, and a second delivery
// reviews the same head and lands.
func flowJobFixture(t *testing.T) FlowSource {
	t.Helper()
	seq := uint64(0)
	events := []pebblestore.V3SessionEvent{}
	events = append(events, flowTool(t, &seq, "run-1", "c1", "read", `{"path":"main.go"}`, flowMin(1), flowMin(1)+1000, strings.Repeat("r", 2000))...)
	events = append(events, flowTool(t, &seq, "run-1", "c2", "bash", `{"command":"cd swarmd && go test ./internal/jobautomation/"}`, flowMin(10), flowMin(20), strings.Repeat("t", 5000))...)
	events = append(events, flowTool(t, &seq, "run-1", "c3", "edit", `{"path":"main.go"}`, flowMin(22), flowMin(22)+500, "ok")...)
	events = append(events, flowTool(t, &seq, "run-2", "c4", "bash", `{"command":"npm test"}`, flowMin(110), flowMin(115), "pass")...)
	seq++
	events = append(events, flowEvent(t, seq, "permission.requested", flowMin(120), map[string]any{"run_id": "run-2", "tool_name": "bash", "call_id": "c5", "tool_instance_id": "run-2/c5"}))
	seq++
	events = append(events, flowEvent(t, seq, "permission.updated", flowMin(125), map[string]any{"run_id": "run-2", "tool_name": "bash", "call_id": "c5", "tool_instance_id": "run-2/c5"}))
	events = append(events, flowTool(t, &seq, "run-2", "c5", "bash", `{"command":"git status"}`, flowMin(125), flowMin(125)+200, "clean")...)
	events = append(events, flowTool(t, &seq, "run-2", "c6", "git_commit", `{"message":"fix"}`, flowMin(130), flowMin(130)+300, "committed")...)

	landed := flowRebaseDeliveryFixture("d2", "abc123def4567890", flowMin(170), flowMin(171), flowMin(181), flowMin(182))
	landed.State, landed.SentBack, landed.Round, landed.FinishedAt = StateSucceeded, nil, 1, flowMin(182)
	landed.Steps[1].State, landed.Steps[1].Reason, landed.Steps[1].ExitCode = StateSucceeded, "", nil
	landed.Steps[2] = StepRecord{ID: "back", Type: StepReopen, State: StateSkipped}

	return FlowSource{
		JobID: "task/p1/t1", ProjectID: "p1", TaskID: "t1", Title: "Fix the thing", Agent: "coder", Now: flowMin(300),
		Runs: []FlowRun{
			{SessionID: "s1", RunID: "run-1", Role: "coder", StartedAt: flowMin(0), EndedAt: flowMin(30), Status: "failed", Reason: "You've hit your usage limit · resets 6pm (UTC)",
				Usage: &pebblestore.SessionTurnUsageSnapshot{InputTokens: 100_000, CacheReadTokens: 20_000, OutputTokens: 5_000, Steps: 1}},
			{SessionID: "s1", RunID: "run-2", Role: "coder", StartedAt: flowMin(102), EndedAt: flowMin(132), Status: "completed",
				Usage: &pebblestore.SessionTurnUsageSnapshot{InputTokens: 600_000, CacheReadTokens: 80_000, OutputTokens: 10_000, Steps: 1}},
			{SessionID: "s1", RunID: "run-3", Role: "coder", StartedAt: flowMin(160), EndedAt: flowMin(170), Status: "completed"},
		},
		Events:     map[string][]pebblestore.V3SessionEvent{"s1": events},
		Deliveries: []Delivery{flowRebaseDeliveryFixture("d1", "abc123def4567890", flowMin(132), flowMin(133), flowMin(143), flowMin(144)), landed},
	}
}

// Requirement: every minute between the job's first and last activity lands
// in exactly one phase, from the records alone: test commands are testing,
// a provider-limit end waits until the next run, a permission waits on the
// person, reviewer steps are reviewing, gates are gates, the run after a
// dev-moved send-back is rebasing, and time with no run and no step is
// stalled. The top-waste list names the largest costs with their numbers.
// Threat: a report that double counts overlaps, hides the limit wait or the
// stall, or ranks a small cost first, so the person fixes the wrong thing.
func TestFlowReportPhasesAndTopWaste(t *testing.T) {
	report := BuildFlowReport(flowJobFixture(t))
	min := func(m int64) int64 { return m * 60_000 }
	want := FlowPhases{CodingMS: min(40), TestingMS: min(15), ReviewingMS: min(20), ProviderLimitMS: min(72), NeedsYouMS: min(5), StalledMS: min(18), GateMS: min(2), RebasingMS: min(10)}
	if report.Phases != want {
		t.Fatalf("phases:\n got %+v\nwant %+v", report.Phases, want)
	}
	if report.WallMS != min(182) || report.Phases.Total() != report.WallMS {
		t.Fatalf("wall %d, phases sum %d; want both %d", report.WallMS, report.Phases.Total(), min(182))
	}
	if len(report.Runs) != 3 {
		t.Fatalf("runs: %+v", report.Runs)
	}
	run1, run2, run3 := report.Runs[0], report.Runs[1], report.Runs[2]
	if run1.Tools != (FlowToolCounts{Read: 1, Edit: 1, Command: 1}) || run1.TestRuns != 1 || run1.TestMS != min(10) || !run1.LimitHit || run1.ToolResultBytes != 7002 {
		t.Fatalf("run 1: %+v", run1)
	}
	if run1.ContextPerRequest != 120_000 || run1.Tokens.Input != 100_000 || run1.Tokens.Output != 5_000 || run1.FirstRequestContext != nil || run1.DurationMS != min(30) {
		t.Fatalf("run 1 tokens: %+v", run1)
	}
	if run2.Tools != (FlowToolCounts{Command: 2, Commit: 1}) || run2.TestRuns != 1 || run2.ContextPerRequest != 680_000 || run2.LimitHit || run2.Phase != FlowPhaseCoding {
		t.Fatalf("run 2: %+v", run2)
	}
	if run3.Phase != FlowPhaseRebasing {
		t.Fatalf("run 3 follows the dev-moved send-back and must be rebasing: %+v", run3)
	}
	if report.Tokens.Input != 700_000 || report.Tokens.Output != 15_000 || report.Tokens.Requests != 2 {
		t.Fatalf("tokens: %+v", report.Tokens)
	}
	if report.Rebases != 1 || report.Reviews != 2 || len(report.Deliveries) != 2 {
		t.Fatalf("deliveries: rebases %d reviews %d %+v", report.Rebases, report.Reviews, report.Deliveries)
	}
	lines := []string{}
	for _, w := range report.TopWaste {
		lines = append(lines, w.Line)
	}
	wantLines := []string{
		"72 min waiting on provider limit",
		"context re-read grew from 120k to 680k per call",
		"18 min stalled (no run and no next step)",
		"15 min running tests (23% of agent time)",
		"2 full reviews of the same head abc123def456",
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("top waste:\n got %q\nwant %q", lines, wantLines)
	}
	md := FlowMarkdown(report)
	for _, part := range []string{"### Flow: Fix the thing", "provider limit 72 min", "1. 72 min waiting on provider limit", "cause: dev_moved", "| 3 coder | rebasing |"} {
		if !strings.Contains(md, part) {
			t.Fatalf("markdown lacks %q:\n%s", part, md)
		}
	}
	// The same records give the same report.
	if again := BuildFlowReport(flowJobFixture(t)); !reflect.DeepEqual(again, report) {
		t.Fatal("the report is not a pure function of its records")
	}
}

// Requirement: a round is attributed to its recorded cause: a push gate that
// failed with "rebase needed" after a passing review is dev moving, never
// the code; a review that blocked is the code; a gate that failed for
// another reason is the gate; a step ended by a provider limit is the
// provider; a landed delivery has no cause. Threat: rebases dev forced are
// blamed on the agent (or the reverse), so the person fixes the wrong loop.
func TestFlowDeliveryCauseClassifiesDevMoved(t *testing.T) {
	devMoved := flowRebaseDeliveryFixture("d1", "abc", flowMin(0), flowMin(1), flowMin(2), flowMin(3))
	if cause, detail := FlowDeliveryCause(devMoved); cause != FlowCauseDevMoved || !strings.Contains(detail, "rebase needed") {
		t.Fatalf("dev moved: cause %q detail %q", cause, detail)
	}
	row := flowDelivery(devMoved, flowMin(10))
	if row.Rebases != 1 || row.Reviews != 1 || len(row.Gates) != 1 || row.Gates[0].State != StateFailed || row.Cause != FlowCauseDevMoved {
		t.Fatalf("dev moved row: %+v", row)
	}

	gate := flowRebaseDeliveryFixture("d2", "abc", flowMin(0), flowMin(1), flowMin(2), flowMin(3))
	gate.Steps[1].Reason = "exited with code 1: remote rejected"
	if cause, _ := FlowDeliveryCause(gate); cause != FlowCauseGate {
		t.Fatalf("a push that failed for another reason is the gate, got %q", cause)
	}

	blocked := flowRebaseDeliveryFixture("d3", "abc", flowMin(0), flowMin(1), flowMin(2), flowMin(3))
	blocked.Definition[2].If = []Condition{{Step: "review", Verdict: VerdictBlock}}
	blocked.Steps[0].Outcome = map[string]any{"verdict": VerdictBlock}
	blocked.Steps[1] = StepRecord{ID: "push", Type: StepRun, State: StateSkipped}
	blocked.SentBack.Reason = "review verdict block"
	if cause, _ := FlowDeliveryCause(blocked); cause != FlowCauseCode {
		t.Fatalf("a blocking review is the code, got %q", cause)
	}

	limited := Delivery{ID: "d4", State: StateFailed, Steps: []StepRecord{{ID: "review", Type: StepAgent, State: StateFailed, Reason: "claude: rate limit exceeded (status 429)"}}}
	if cause, _ := FlowDeliveryCause(limited); cause != FlowCauseProvider {
		t.Fatalf("a provider limit is the provider, got %q", cause)
	}

	landed := devMoved
	landed.State, landed.SentBack = StateSucceeded, nil
	if cause, detail := FlowDeliveryCause(landed); cause != "" || detail != "" {
		t.Fatalf("a landed delivery has no cause, got %q %q", cause, detail)
	}
}

// Requirement: a project's roll-up for a period sums its tasks' phases,
// tokens, reviews and rebases, lists each task, and leaves out a task with
// no activity in the period; a task's report is bounded to the period.
// Threat: "where did this week go" double counts, misses a task, or counts
// time outside the period.
func TestFlowRollupSumsTasks(t *testing.T) {
	first := BuildFlowReport(flowJobFixture(t))
	src := flowJobFixture(t)
	src.TaskID, src.Title = "t2", "Second"
	src.Runs = src.Runs[1:2] // only run 2: 30 minutes, no limit
	src.Deliveries = nil
	second := BuildFlowReport(src)
	empty := BuildFlowReport(FlowSource{TaskID: "t3", Now: flowMin(300)})

	rollup := BuildFlowRollup("p1", "7d", flowMin(-60), flowMin(300), []FlowReport{first, second, empty})
	if rollup.Tasks != 2 || len(rollup.ByTask) != 2 {
		t.Fatalf("tasks: %d %+v", rollup.Tasks, rollup.ByTask)
	}
	var want FlowPhases
	want.Add(first.Phases)
	want.Add(second.Phases)
	if rollup.Phases != want || rollup.Phases.Total() != first.WallMS+second.WallMS {
		t.Fatalf("phases: got %+v want %+v", rollup.Phases, want)
	}
	if second.Phases.CodingMS != 20*60_000 || second.Phases.TestingMS != 5*60_000 || second.Phases.NeedsYouMS != 5*60_000 {
		t.Fatalf("second task phases: %+v", second.Phases)
	}
	if rollup.Tokens.Input != first.Tokens.Input+second.Tokens.Input || rollup.Reviews != 2 || rollup.Rebases != 1 {
		t.Fatalf("totals: %+v reviews %d rebases %d", rollup.Tokens, rollup.Reviews, rollup.Rebases)
	}
	if len(rollup.TopWaste) == 0 || rollup.TopWaste[0].Line != "72 min waiting on provider limit" {
		t.Fatalf("top waste: %+v", rollup.TopWaste)
	}
	if !strings.Contains(FlowRollupMarkdown(rollup), "2 tasks") {
		t.Fatalf("markdown: %s", FlowRollupMarkdown(rollup))
	}

	// Bounded to a period that starts at minute 100: run 1 and the limit
	// wait before it are outside, so only the time from minute 100 counts.
	bounded := flowJobFixture(t)
	bounded.From = flowMin(100)
	report := BuildFlowReport(bounded)
	if report.WallMS != 82*60_000 || report.Phases.Total() != report.WallMS || report.Phases.ProviderLimitMS != 2*60_000 {
		t.Fatalf("bounded: wall %d %+v", report.WallMS, report.Phases)
	}
	// Run 1 (minutes 0-30) is before the period: its row, tools and
	// tokens do not count. Both deliveries (from minute 132) do.
	if len(report.Runs) != 2 || report.Runs[0].RunID != "run-2" || report.Tokens.Input != 600_000 || report.Tokens.Output != 10_000 || report.Tokens.Requests != 1 {
		t.Fatalf("bounded runs/tokens: %+v %+v", report.Runs, report.Tokens)
	}
	if report.Reviews != 2 || report.Rebases != 1 || len(report.Deliveries) != 2 {
		t.Fatalf("bounded deliveries: reviews %d rebases %d %+v", report.Reviews, report.Rebases, report.Deliveries)
	}

	// Purpose: a period after the first delivery leaves out its review and
	// its rebase, and every run but the rebase run; the context-growth line
	// (from run 1 and run 2) is gone with them. Threat: older reviews,
	// rebases and tokens counted in "where did this week go".
	late := flowJobFixture(t)
	late.From = flowMin(150)
	report = BuildFlowReport(late)
	if len(report.Runs) != 1 || report.Runs[0].RunID != "run-3" || report.Tokens != (FlowTokens{}) {
		t.Fatalf("late runs/tokens: %+v %+v", report.Runs, report.Tokens)
	}
	if report.Reviews != 1 || report.Rebases != 0 || len(report.Deliveries) != 1 || report.Deliveries[0].ID != "d2" {
		t.Fatalf("late deliveries: reviews %d rebases %d %+v", report.Reviews, report.Rebases, report.Deliveries)
	}
	for _, w := range report.TopWaste {
		if w.Kind == "context_growth" || w.Kind == FlowPhaseRebasing {
			t.Fatalf("late top waste counts activity before the period: %+v", report.TopWaste)
		}
	}
	// And the roll-up of that period sums only what is in it.
	rollup = BuildFlowRollup("p1", "7d", flowMin(150), flowMin(300), []FlowReport{report})
	if rollup.Tokens != (FlowTokens{}) || rollup.Reviews != 1 || rollup.Rebases != 0 {
		t.Fatalf("late rollup: %+v reviews %d rebases %d", rollup.Tokens, rollup.Reviews, rollup.Rebases)
	}
	// A period that ends before run 3 leaves out run 3 and delivery d2.
	early := flowJobFixture(t)
	early.From, early.To = flowMin(100), flowMin(150)
	report = BuildFlowReport(early)
	if len(report.Runs) != 1 || report.Runs[0].RunID != "run-2" || report.Reviews != 1 || report.Rebases != 1 || len(report.Deliveries) != 1 {
		t.Fatalf("early: runs %+v reviews %d rebases %d deliveries %+v", report.Runs, report.Reviews, report.Rebases, report.Deliveries)
	}
}

// Requirement: a task waiting on the person (an unanswered question or
// approval) counts that wait as needs_you even while its run is still open;
// a waiting task whose permission event was not read waits from its last
// settled activity. Threat: the person's wait reported as coding. Owner:
// BuildFlowReport (the status mapping is api.flowTaskLive).
func TestFlowWaitingOnPersonWithOpenRun(t *testing.T) {
	seq := uint64(0)
	events := flowTool(t, &seq, "run-1", "c1", "read", `{"path":"main.go"}`, flowMin(1), flowMin(2), "x")
	seq++
	events = append(events, flowEvent(t, seq, "permission.requested", flowMin(10), map[string]any{"run_id": "run-1", "tool_name": "ask_user", "call_id": "q1", "tool_instance_id": "run-1/q1"}))
	src := FlowSource{TaskID: "t1", Now: flowMin(30), Live: true, WaitingOnPerson: true,
		Runs:   []FlowRun{{SessionID: "s1", RunID: "run-1", Role: "coder", StartedAt: flowMin(0), Status: "running"}},
		Events: map[string][]pebblestore.V3SessionEvent{"s1": events}}
	report := BuildFlowReport(src)
	if report.Phases.NeedsYouMS != 20*60_000 || report.Phases.CodingMS != 10*60_000 || report.WallMS != 30*60_000 {
		t.Fatalf("open question: %+v wall %d", report.Phases, report.WallMS)
	}
	// The permission event was not read (only the tool call was).
	src.Events = map[string][]pebblestore.V3SessionEvent{"s1": events[:2]}
	report = BuildFlowReport(src)
	if report.Phases.NeedsYouMS != 28*60_000 || report.Phases.CodingMS != 2*60_000 {
		t.Fatalf("waiting without its event: %+v", report.Phases)
	}
}

// Requirement: a run bound reached and a message bound reached are both
// reported as partial with a note. Threat: arbitrarily dropped runs read as
// a complete report.
func TestFlowReportMarksReadBoundsPartial(t *testing.T) {
	src := flowJobFixture(t)
	if r := BuildFlowReport(src); r.Partial {
		t.Fatalf("complete records reported partial: %+v", r.Notes)
	}
	src.RunsTruncated = true
	r := BuildFlowReport(src)
	if !r.Partial || !strings.Contains(strings.Join(r.Notes, "\n"), "more runs than the bound") {
		t.Fatalf("runs bound: partial %v notes %q", r.Partial, r.Notes)
	}
	src.RunsTruncated, src.MessagesTruncated = false, true
	r = BuildFlowReport(src)
	if !r.Partial || !strings.Contains(strings.Join(r.Notes, "\n"), "more messages than the bound") {
		t.Fatalf("messages bound: partial %v notes %q", r.Partial, r.Notes)
	}
}

// Requirement: test commands are recognised by what they run.
func TestFlowTestCommand(t *testing.T) {
	for _, command := range []string{"go test ./...", "cd web && npm test", "npm run test -- --watch=false", "pytest -q", "python -m pytest", "cargo test", "bash scripts/run-critical-tests.sh fast", "make test"} {
		if !FlowTestCommand(command) {
			t.Errorf("%q is a test command", command)
		}
	}
	for _, command := range []string{"go vet ./...", "git status", "npm install", "go build ./...", "cat testdata/x"} {
		if FlowTestCommand(command) {
			t.Errorf("%q is not a test command", command)
		}
	}
}
