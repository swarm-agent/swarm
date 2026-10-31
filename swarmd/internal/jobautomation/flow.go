package jobautomation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/providerlimit"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The flow report: where one job's time and tokens went, computed only from
// durable records (the job's session events and run records, its usage
// records and its workflow deliveries). Nothing here is sampled, estimated
// from formulas or stored: the same records always give the same report.
// The caller gathers the records (FlowSource); BuildFlowReport reads them.

// Phases a job's time is split into. Every millisecond between the job's
// first and last activity (or now, while it is unfinished) counts in exactly
// one phase: where phases overlap, the one listed first wins (a test command
// that runs while a permission waits is waiting on a person, not testing).
const (
	FlowPhaseProviderLimit = "provider_limit" // a run ended on a provider limit until the next one started
	FlowPhaseNeedsYou      = "needs_you"      // a permission or question waited for the person
	FlowPhaseTesting       = "testing"        // a test command ran (FlowTestCommand)
	FlowPhaseReviewing     = "reviewing"      // a reviewer step or reviewer run
	FlowPhaseRebasing      = "rebasing"       // the run sent back only because dev moved
	FlowPhaseGate          = "gate"           // a workflow run (gate) or deliver step
	FlowPhaseCoding        = "coding"         // any other time inside a run
	FlowPhaseStalled       = "stalled"        // no run and no step ran
)

// flowPhaseOrder is the phase precedence, highest first.
var flowPhaseOrder = []string{FlowPhaseProviderLimit, FlowPhaseNeedsYou, FlowPhaseTesting, FlowPhaseReviewing, FlowPhaseRebasing, FlowPhaseGate, FlowPhaseCoding}

// Delivery round causes (FlowDeliveryCause).
const (
	FlowCauseCode     = "code"      // the review blocked: the work itself
	FlowCauseDevMoved = "dev_moved" // the push needed a rebase: dev moved
	FlowCauseGate     = "gate"      // a gate (run step) failed
	FlowCauseProvider = "provider"  // a provider limit or failure ended a step
)

// Bounds that keep one report cheap.
const (
	FlowMaxEventsPerSession = 20000
	flowMaxSpans            = 20000
	flowTopWaste            = 5
	flowMarkdownRuns        = 12
)

// flowTestCommand matches commands that run tests.
var flowTestCommand = regexp.MustCompile(`(?i)(^|[\s;&|(/])(go\s+test|gotestsum|(npm|pnpm|yarn|bun)\s+(run\s+)?test|npx\s+(vitest|jest|playwright)|vitest|jest|pytest|python3?\s+-m\s+(pytest|unittest)|cargo\s+(test|nextest)|mvn\s+(\S+\s+)*test|gradlew?\s+(\S+\s+)*test|make\s+(\S+\s+)*(test|check)|run-critical-tests\.sh|rspec|phpunit|dotnet\s+test|ctest|tox)(\b|$)`)

// FlowTestCommand reports whether a shell command runs tests (go test, npm
// test, pytest, cargo test and the like).
func FlowTestCommand(command string) bool {
	return flowTestCommand.MatchString(command)
}

// ToolKind is the kind of a tool call by its name: read, edit, command,
// commit or other. The fleet's agents-at-work view and the flow report use it.
func ToolKind(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "search", "find", "list", "grep", "glob", "ls", "webfetch", "websearch", "webdownload":
		return "read"
	case "edit", "write", "multiedit", "multi_edit", "apply_patch":
		return "edit"
	case "bash", "shell", "exec_command":
		return "command"
	case "git_commit":
		return "commit"
	}
	return "other"
}

// FlowRun is one run of the job's sessions (its run record).
type FlowRun struct {
	SessionID string
	RunID     string
	// Role is the agent that ran (coder, reviewer, ...).
	Role      string
	StartedAt int64
	EndedAt   int64 // 0 while it runs
	Status    string
	Reason    string
	Usage     *pebblestore.SessionTurnUsageSnapshot
}

// FlowSource is every record a flow report reads.
type FlowSource struct {
	JobID     string
	ProjectID string
	TaskID    string
	Title     string
	Agent     string
	Now       int64
	// Live: the job is unfinished, so the time since its last activity
	// counts (as needs_you when WaitingOnPerson, else stalled).
	Live            bool
	WaitingOnPerson bool
	// From and To bound the report (a roll-up's period); 0 is unbounded.
	// Bounded, only the runs and deliveries active in [From, To] count
	// (each whole: its tokens, tools, reviews and rebases), and the phases
	// are clipped to it.
	From int64
	To   int64
	Runs []FlowRun
	// RunsTruncated: a session had more runs than were read.
	RunsTruncated bool
	// Events are each session's events, oldest first.
	Events          map[string][]pebblestore.V3SessionEvent
	EventsTruncated bool
	// MessagesTruncated: a session had more messages than were read.
	MessagesTruncated bool
	Deliveries        []Delivery
	// Interventions counts what a person sent the job by hand (messages,
	// explicit re-runs are added from the deliveries).
	Interventions int
}

// FlowPhases is time per phase, in milliseconds.
type FlowPhases struct {
	CodingMS        int64 `json:"coding_ms"`
	TestingMS       int64 `json:"testing_ms"`
	ReviewingMS     int64 `json:"reviewing_ms"`
	ProviderLimitMS int64 `json:"provider_limit_ms"`
	NeedsYouMS      int64 `json:"needs_you_ms"`
	StalledMS       int64 `json:"stalled_ms"`
	GateMS          int64 `json:"gate_ms"`
	RebasingMS      int64 `json:"rebasing_ms"`
}

func (p *FlowPhases) add(phase string, ms int64) {
	switch phase {
	case FlowPhaseCoding:
		p.CodingMS += ms
	case FlowPhaseTesting:
		p.TestingMS += ms
	case FlowPhaseReviewing:
		p.ReviewingMS += ms
	case FlowPhaseProviderLimit:
		p.ProviderLimitMS += ms
	case FlowPhaseNeedsYou:
		p.NeedsYouMS += ms
	case FlowPhaseStalled:
		p.StalledMS += ms
	case FlowPhaseGate:
		p.GateMS += ms
	case FlowPhaseRebasing:
		p.RebasingMS += ms
	}
}

// Add sums other into p.
func (p *FlowPhases) Add(other FlowPhases) {
	p.CodingMS += other.CodingMS
	p.TestingMS += other.TestingMS
	p.ReviewingMS += other.ReviewingMS
	p.ProviderLimitMS += other.ProviderLimitMS
	p.NeedsYouMS += other.NeedsYouMS
	p.StalledMS += other.StalledMS
	p.GateMS += other.GateMS
	p.RebasingMS += other.RebasingMS
}

// Total is the sum of every phase.
func (p FlowPhases) Total() int64 {
	return p.CodingMS + p.TestingMS + p.ReviewingMS + p.ProviderLimitMS + p.NeedsYouMS + p.StalledMS + p.GateMS + p.RebasingMS
}

// FlowToolCounts are a run's tool calls by kind (ToolKind).
type FlowToolCounts struct {
	Read    int `json:"read"`
	Edit    int `json:"edit"`
	Command int `json:"command"`
	Commit  int `json:"commit"`
	Other   int `json:"other"`
}

// FlowTokens are tokens from the runs' usage records.
type FlowTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Requests   int   `json:"requests"`
}

func (t *FlowTokens) Add(other FlowTokens) {
	t.Input += other.Input
	t.Output += other.Output
	t.CacheRead += other.CacheRead
	t.CacheWrite += other.CacheWrite
	t.Requests += other.Requests
}

// FlowRunReport is one run.
type FlowRunReport struct {
	SessionID  string         `json:"session_id"`
	RunID      string         `json:"run_id"`
	Role       string         `json:"role,omitempty"`
	Phase      string         `json:"phase"`
	Status     string         `json:"status,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	StartedAt  int64          `json:"started_at"`
	EndedAt    int64          `json:"ended_at,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Tools      FlowToolCounts `json:"tools"`
	TestRuns   int            `json:"test_runs"`
	TestMS     int64          `json:"test_ms"`
	// ToolResultBytes is what the run's tool results added to its context.
	ToolResultBytes int64      `json:"tool_result_bytes"`
	Tokens          FlowTokens `json:"tokens"`
	// ContextPerRequest is the run's input context read per provider
	// request: (input + cache read + cache write tokens) / requests.
	ContextPerRequest int64 `json:"context_tokens_per_request"`
	// FirstRequestContext is the first provider request's context size.
	// TODO(a receipt per provider request): the usage record is per run
	// today, so this stays null until per-request receipts are on dev.
	FirstRequestContext *int64 `json:"first_request_context_tokens"`
	LimitHit            bool   `json:"provider_limit,omitempty"`
}

// FlowGate is one gate (run step) of a delivery.
type FlowGate struct {
	Step     string `json:"step"`
	State    string `json:"state"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// FlowDeliveryReport is one workflow delivery.
type FlowDeliveryReport struct {
	ID         string     `json:"id"`
	Workflow   string     `json:"workflow"`
	State      string     `json:"state"`
	Head       string     `json:"head,omitempty"`
	Round      int        `json:"round"`
	Reviews    int        `json:"reviews"`
	Verdicts   []string   `json:"verdicts,omitempty"`
	Rebases    int        `json:"rebases"`
	LimitWaits int        `json:"limit_waits,omitempty"`
	Reruns     int        `json:"reruns,omitempty"`
	Gates      []FlowGate `json:"gates,omitempty"`
	// Cause is why the round did not land: code, dev_moved, gate or
	// provider (FlowDeliveryCause); "" when it landed or still runs.
	Cause       string `json:"cause,omitempty"`
	CauseDetail string `json:"cause_detail,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`
	FinishedAt  int64  `json:"finished_at,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
}

// FlowWaste is one top-waste line: the cost, its number and its share of
// the job's time or tokens (Score, 0..1) by which the lines are ranked.
type FlowWaste struct {
	Kind  string  `json:"kind"`
	Line  string  `json:"line"`
	Value int64   `json:"value"`
	Score float64 `json:"-"`
}

// FlowReport is the flow report of one job.
type FlowReport struct {
	JobID         string               `json:"job_id"`
	ProjectID     string               `json:"project_id,omitempty"`
	TaskID        string               `json:"task_id,omitempty"`
	Title         string               `json:"title,omitempty"`
	From          int64                `json:"from_ms"`
	To            int64                `json:"to_ms"`
	WallMS        int64                `json:"wall_ms"`
	Phases        FlowPhases           `json:"phases"`
	Tokens        FlowTokens           `json:"tokens"`
	Runs          []FlowRunReport      `json:"runs"`
	Deliveries    []FlowDeliveryReport `json:"deliveries"`
	Rebases       int                  `json:"rebases"`
	Reviews       int                  `json:"reviews"`
	Interventions int                  `json:"interventions"`
	TopWaste      []FlowWaste          `json:"top_waste"`
	Partial       bool                 `json:"partial,omitempty"`
	Notes         []string             `json:"notes,omitempty"`
}

type flowSpan struct {
	from, to int64
	phase    string
}

// flowToolEvent is the part of a session.tool.* event payload the report reads.
type flowToolEvent struct {
	RunID      string `json:"run_id"`
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
	CallID     string `json:"call_id"`
	Instance   string `json:"tool_instance_id"`
	Output     string `json:"output"`
	RawOutput  string `json:"raw_output"`
	DurationMS int64  `json:"duration_ms"`
}

// BuildFlowReport computes a job's flow report from its records.
func BuildFlowReport(src FlowSource) FlowReport {
	now := src.Now
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	report := FlowReport{JobID: src.JobID, ProjectID: src.ProjectID, TaskID: src.TaskID, Title: src.Title, Runs: []FlowRunReport{}, Deliveries: []FlowDeliveryReport{}, TopWaste: []FlowWaste{},
		Partial: src.EventsTruncated || src.RunsTruncated || src.MessagesTruncated}
	if src.EventsTruncated {
		report.Notes = append(report.Notes, fmt.Sprintf("only the first %d tool and permission events of a session were read", FlowMaxEventsPerSession))
	}
	if src.RunsTruncated {
		report.Notes = append(report.Notes, "a session had more runs than the bound; the runs read are in store key order, not time order, so some runs are missing")
	}
	if src.MessagesTruncated {
		report.Notes = append(report.Notes, "a session had more messages than the bound; hand interventions may be undercounted")
	}
	// inPeriod: an activity from..to (to 0: still on) overlaps the
	// report's bounds.
	inPeriod := func(from, to int64) bool {
		if to <= 0 {
			to = now
		}
		return (src.From <= 0 || to >= src.From) && (src.To <= 0 || from <= src.To)
	}
	// settled is the last activity that did not run on to now (a waiting
	// task waits on the person from there).
	settled := int64(0)
	report.Notes = append(report.Notes, "first_request_context_tokens: not recorded yet (usage is per run until per-request receipts land)")
	spans := []flowSpan{}
	first, last := int64(0), int64(0)
	seen := func(from, to int64) {
		if from > 0 && (first == 0 || from < first) {
			first = from
		}
		if to > last {
			last = to
		}
		if to < now && to > settled {
			settled = to
		}
	}

	runs := append([]FlowRun(nil), src.Runs...)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt < runs[j].StartedAt })
	// Rebase runs: the first run of the job that starts after a delivery
	// sent the work back only because dev moved.
	rebaseAfter := []int64{}
	for _, d := range src.Deliveries {
		if flowRebaseDelivery(d) && d.FinishedAt > 0 {
			rebaseAfter = append(rebaseAfter, d.FinishedAt)
		}
	}
	sort.Slice(rebaseAfter, func(i, j int) bool { return rebaseAfter[i] < rebaseAfter[j] })
	rebaseRun := map[int]bool{}
	for _, at := range rebaseAfter {
		for i, run := range runs {
			if run.StartedAt >= at && !rebaseRun[i] {
				rebaseRun[i] = true
				break
			}
		}
	}

	for i, run := range runs {
		end := run.EndedAt
		if end <= 0 {
			end = now
		}
		if end < run.StartedAt {
			end = run.StartedAt
		}
		phase := FlowPhaseCoding
		switch {
		case strings.EqualFold(run.Role, pebblestore.WorkerRoleReviewer):
			phase = FlowPhaseReviewing
		case rebaseRun[i]:
			phase = FlowPhaseRebasing
		}
		row := FlowRunReport{SessionID: run.SessionID, RunID: run.RunID, Role: run.Role, Phase: phase, Status: run.Status, Reason: clipFlow(run.Reason, 200), StartedAt: run.StartedAt, EndedAt: run.EndedAt, DurationMS: end - run.StartedAt}
		if run.StartedAt > 0 {
			spans = append(spans, flowSpan{run.StartedAt, end, phase})
			seen(run.StartedAt, end)
		}
		if u := run.Usage; u != nil {
			row.Tokens = FlowTokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens, CacheWrite: u.CacheWriteTokens, Requests: u.Steps}
			if u.Steps > 0 {
				row.ContextPerRequest = (u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens) / int64(u.Steps)
			}
		}
		// A run that ended on a provider limit waits until the next run
		// of its session starts (or now, while the job is unfinished).
		if run.EndedAt > 0 && (providerlimit.Is(run.Reason) || providerlimit.IsExhausted(run.Reason)) {
			row.LimitHit = true
			resume := int64(0)
			for _, next := range runs[i+1:] {
				if next.SessionID == run.SessionID && next.StartedAt >= run.EndedAt {
					resume = next.StartedAt
					break
				}
			}
			if resume == 0 && src.Live {
				resume = now
			}
			if resume > run.EndedAt {
				spans = append(spans, flowSpan{run.EndedAt, resume, FlowPhaseProviderLimit})
				seen(run.EndedAt, resume)
			}
		}
		report.Runs = append(report.Runs, row)
	}

	// Tool calls and permission waits, from each session's events.
	waitOpen := false
	sessions := make([]string, 0, len(src.Events))
	for id := range src.Events {
		sessions = append(sessions, id)
	}
	sort.Strings(sessions)
	for _, sessionID := range sessions {
		events := src.Events[sessionID]
		started := map[string]int{} // call -> index into calls
		type call struct {
			at, end int64
			runIdx  int
			kind    string
			test    bool
		}
		calls := []call{}
		permissions := map[string]int64{}
		for _, ev := range events {
			switch {
			case ev.EventType == "permission.requested":
				var p flowToolEvent
				_ = json.Unmarshal(ev.Payload, &p)
				permissions[firstFlow(p.Instance, p.CallID, ev.ID)] = ev.TsUnixMs
			case ev.EventType == "permission.updated":
				var p flowToolEvent
				_ = json.Unmarshal(ev.Payload, &p)
				key := firstFlow(p.Instance, p.CallID, ev.ID)
				if at, ok := permissions[key]; ok {
					delete(permissions, key)
					if ev.TsUnixMs > at {
						spans = append(spans, flowSpan{at, ev.TsUnixMs, FlowPhaseNeedsYou})
					}
				}
				if ev.TsUnixMs < now {
					settled = max(settled, ev.TsUnixMs)
				}
			case strings.HasPrefix(ev.EventType, "session.tool."):
				var p flowToolEvent
				if json.Unmarshal(ev.Payload, &p) != nil {
					continue
				}
				key := firstFlow(p.Instance, p.CallID)
				runIdx := flowRunIndex(report.Runs, sessionID, p.RunID, ev.TsUnixMs)
				if ev.TsUnixMs < now {
					settled = max(settled, ev.TsUnixMs)
				}
				switch ev.EventType {
				case "session.tool.started":
					kind := ToolKind(p.ToolName)
					test := kind == "command" && FlowTestCommand(flowCommand(p.Arguments))
					started[key] = len(calls)
					calls = append(calls, call{at: ev.TsUnixMs, runIdx: runIdx, kind: kind, test: test})
				case "session.tool.completed", "session.tool.failed", "session.tool.cancelled", "session.tool.canceled":
					if runIdx >= 0 {
						report.Runs[runIdx].ToolResultBytes += int64(len(p.Output))
					}
					if at, ok := started[key]; ok {
						end := ev.TsUnixMs
						if p.DurationMS > 0 && calls[at].at+p.DurationMS < end {
							end = calls[at].at + p.DurationMS
						}
						calls[at].end = end
						delete(started, key)
					}
				}
			}
		}
		// A permission still asked waits until now, while the job is unfinished.
		if src.Live {
			for _, at := range permissions {
				spans = append(spans, flowSpan{at, now, FlowPhaseNeedsYou})
				waitOpen = true
			}
		}
		for _, c := range calls {
			if c.runIdx >= 0 {
				row := &report.Runs[c.runIdx]
				switch c.kind {
				case "read":
					row.Tools.Read++
				case "edit":
					row.Tools.Edit++
				case "command":
					row.Tools.Command++
				case "commit":
					row.Tools.Commit++
				default:
					row.Tools.Other++
				}
			}
			if !c.test {
				continue
			}
			end := c.end
			if end == 0 {
				// Never ended: it ran until its run did.
				end = now
				if c.runIdx >= 0 && report.Runs[c.runIdx].EndedAt > 0 {
					end = report.Runs[c.runIdx].EndedAt
				}
			}
			if end < c.at {
				end = c.at
			}
			if c.runIdx >= 0 {
				report.Runs[c.runIdx].TestRuns++
				report.Runs[c.runIdx].TestMS += end - c.at
			}
			spans = append(spans, flowSpan{c.at, end, FlowPhaseTesting})
		}
	}

	// Deliveries: their steps' time and why each round ran.
	deliveries := append([]Delivery(nil), src.Deliveries...)
	sort.SliceStable(deliveries, func(i, j int) bool { return deliveries[i].CreatedAt < deliveries[j].CreatedAt })
	for _, d := range deliveries {
		row := flowDelivery(d, now)
		deliveryEnd := d.FinishedAt
		if deliveryEnd <= 0 && Terminal(d.State) {
			deliveryEnd = d.CreatedAt
		}
		// Outside the bounds, only its spans count (clipped away below).
		if inPeriod(d.CreatedAt, deliveryEnd) {
			report.Deliveries = append(report.Deliveries, row)
			report.Rebases += row.Rebases
			report.Reviews += row.Reviews
			report.Interventions += d.Reruns
		}
		for _, rec := range d.Steps {
			def, _ := d.step(rec.ID)
			phase := ""
			switch rec.Type {
			case StepRun, StepDeliver:
				phase = FlowPhaseGate
			case StepAgent:
				phase = FlowPhaseCoding
				if flowReviewStep(def, rec) {
					phase = FlowPhaseReviewing
				}
			}
			if phase != "" && rec.StartedAt > 0 {
				end := rec.FinishedAt
				if end <= 0 {
					end = now
				}
				if end > rec.StartedAt {
					spans = append(spans, flowSpan{rec.StartedAt, end, phase})
					seen(rec.StartedAt, end)
				}
			}
			// An agent step's provider-limit waits: from each failed
			// attempt on a limit to its stated reset (or the step's next
			// end), and the wait that is still on.
			for i, failure := range rec.Failures {
				if !providerlimit.Is(failure.Reason) {
					continue
				}
				until := int64(0)
				if reset, ok := providerlimit.ResetAt(failure.Reason, time.UnixMilli(failure.At)); ok {
					until = reset.UnixMilli()
				}
				bound := rec.FinishedAt
				if i+1 < len(rec.Failures) {
					bound = rec.Failures[i+1].At
				}
				if bound > 0 && (until == 0 || until > bound) {
					until = bound
				}
				if until > failure.At {
					spans = append(spans, flowSpan{failure.At, until, FlowPhaseProviderLimit})
					seen(failure.At, until)
				}
			}
			if LimitWaiting(rec) {
				from := rec.StartedAt
				if n := len(rec.Failures); n > 0 {
					from = rec.Failures[n-1].At
				}
				until := min(rec.NextAttemptAt, now)
				if from > 0 && until > from {
					spans = append(spans, flowSpan{from, until, FlowPhaseProviderLimit})
					seen(from, until)
				}
			}
		}
		seen(d.CreatedAt, max(d.FinishedAt, d.CreatedAt))
	}
	report.Interventions += src.Interventions

	// The report's span: first activity to last (now while unfinished),
	// within the requested window. A job waiting on the person with no
	// open permission read waits from its last settled activity (an open
	// run's time to now is the person's, not coding).
	if src.Live && first > 0 {
		if src.WaitingOnPerson && !waitOpen {
			from := settled
			if from <= 0 {
				from = first
			}
			if now > from {
				spans = append(spans, flowSpan{from, now, FlowPhaseNeedsYou})
			}
		}
		last = now
	}
	// Runs outside the bounds: their spans are clipped away; their rows,
	// tools and tokens do not count.
	kept := report.Runs[:0]
	for i, row := range report.Runs {
		if runs[i].StartedAt > 0 && !inPeriod(runs[i].StartedAt, runs[i].EndedAt) {
			continue
		}
		report.Tokens.Add(row.Tokens)
		kept = append(kept, row)
	}
	report.Runs = kept
	if src.From > 0 && first < src.From {
		first = src.From
	}
	if src.To > 0 && (last > src.To || last == 0) {
		last = src.To
	}
	if first > 0 && last > first {
		report.From, report.To, report.WallMS = first, last, last-first
		if len(spans) > flowMaxSpans {
			spans = spans[len(spans)-flowMaxSpans:]
			report.Partial = true
		}
		report.Phases = sweepFlowSpans(spans, first, last)
	}
	report.TopWaste = flowTopWasteLines(report)
	return report
}

// sweepFlowSpans splits [from, to) into phases: each moment goes to the
// highest-precedence span covering it, stalled where none does.
func sweepFlowSpans(spans []flowSpan, from, to int64) FlowPhases {
	rank := map[string]int{}
	for i, phase := range flowPhaseOrder {
		rank[phase] = i
	}
	type edge struct {
		at    int64
		phase int
		delta int
	}
	edges := make([]edge, 0, 2*len(spans))
	for _, s := range spans {
		a, b := max(s.from, from), min(s.to, to)
		phase, ok := rank[s.phase]
		if b <= a || !ok {
			continue
		}
		edges = append(edges, edge{a, phase, 1}, edge{b, phase, -1})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })
	active := make([]int, len(flowPhaseOrder))
	var out FlowPhases
	cursor := from
	current := func() string {
		for i, n := range active {
			if n > 0 {
				return flowPhaseOrder[i]
			}
		}
		return FlowPhaseStalled
	}
	for _, e := range edges {
		if e.at > cursor {
			out.add(current(), e.at-cursor)
			cursor = e.at
		}
		active[e.phase] += e.delta
	}
	if to > cursor {
		out.add(current(), to-cursor)
	}
	return out
}

// flowRunIndex finds the run an event belongs to: by run id, else by time.
func flowRunIndex(runs []FlowRunReport, sessionID, runID string, at int64) int {
	if runID != "" {
		for i := range runs {
			if runs[i].SessionID == sessionID && runs[i].RunID == runID {
				return i
			}
		}
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].SessionID == sessionID && runs[i].StartedAt <= at && (runs[i].EndedAt == 0 || at <= runs[i].EndedAt) {
			return i
		}
	}
	return -1
}

// flowCommand is a command tool call's command line.
func flowCommand(arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return arguments
	}
	for _, key := range []string{"command", "cmd"} {
		switch v := args[key].(type) {
		case string:
			return v
		case []any:
			parts := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// flowReviewStep reports whether a delivery's agent step is a review.
func flowReviewStep(def Step, rec StepRecord) bool {
	if strings.EqualFold(strings.TrimSpace(def.Worker), pebblestore.WorkerRoleReviewer) {
		return true
	}
	_, ok := rec.Outcome["verdict"].(string)
	return ok
}

// flowRebaseDelivery reports whether a delivery sent the work back only
// because the push needed a rebase.
func flowRebaseDelivery(d Delivery) bool {
	cause, _ := FlowDeliveryCause(d)
	return cause == FlowCauseDevMoved && d.State == StateSentBack
}

// FlowDeliveryCause is why a delivery's round did not land, from the reasons
// the delivery recorded: dev_moved (the push needed a rebase), code (the
// review blocked), gate (a run step failed) or provider (a provider limit
// ended a step). It is "" for a delivery that landed or still runs.
func FlowDeliveryCause(d Delivery) (cause, detail string) {
	switch d.State {
	case StateSucceeded, StateRunning, StatePending, StateWaiting, "":
		return "", ""
	}
	if d.SentBack != nil {
		if IsRebaseReopen(d, d.SentBack.Step) {
			_, failure := ReopenCause(d, d.SentBack.Step)
			return FlowCauseDevMoved, clipFlow(failure, 200)
		}
		reopen, failure := ReopenCause(d, d.SentBack.Step)
		switch reopen {
		case pebblestore.ProjectTaskReopenReviewBlock:
			return FlowCauseCode, clipFlow(firstFlow(d.SentBack.Reason, "review verdict block"), 200)
		default:
			if providerlimit.Is(failure) {
				return FlowCauseProvider, clipFlow(failure, 200)
			}
			return FlowCauseGate, clipFlow(failure, 200)
		}
	}
	if strings.HasPrefix(d.BlockedReason, ReasonRebaseRoundsExhausted) {
		return FlowCauseDevMoved, clipFlow(d.BlockedReason, 200)
	}
	for _, rec := range d.Steps {
		if rec.State != StateFailed && rec.State != StateStopped {
			continue
		}
		reason := strings.TrimSpace(rec.Reason)
		switch {
		case strings.Contains(strings.ToLower(reason), "rebase needed"):
			return FlowCauseDevMoved, clipFlow(rec.ID+" failed: "+reason, 200)
		case providerlimit.Is(reason) || providerlimit.IsExhausted(reason):
			return FlowCauseProvider, clipFlow(rec.ID+" failed: "+reason, 200)
		case rec.Type == StepRun || rec.Type == StepDeliver:
			return FlowCauseGate, clipFlow(rec.ID+" failed: "+reason, 200)
		}
	}
	for _, rec := range d.Steps {
		if verdict, _ := rec.Outcome["verdict"].(string); verdict == VerdictBlock {
			return FlowCauseCode, clipFlow(firstFlow(d.BlockedReason, "review verdict block"), 200)
		}
	}
	if strings.HasPrefix(d.BlockedReason, ReasonRoundsExhausted) {
		return FlowCauseCode, clipFlow(d.BlockedReason, 200)
	}
	if d.BlockedReason != "" {
		return FlowCauseGate, clipFlow(d.BlockedReason, 200)
	}
	return "", ""
}

func flowDelivery(d Delivery, now int64) FlowDeliveryReport {
	row := FlowDeliveryReport{ID: d.ID, Workflow: d.AutomationName, State: d.State, Head: shortFlowHead(d.Head), Round: d.Round, Reruns: d.Reruns, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt}
	start := d.StartedAt
	if start == 0 {
		start = d.CreatedAt
	}
	end := d.FinishedAt
	if end == 0 && !Terminal(d.State) {
		end = now
	}
	if end > start && start > 0 {
		row.DurationMS = end - start
	}
	for _, rec := range d.Steps {
		def, _ := d.step(rec.ID)
		row.LimitWaits += rec.LimitWaits
		switch rec.Type {
		case StepAgent:
			if rec.StartedAt > 0 && flowReviewStep(def, rec) && rec.State != StateSkipped && rec.State != StatePending {
				row.Reviews++
				if verdict, _ := rec.Outcome["verdict"].(string); verdict != "" {
					row.Verdicts = append(row.Verdicts, verdict)
				}
			}
		case StepRun:
			if rec.State == StatePending || rec.State == StateSkipped {
				continue
			}
			row.Gates = append(row.Gates, FlowGate{Step: rec.ID, State: rec.State, ExitCode: rec.ExitCode, Reason: clipFlow(rec.Reason, 160)})
		}
	}
	row.Cause, row.CauseDetail = FlowDeliveryCause(d)
	if row.Cause == FlowCauseDevMoved {
		row.Rebases = 1
	}
	return row
}

// flowTopWasteLines ranks the job's largest costs: each line's score is its
// share of the job's time (or of its tokens, for context growth); counts
// without time of their own rank last.
func flowTopWasteLines(r FlowReport) []FlowWaste {
	return rankFlowWaste(flowWasteCandidates(r))
}

// rankFlowWaste orders lines by score (then value) and keeps the top ones.
func rankFlowWaste(lines []FlowWaste) []FlowWaste {
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Score != lines[j].Score {
			return lines[i].Score > lines[j].Score
		}
		return lines[i].Value > lines[j].Value
	})
	if len(lines) > flowTopWaste {
		lines = lines[:flowTopWaste]
	}
	return lines
}

// flowWasteCandidates are every waste line of a report, unranked.
func flowWasteCandidates(r FlowReport) []FlowWaste {
	lines := []FlowWaste{}
	wall := float64(max(r.WallMS, 1))
	addTime := func(kind string, ms int64, format string) {
		if ms < 60_000 {
			return
		}
		lines = append(lines, FlowWaste{Kind: kind, Line: fmt.Sprintf(format, FlowMinutes(ms)), Value: ms, Score: float64(ms) / wall})
	}
	addTime(FlowPhaseProviderLimit, r.Phases.ProviderLimitMS, "%s waiting on provider limit")
	addTime(FlowPhaseStalled, r.Phases.StalledMS, "%s stalled (no run and no next step)")
	addTime(FlowPhaseNeedsYou, r.Phases.NeedsYouMS, "%s waiting on a person")
	if r.Phases.TestingMS >= 60_000 {
		agent := r.Phases.CodingMS + r.Phases.TestingMS + r.Phases.RebasingMS
		share := int64(0)
		if agent > 0 {
			share = r.Phases.TestingMS * 100 / agent
		}
		lines = append(lines, FlowWaste{Kind: FlowPhaseTesting, Line: fmt.Sprintf("%s running tests (%d%% of agent time)", FlowMinutes(r.Phases.TestingMS), share), Value: r.Phases.TestingMS, Score: float64(r.Phases.TestingMS) / wall})
	}
	if r.Rebases > 0 {
		lines = append(lines, FlowWaste{Kind: FlowPhaseRebasing, Line: fmt.Sprintf("%d %s because dev moved (%s)", r.Rebases, plural(r.Rebases, "rebase", "rebases"), FlowMinutes(r.Phases.RebasingMS)), Value: int64(r.Rebases), Score: float64(r.Phases.RebasingMS) / wall})
	}
	// Reviews of the same head.
	byHead, reviewMS := map[string]int{}, r.Phases.ReviewingMS
	for _, d := range r.Deliveries {
		if d.Head != "" {
			byHead[d.Head] += d.Reviews
		}
	}
	worstHead, worst := "", 0
	for head, n := range byHead {
		if n > worst || (n == worst && head < worstHead) {
			worstHead, worst = head, n
		}
	}
	if worst >= 2 {
		share := 0.0
		if r.Reviews > 0 {
			share = float64(reviewMS) * float64(worst-1) / float64(r.Reviews) / wall
		}
		lines = append(lines, FlowWaste{Kind: "repeat_reviews", Line: fmt.Sprintf("%d full reviews of the same head %s", worst, worstHead), Value: int64(worst), Score: share})
	} else if r.Reviews >= 3 {
		lines = append(lines, FlowWaste{Kind: "reviews", Line: fmt.Sprintf("%d reviews (%s)", r.Reviews, FlowMinutes(reviewMS)), Value: int64(r.Reviews), Score: float64(reviewMS) / wall / 2})
	}
	// Context growth across runs: the tokens re-read above the smallest
	// run's context per request, as a share of all input tokens (token
	// costs count half against time).
	low, high := int64(0), int64(0)
	for _, run := range r.Runs {
		if run.ContextPerRequest <= 0 {
			continue
		}
		if low == 0 || run.ContextPerRequest < low {
			low = run.ContextPerRequest
		}
		high = max(high, run.ContextPerRequest)
	}
	if low > 0 && high >= 2*low && high >= 50_000 {
		total := float64(max(r.Tokens.Input+r.Tokens.CacheRead+r.Tokens.CacheWrite, 1))
		excess := int64(0)
		for _, run := range r.Runs {
			if run.ContextPerRequest > low {
				excess += (run.ContextPerRequest - low) * int64(max(run.Tokens.Requests, 1))
			}
		}
		lines = append(lines, FlowWaste{Kind: "context_growth", Line: fmt.Sprintf("context re-read grew from %s to %s per call", flowTokensText(low), flowTokensText(high)), Value: high, Score: float64(excess) / total / 2})
	}
	// The run that ran tests the most.
	most, mostMS := 0, int64(0)
	for _, run := range r.Runs {
		if run.TestRuns > most {
			most, mostMS = run.TestRuns, run.TestMS
		}
	}
	if most >= 5 {
		lines = append(lines, FlowWaste{Kind: "test_repeats", Line: fmt.Sprintf("tests run %d times in one run", most), Value: int64(most), Score: float64(mostMS) / wall / 2})
	}
	if r.Interventions > 0 {
		lines = append(lines, FlowWaste{Kind: "interventions", Line: fmt.Sprintf("%d hand %s", r.Interventions, plural(r.Interventions, "intervention", "interventions")), Value: int64(r.Interventions), Score: 0})
	}
	return lines
}

// FlowMinutes renders milliseconds as minutes (or hours and minutes).
func FlowMinutes(ms int64) string {
	minutes := (ms + 30_000) / 60_000
	if minutes >= 120 {
		return fmt.Sprintf("%dh %02dmin", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%d min", minutes)
}

func flowTokensText(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1000:
		return fmt.Sprintf("%dk", (tokens+500)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortFlowHead(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 12 {
		return head[:12]
	}
	return head
}

func firstFlow(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clipFlow(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.ToValidUTF8(text, "")), " ")
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return string(r[:limit-1]) + "…"
}

// FlowMarkdown is the report's short Markdown rendering: the text the MCP
// answer carries and a client shows as it is.
func FlowMarkdown(r FlowReport) string {
	var b strings.Builder
	title := firstFlow(r.Title, r.TaskID, r.JobID)
	fmt.Fprintf(&b, "### Flow: %s\n\n", title)
	if r.WallMS == 0 {
		b.WriteString("No runs or deliveries recorded yet.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%s from first to last activity; %s in, %s out over %d requests.\n\n", FlowMinutes(r.WallMS), flowTokensText(r.Tokens.Input+r.Tokens.CacheRead+r.Tokens.CacheWrite), flowTokensText(r.Tokens.Output), r.Tokens.Requests)
	writeFlowPhases(&b, r.Phases)
	writeFlowWaste(&b, r.TopWaste)
	if len(r.Runs) > 0 {
		b.WriteString("\n**Runs**\n\n| run | phase | min | read/edit/cmd/commit | tests | result KB | ctx/request | tokens in/out |\n|---|---|---|---|---|---|---|---|\n")
		for i, run := range r.Runs {
			if i == flowMarkdownRuns {
				fmt.Fprintf(&b, "| … %d more | | | | | | | |\n", len(r.Runs)-flowMarkdownRuns)
				break
			}
			fmt.Fprintf(&b, "| %d %s | %s | %s | %d/%d/%d/%d | %d | %d | %s | %s/%s |\n", i+1, firstFlow(run.Role, "run"), run.Phase, strings.TrimSuffix(FlowMinutes(run.DurationMS), " min"),
				run.Tools.Read, run.Tools.Edit, run.Tools.Command, run.Tools.Commit, run.TestRuns, run.ToolResultBytes/1024, flowTokensText(run.ContextPerRequest),
				flowTokensText(run.Tokens.Input+run.Tokens.CacheRead+run.Tokens.CacheWrite), flowTokensText(run.Tokens.Output))
		}
	}
	if len(r.Deliveries) > 0 {
		b.WriteString("\n**Deliveries**\n\n")
		for _, d := range r.Deliveries {
			fmt.Fprintf(&b, "- %s round %d: %s; %d %s", firstFlow(d.Workflow, d.ID), d.Round, d.State, d.Reviews, plural(d.Reviews, "review", "reviews"))
			if len(d.Verdicts) > 0 {
				fmt.Fprintf(&b, " (%s)", strings.Join(d.Verdicts, ", "))
			}
			for _, g := range d.Gates {
				fmt.Fprintf(&b, "; %s %s", g.Step, g.State)
			}
			if d.Cause != "" {
				fmt.Fprintf(&b, "; cause: %s", d.Cause)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func writeFlowPhases(b *strings.Builder, p FlowPhases) {
	parts := []string{}
	for _, item := range []struct {
		name string
		ms   int64
	}{{"coding", p.CodingMS}, {"testing", p.TestingMS}, {"reviewing", p.ReviewingMS}, {"provider limit", p.ProviderLimitMS}, {"needs you", p.NeedsYouMS}, {"stalled", p.StalledMS}, {"gates", p.GateMS}, {"rebasing", p.RebasingMS}} {
		if item.ms > 0 {
			parts = append(parts, item.name+" "+FlowMinutes(item.ms))
		}
	}
	if len(parts) > 0 {
		b.WriteString("**Time:** " + strings.Join(parts, " · ") + "\n")
	}
}

func writeFlowWaste(b *strings.Builder, waste []FlowWaste) {
	if len(waste) == 0 {
		return
	}
	b.WriteString("\n**Top waste**\n\n")
	for i, w := range waste {
		fmt.Fprintf(b, "%d. %s\n", i+1, w.Line)
	}
}

// FlowRollup is a project's flow over a period: the phases and tokens of
// its jobs within the period, and the top waste across them.
type FlowRollup struct {
	ProjectID string       `json:"project_id"`
	Period    string       `json:"period"`
	From      int64        `json:"from_ms"`
	To        int64        `json:"to_ms"`
	Tasks     int          `json:"tasks"`
	Phases    FlowPhases   `json:"phases"`
	Tokens    FlowTokens   `json:"tokens"`
	Rebases   int          `json:"rebases"`
	Reviews   int          `json:"reviews"`
	TopWaste  []FlowWaste  `json:"top_waste"`
	ByTask    []FlowByTask `json:"by_task"`
	Partial   bool         `json:"partial,omitempty"`
}

// FlowByTask is one task's line in a roll-up.
type FlowByTask struct {
	TaskID   string `json:"task_id"`
	Title    string `json:"title,omitempty"`
	WallMS   int64  `json:"wall_ms"`
	WasteTop string `json:"top_waste,omitempty"`
}

// BuildFlowRollup sums task reports of one period. Each report must already
// be bounded to the period (FlowSource.From/To).
func BuildFlowRollup(projectID, period string, from, to int64, reports []FlowReport) FlowRollup {
	out := FlowRollup{ProjectID: projectID, Period: period, From: from, To: to, TopWaste: []FlowWaste{}, ByTask: []FlowByTask{}}
	sum := FlowReport{}
	// Lines that compare runs (context growth, tests in one run) are a
	// task's own: the roll-up keeps the largest of each, naming its task.
	type taskLine struct {
		line FlowWaste
		wall int64
	}
	perTask := map[string]taskLine{}
	for _, r := range reports {
		if r.WallMS == 0 {
			continue
		}
		out.Tasks++
		out.Phases.Add(r.Phases)
		out.Tokens.Add(r.Tokens)
		out.Rebases += r.Rebases
		out.Reviews += r.Reviews
		out.Partial = out.Partial || r.Partial
		row := FlowByTask{TaskID: r.TaskID, Title: r.Title, WallMS: r.WallMS}
		if len(r.TopWaste) > 0 {
			row.WasteTop = r.TopWaste[0].Line
		}
		out.ByTask = append(out.ByTask, row)
		sum.WallMS += r.WallMS
		sum.Interventions += r.Interventions
		sum.Deliveries = append(sum.Deliveries, r.Deliveries...)
		for _, line := range flowWasteCandidates(r) {
			if line.Kind != "context_growth" && line.Kind != "test_repeats" {
				continue
			}
			if best, ok := perTask[line.Kind]; !ok || line.Value > best.line.Value {
				line.Line += " (" + firstFlow(r.Title, r.TaskID) + ")"
				perTask[line.Kind] = taskLine{line, r.WallMS}
			}
		}
	}
	sum.Phases, sum.Tokens, sum.Rebases, sum.Reviews = out.Phases, out.Tokens, out.Rebases, out.Reviews
	lines := flowWasteCandidates(sum)
	for _, kind := range []string{"context_growth", "test_repeats"} {
		if best, ok := perTask[kind]; ok {
			// Its share of its task, scaled by the task's share of the
			// period's task time.
			best.line.Score *= float64(best.wall) / float64(max(sum.WallMS, 1))
			lines = append(lines, best.line)
		}
	}
	out.TopWaste = rankFlowWaste(lines)
	sort.SliceStable(out.ByTask, func(i, j int) bool { return out.ByTask[i].WallMS > out.ByTask[j].WallMS })
	return out
}

// FlowRollupMarkdown is the roll-up's short Markdown rendering.
func FlowRollupMarkdown(r FlowRollup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Flow: project %s, %s\n\n", r.ProjectID, r.Period)
	if r.Tasks == 0 {
		b.WriteString("No task activity in this period.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d %s; %s of task time; %s in, %s out over %d requests.\n\n", r.Tasks, plural(r.Tasks, "task", "tasks"), FlowMinutes(r.Phases.Total()), flowTokensText(r.Tokens.Input+r.Tokens.CacheRead+r.Tokens.CacheWrite), flowTokensText(r.Tokens.Output), r.Tokens.Requests)
	writeFlowPhases(&b, r.Phases)
	writeFlowWaste(&b, r.TopWaste)
	if len(r.ByTask) > 0 {
		b.WriteString("\n**Tasks**\n\n")
		for i, t := range r.ByTask {
			if i == 10 {
				fmt.Fprintf(&b, "- … %d more\n", len(r.ByTask)-10)
				break
			}
			fmt.Fprintf(&b, "- %s: %s", firstFlow(t.Title, t.TaskID), FlowMinutes(t.WallMS))
			if t.WasteTop != "" {
				fmt.Fprintf(&b, "; %s", t.WasteTop)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// FlowPeriod resolves a roll-up period (today, 7d, 30d) to its bounds.
// today starts at the current UTC day.
func FlowPeriod(period string, now time.Time) (name string, from, to int64, err error) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "", "7d", "week":
		return "7d", now.Add(-7 * 24 * time.Hour).UnixMilli(), now.UnixMilli(), nil
	case "today", "1d":
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return "today", day.UnixMilli(), now.UnixMilli(), nil
	case "30d", "month":
		return "30d", now.Add(-30 * 24 * time.Hour).UnixMilli(), now.UnixMilli(), nil
	}
	return "", 0, 0, fmt.Errorf("period %q is not one of today, 7d, 30d", period)
}
