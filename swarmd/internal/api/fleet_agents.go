package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The fleet's agents at work: one read of which agent runs are active right
// now on this Swarm and on each linked machine, what each is doing, and how
// the day went.
//
//	GET /v3/fleet/agents                 this Swarm and its linked machines
//	GET /v3/fleet/agents?scope=machine   this Swarm only (what a home reads
//	                                     from a linked machine, through the link)
//	&machine=NAME                        one machine only (this Swarm's name or
//	                                     a linked machine's)
//	&since=<unix ms>                     where "today" starts (default: the UTC day)
//
// The same service (FleetAgents) answers Swarm Control's swarm_agents_at_work
// (control_mcp_jobs.go, through this route) and the Orchestrator's
// manage_projects action=agents_at_work (FleetAgentsAtWork).
//
// A run is active while it holds an execution lease
// (executioncapacity.Manager.HeldRuns), the same live set swarm_status counts
// as running_now. What it does comes from its session's tool events; a task's
// workflow step from the task's progress (taskProgressFor). A linked machine
// is read only while linked.{name}.work is ask or auto; one that cannot be
// read is listed with why, never dropped. What a machine sends is untrusted:
// only the known fields are kept and strings are clipped.

const (
	fleetAgentsPath = "/v3/fleet/agents"

	fleetAgentsMachineTimeout = 5 * time.Second
	fleetAgentsResponseLimit  = 4 << 20
	// Session events read back per run for its tool calls; a run with more
	// is marked partial.
	fleetAgentsEventScan = 2048
	fleetAgentsEventPage = 256
	fleetAgentsTimeline  = 12
	fleetAgentsFiles     = 24
	fleetAgentsRecent    = 8
	fleetAgentsMaxAgents = 256
	fleetAgentsTarget    = 80
	fleetAgentsField     = 256
	fleetAgentsProjects  = 200
)

// Workflow steps a task's run can stand at, in pipeline order.
const (
	fleetAgentStepCode   = "code"
	fleetAgentStepReview = "review"
	fleetAgentStepFix    = "fix"
	fleetAgentStepPush   = "push"
	fleetAgentStepLanded = "landed"
)

type fleetAgentTool struct {
	// Kind is read, edit, command, commit or other.
	Kind   string `json:"kind"`
	Tool   string `json:"tool"`
	Target string `json:"target,omitempty"`
	At     int64  `json:"at"`
	// State is running, done or failed.
	State string `json:"state"`
}

type fleetAgentCounts struct {
	Reads    int `json:"reads"`
	Edits    int `json:"edits"`
	Commands int `json:"commands"`
	Commits  int `json:"commits"`
	Other    int `json:"other"`
	// LastMinute is the tool calls started in the minute before the read.
	LastMinute int `json:"last_minute"`
}

type fleetAgentStage struct {
	Step     string `json:"step"`
	Round    int    `json:"round,omitempty"`
	Workflow string `json:"workflow,omitempty"`
}

type fleetAgentRun struct {
	Machine    string           `json:"machine"`
	Agent      string           `json:"agent"`
	SessionID  string           `json:"session_id"`
	RunID      string           `json:"run_id"`
	Title      string           `json:"title"`
	ProjectID  string           `json:"project_id,omitempty"`
	TaskID     string           `json:"task_id,omitempty"`
	TaskTitle  string           `json:"task_title,omitempty"`
	WorkerID   string           `json:"worker_id,omitempty"`
	WorkerName string           `json:"worker_name,omitempty"`
	StartedAt  int64            `json:"started_at"`
	Now        *fleetAgentTool  `json:"now,omitempty"`
	Timeline   []fleetAgentTool `json:"timeline"`
	Counts     fleetAgentCounts `json:"counts"`
	Files      []string         `json:"files"`
	FilesTotal int              `json:"files_total"`
	Stage      *fleetAgentStage `json:"stage,omitempty"`
	// Partial: the run has more tool events than were read; counts and
	// files are of the newest ones.
	Partial bool `json:"partial,omitempty"`
}

type fleetAgentFinished struct {
	Machine   string `json:"machine"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	At        int64  `json:"at"`
}

type fleetAgentMachine struct {
	Name string `json:"name"`
	// State is online, offline (Reason says why) or not_shown (Reason says why).
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Agents      int    `json:"agents"`
	LandedToday int    `json:"landed_today"`
	FailedToday int    `json:"failed_today"`
}

type fleetAgentTotals struct {
	Agents      int `json:"agents"`
	LastMinute  int `json:"tool_calls_last_minute"`
	LandedToday int `json:"landed_today"`
	FailedToday int `json:"failed_today"`
}

type fleetAgentsView struct {
	Home     string               `json:"home"`
	At       int64                `json:"at"`
	Since    int64                `json:"since"`
	Agents   []fleetAgentRun      `json:"agents"`
	Machines []fleetAgentMachine  `json:"machines"`
	Recent   []fleetAgentFinished `json:"recent"`
	Totals   fleetAgentTotals     `json:"totals"`
}

func (s *Server) handleFleetAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	if !s.requireScope(w, r, "sessions:read") {
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read the fleet's agents"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("sessions unavailable"))
		return
	}
	query := r.URL.Query()
	localOnly := false
	switch query.Get("scope") {
	case "", "fleet":
	case "machine":
		localOnly = true
	default:
		writeError(w, http.StatusBadRequest, errors.New("scope is fleet (default) or machine"))
		return
	}
	now := time.Now().UnixMilli()
	since := fleetAgentsUTCDay(now)
	if raw := strings.TrimSpace(query.Get("since")); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 || v > now {
			writeError(w, http.StatusBadRequest, errors.New("since is a Unix time in milliseconds, not in the future"))
			return
		}
		since = v
	}
	view, err := s.FleetAgents(r.Context(), principal, now, since, localOnly, strings.TrimSpace(query.Get("machine")))
	var unknown fleetAgentsUnknownMachine
	switch {
	case errors.As(err, &unknown):
		writeError(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// fleetAgentsUnknownMachine is a machine filter that names no machine: not
// this Swarm and not linked for the account.
type fleetAgentsUnknownMachine struct {
	machine string
	known   []string
}

func (e fleetAgentsUnknownMachine) Error() string {
	if len(e.known) == 0 {
		return "no machine is named " + strconv.Quote(e.machine)
	}
	return "no machine is named " + strconv.Quote(e.machine) + "; machines: " + strings.Join(e.known, ", ")
}

// FleetAgentsAtWork is the Orchestrator's read (manage_projects
// action=agents_at_work): the same answer as GET /v3/fleet/agents for this
// Swarm and its linked machines, or one machine; today is the UTC day.
func (s *Server) FleetAgentsAtWork(ctx context.Context, p identity.Principal, machine string) (json.RawMessage, error) {
	if s.sessions == nil {
		return nil, errors.New("sessions unavailable")
	}
	now := time.Now().UnixMilli()
	view, err := s.FleetAgents(ctx, p, now, fleetAgentsUTCDay(now), false, strings.TrimSpace(machine))
	if err != nil {
		return nil, err
	}
	return json.Marshal(view)
}

func fleetAgentsUTCDay(now int64) int64 {
	t := time.UnixMilli(now).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).UnixMilli()
}

// FleetAgents is the agents at work on this Swarm and, unless localOnly, on
// each machine linked for the principal's account, read in parallel. only,
// when set, reads that one machine (this Swarm's name or a linked one's).
func (s *Server) FleetAgents(ctx context.Context, p identity.Principal, now, since int64, localOnly bool, only string) (fleetAgentsView, error) {
	home := s.fleetName()
	view := fleetAgentsView{Home: home, At: now, Since: since, Agents: []fleetAgentRun{}, Machines: []fleetAgentMachine{}, Recent: []fleetAgentFinished{}}

	var roster []fleetActivityMachine
	if !localOnly && home != "" {
		if _, linked, err := s.fleetActivityRoster(p); err == nil {
			roster = linked
		} else {
			return view, err
		}
	}
	if only != "" && only != home {
		known := []string{}
		if home != "" {
			known = append(known, home)
		}
		var picked []fleetActivityMachine
		for _, m := range roster {
			known = append(known, m.name)
			if m.name == only {
				picked = append(picked, m)
			}
		}
		if len(picked) == 0 {
			return view, fleetAgentsUnknownMachine{machine: only, known: known}
		}
		roster = picked
	} else {
		agents, err := s.localAgentsNow(p, home, now)
		if err != nil {
			return view, err
		}
		recent, landed, failed := s.localAgentsFinished(p, home, now, since)
		view.Agents = append(view.Agents, agents...)
		view.Recent = append(view.Recent, recent...)
		view.Machines = append(view.Machines, fleetAgentMachine{Name: home, State: "online", Agents: len(agents), LandedToday: landed, FailedToday: failed})
		if only != "" {
			roster = nil
		}
	}
	type answer struct {
		machine fleetAgentMachine
		view    fleetAgentsView
	}
	answers := make([]answer, len(roster))
	var wg sync.WaitGroup
	for i, m := range roster {
		if !m.shown {
			answers[i].machine = fleetAgentMachine{Name: m.name, State: "not_shown", Reason: m.reason}
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			remote, err := s.fleetAgentsMachine(ctx, p, name, since)
			if err != nil {
				answers[i].machine = fleetAgentMachine{Name: name, State: "offline", Reason: fleetActivityShortError(err)}
				return
			}
			answers[i] = answer{machine: fleetAgentMachine{Name: name, State: "online", Agents: len(remote.Agents), LandedToday: remote.Totals.LandedToday, FailedToday: remote.Totals.FailedToday}, view: remote}
		}(i, m.name)
	}
	wg.Wait()
	for _, a := range answers {
		view.Machines = append(view.Machines, a.machine)
		view.Agents = append(view.Agents, a.view.Agents...)
		view.Recent = append(view.Recent, a.view.Recent...)
	}
	sort.SliceStable(view.Recent, func(i, j int) bool { return view.Recent[i].At > view.Recent[j].At })
	if len(view.Recent) > fleetAgentsRecent {
		view.Recent = view.Recent[:fleetAgentsRecent]
	}
	for _, a := range view.Agents {
		view.Totals.LastMinute += a.Counts.LastMinute
	}
	for _, m := range view.Machines {
		view.Totals.Agents += m.Agents
		view.Totals.LandedToday += m.LandedToday
		view.Totals.FailedToday += m.FailedToday
	}
	return view, nil
}

// localAgentsNow is this Swarm's active runs the principal may read.
func (s *Server) localAgentsNow(p identity.Principal, machine string, now int64) ([]fleetAgentRun, error) {
	out := []fleetAgentRun{}
	manager := s.ExecutionCapacity()
	if manager == nil {
		return out, nil
	}
	store := s.sessions.Store()
	for _, held := range manager.HeldRuns() {
		if held.Account != p.AccountScopeID || strings.TrimSpace(held.Session) == "" {
			continue
		}
		allowed, err := s.authorizeReadableSessionV3Access(p, held.Session)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		session, found, err := s.sessions.GetSession(held.Session)
		if err != nil {
			return nil, err
		}
		if !found || session.AccountScopeID != p.AccountScopeID {
			continue
		}
		run := fleetAgentRun{Machine: machine, SessionID: session.ID, RunID: held.Run, Title: session.Title, Timeline: []fleetAgentTool{}, Files: []string{}}
		meta := session.Metadata
		run.Agent = firstNonEmptyString(fleetAgentsMeta(meta, "resolved_agent_name"), fleetAgentsMeta(meta, "agent_name"), fleetAgentsMeta(meta, "role"), "swarm")
		run.WorkerID, run.WorkerName = fleetAgentsMeta(meta, "worker_id"), fleetAgentsMeta(meta, "worker_name")
		if lc := session.Lifecycle; lc != nil && (lc.RunID == held.Run || held.Run == "") {
			run.StartedAt = lc.StartedAt
		}
		projectID, taskID := fleetAgentsMeta(meta, "project_id"), fleetAgentsMeta(meta, "task_id")
		if projectID != "" && taskID != "" && store != nil {
			if task, found, err := store.GetProjectTask(p.AccountScopeID, projectID, taskID); err == nil && found && task != nil {
				run.ProjectID, run.TaskID, run.TaskTitle = task.ProjectID, task.ID, task.Title
				run.WorkerID = firstNonEmptyString(run.WorkerID, task.WorkerID)
				run.WorkerName = firstNonEmptyString(run.WorkerName, task.WorkerName)
				run.Stage = fleetAgentStageOf(*task, s.taskProgressFor(p.AccountScopeID, *task, now), s.fleetAgentsWorkflowApplies(p.AccountScopeID, *task))
			}
		}
		s.fleetAgentsTools(&run, now)
		out = append(out, run)
		if len(out) >= fleetAgentsMaxAgents {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out, nil
}

func fleetAgentsMeta(meta map[string]any, key string) string {
	v, _ := meta[key].(string)
	return strings.TrimSpace(v)
}

// fleetAgentsWorkflowApplies says whether an enabled workflow would take the
// task's work when its run ends (jobautomation.Automation.Matches, the rule
// the job end applies), so a first run shows the pipeline's code step.
func (s *Server) fleetAgentsWorkflowApplies(account string, task pebblestore.ProjectTaskRecord) bool {
	store, err := s.jobAutomationStore()
	if err != nil {
		return false
	}
	list, err := store.List(account)
	if err != nil {
		return false
	}
	result := &jobautomation.Result{V: jobautomation.ResultVersion, JobID: taskProgressJobID(task), Kind: jobautomation.KindTask, Role: task.Agent,
		ProjectID: task.ProjectID, Folder: firstNonEmptyString(task.SourceWorkspace.Path, task.WorkspacePath), Status: jobautomation.StatusSucceeded}
	for _, a := range list {
		if a.Matches(result) {
			return true
		}
	}
	return false
}

// fleetAgentStageOf is where a task's run stands in its workflow: code
// (the first run, a workflow applies), review (a workflow's reviewer runs,
// or the run is a reviewer's own task), fix (sent back: round n), push (a
// run or deliver step after the review) or landed. Nil when no workflow
// takes the task.
func fleetAgentStageOf(task pebblestore.ProjectTaskRecord, progress *taskProgress, applies bool) *fleetAgentStage {
	if pebblestore.ProjectTaskWorkflowSpawned(task) {
		return &fleetAgentStage{Step: fleetAgentStepReview}
	}
	if progress == nil {
		return nil
	}
	if progress.TaskState == pebblestore.ProjectTaskStateLanded {
		return &fleetAgentStage{Step: fleetAgentStepLanded}
	}
	if progress.Workflow == nil || len(progress.Workflow.Deliveries) == 0 {
		if applies && taskStatusClass(task.Status) == progressWorking {
			return &fleetAgentStage{Step: fleetAgentStepCode}
		}
		return nil
	}
	d := progress.Workflow.Deliveries[0]
	stage := &fleetAgentStage{Workflow: d.Workflow}
	switch {
	case d.State == workflowSentBack:
		stage.Step, stage.Round = fleetAgentStepFix, max(d.Round, 1)
		if d.SentBack != nil && d.SentBack.Round > 0 {
			stage.Round = d.SentBack.Round
		}
	case d.StepType == jobautomation.StepAgent:
		stage.Step = fleetAgentStepReview
	case d.StepType == jobautomation.StepReopen:
		stage.Step, stage.Round = fleetAgentStepFix, max(d.Round, 1)
	case d.StepType == jobautomation.StepRun || d.StepType == jobautomation.StepDeliver:
		stage.Step = fleetAgentStepPush
	default:
		stage.Step = fleetAgentStepCode
	}
	// The agent runs again on a task its workflow already took: a fix.
	if taskStatusClass(task.Status) == progressWorking && d.Round > 0 && stage.Step != fleetAgentStepFix {
		stage.Step, stage.Round = fleetAgentStepFix, d.Round
	}
	return stage
}

// fleetAgentToolKind sorts a tool by what it does to the work.
func fleetAgentToolKind(name string) string {
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

// fleetAgentToolTarget is a short, one-line target for a tool call: a path,
// a query, a command's first line or a commit message's.
func fleetAgentToolTarget(name, arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	pick := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	target := ""
	switch fleetAgentToolKind(name) {
	case "read", "edit":
		target = pick("path", "file_path", "query", "pattern", "url")
	case "command":
		target = pick("command", "cmd")
	case "commit":
		target = pick("message")
	default:
		target = pick("path", "query", "action")
	}
	line, _, _ := strings.Cut(strings.TrimSpace(target), "\n")
	return fleetAgentsClip(strings.Join(strings.Fields(line), " "), fleetAgentsTarget)
}

func fleetAgentsClip(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	r := []rune(text)
	return string(r[:limit-1]) + "…"
}

// fleetAgentsTools reads the run's tool calls from its session's events,
// newest first, back to its start or fleetAgentsEventScan events.
func (s *Server) fleetAgentsTools(run *fleetAgentRun, now int64) {
	type payload struct {
		RunID     string `json:"run_id"`
		ToolName  string `json:"tool_name"`
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
		Instance  string `json:"tool_instance_id"`
	}
	ended := map[string]string{}
	calls := []fleetAgentTool{}
	files, seenFile := []string{}, map[string]bool{}
	earliest := int64(0)
	before, scanned, done := uint64(0), 0, false
	for !done && scanned < fleetAgentsEventScan {
		events, err := s.sessions.ListSessionEventsBefore(run.SessionID, before, fleetAgentsEventPage)
		if err != nil || len(events) == 0 {
			done = true
			break
		}
		for _, ev := range events {
			scanned++
			before = ev.Seq
			if run.StartedAt > 0 && ev.TsUnixMs < run.StartedAt {
				done = true
				break
			}
			if !strings.HasPrefix(ev.EventType, "session.tool.") {
				continue
			}
			var p payload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			if run.RunID != "" && p.RunID != "" && p.RunID != run.RunID {
				// An earlier run's tools: this run starts after them.
				done = true
				break
			}
			call := firstNonEmptyString(p.Instance, p.CallID)
			switch ev.EventType {
			case "session.tool.completed":
				ended[call] = "done"
				if fleetAgentToolKind(p.ToolName) == "commit" {
					run.Counts.Commits++
				}
			case "session.tool.failed", "session.tool.cancelled", "session.tool.canceled":
				ended[call] = "failed"
			case "session.tool.started":
				earliest = ev.TsUnixMs
				kind := fleetAgentToolKind(p.ToolName)
				tool := fleetAgentTool{Kind: kind, Tool: fleetAgentsClip(p.ToolName, 64), Target: fleetAgentToolTarget(p.ToolName, p.Arguments), At: ev.TsUnixMs, State: "running"}
				if state, ok := ended[call]; ok {
					tool.State = state
				}
				calls = append(calls, tool)
				switch kind {
				case "read":
					run.Counts.Reads++
				case "edit":
					run.Counts.Edits++
					if tool.Target != "" && !seenFile[tool.Target] {
						seenFile[tool.Target] = true
						files = append(files, tool.Target)
					}
				case "command":
					run.Counts.Commands++
				case "commit":
				default:
					run.Counts.Other++
				}
				if now-ev.TsUnixMs <= int64(time.Minute/time.Millisecond) {
					run.Counts.LastMinute++
				}
			}
			if scanned >= fleetAgentsEventScan {
				break
			}
		}
		if len(events) < fleetAgentsEventPage {
			done = true
		}
	}
	run.Partial = !done && scanned >= fleetAgentsEventScan
	if run.StartedAt == 0 {
		run.StartedAt = earliest
	}
	if len(calls) > 0 {
		latest := calls[0]
		run.Now = &latest
	}
	timeline := calls
	if len(timeline) > fleetAgentsTimeline {
		timeline = timeline[:fleetAgentsTimeline]
	}
	for i := len(timeline) - 1; i >= 0; i-- {
		run.Timeline = append(run.Timeline, timeline[i])
	}
	run.FilesTotal = len(files)
	if len(files) > fleetAgentsFiles {
		files = files[:fleetAgentsFiles]
	}
	run.Files = files
}

// fleetAgentsFinishedTTL is how long one machine's finished-task summary
// (recent tasks, today's landed and failed) is served from memory. The read
// is polled every few seconds by several clients; the summary is history,
// so a few seconds' lag is fine, while the agents at work are read fresh.
const fleetAgentsFinishedTTL = 5 * time.Second

// fleetAgentsFinishedCacheMax bounds the summaries kept (one per account,
// machine name and since).
const fleetAgentsFinishedCacheMax = 64

type fleetAgentsFinishedKey struct {
	account, machine string
	since            int64
}

type fleetAgentsFinishedEntry struct {
	at             time.Time
	recent         []fleetAgentFinished
	landed, failed int
}

type fleetAgentsFinishedCache struct {
	mu      sync.Mutex
	entries map[fleetAgentsFinishedKey]fleetAgentsFinishedEntry
	// now is the cache's clock; nil is time.Now (a test moves it).
	now func() time.Time
	// progressReads counts the task progress reads (deliveries,
	// evaluation, run state) the store reads have made, for tests.
	progressReads int
}

// localAgentsFinished is this Swarm's latest finished tasks (newest first)
// and how many landed and failed since `since`, kept for
// fleetAgentsFinishedTTL. Callers that poll share one read; the lock makes
// concurrent cold reads wait for that one instead of each scanning.
func (s *Server) localAgentsFinished(p identity.Principal, machine string, now, since int64) ([]fleetAgentFinished, int, int) {
	cache := &s.fleetAgentsFinished
	key := fleetAgentsFinishedKey{account: p.AccountScopeID, machine: machine, since: since}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	at := time.Now()
	if cache.now != nil {
		at = cache.now()
	}
	if entry, ok := cache.entries[key]; ok && at.Sub(entry.at) >= 0 && at.Sub(entry.at) < fleetAgentsFinishedTTL {
		return append([]fleetAgentFinished{}, entry.recent...), entry.landed, entry.failed
	}
	recent, landed, failed := s.readAgentsFinished(p, machine, now, since, &cache.progressReads)
	if cache.entries == nil {
		cache.entries = map[fleetAgentsFinishedKey]fleetAgentsFinishedEntry{}
	}
	for k, entry := range cache.entries {
		if at.Sub(entry.at) >= fleetAgentsFinishedTTL || at.Before(entry.at) {
			delete(cache.entries, k)
		}
	}
	if len(cache.entries) >= fleetAgentsFinishedCacheMax {
		cache.entries = map[fleetAgentsFinishedKey]fleetAgentsFinishedEntry{}
	}
	cache.entries[key] = fleetAgentsFinishedEntry{at: at, recent: append([]fleetAgentFinished{}, recent...), landed: landed, failed: failed}
	return recent, landed, failed
}

// readAgentsFinished reads the finished-task summary from the store. A
// workflow step's own task is left out: its work belongs to the task it
// serves.
//
// The task records are read as stored (ListProjectTaskRecordsForList), not
// hydrated: hydration reads each task's session, run state, terminal event
// and plan document, which on a machine with thousands of finished worker
// runs is most of the store. A task's progress needs none of it; it is
// computed only for the tasks shown and the tasks that ended today.
func (s *Server) readAgentsFinished(p identity.Principal, machine string, now, since int64, progressReads *int) ([]fleetAgentFinished, int, int) {
	recent := []fleetAgentFinished{}
	store := s.sessions.Store()
	if store == nil {
		return recent, 0, 0
	}
	projects, err := store.ListProjects(p.AccountScopeID, fleetAgentsProjects)
	if err != nil {
		return recent, 0, 0
	}
	finished := []pebblestore.ProjectTaskRecord{}
	for _, project := range projects {
		tasks, _, err := store.ListProjectTaskRecordsForList(p.AccountScopeID, project.ID)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			class := taskStatusClass(task.Status)
			if task.Archived || pebblestore.ProjectTaskWorkflowSpawned(task) || class == progressWorking || class == progressWaiting {
				continue
			}
			finished = append(finished, task)
		}
	}
	sort.SliceStable(finished, func(i, j int) bool { return finished[i].UpdatedAt > finished[j].UpdatedAt })
	landed, failed := 0, 0
	for i, task := range finished {
		today := task.UpdatedAt >= since
		shown := i < fleetAgentsRecent
		if !today && !shown {
			break
		}
		if today && taskStatusClass(task.Status) == progressFailed {
			failed++
		}
		task.EnsureTaskAttempts()
		progress := s.taskProgressFor(p.AccountScopeID, task, now)
		*progressReads++
		if today && progress.TaskState == pebblestore.ProjectTaskStateLanded {
			landed++
		}
		if shown {
			recent = append(recent, fleetAgentFinished{Machine: machine, ProjectID: task.ProjectID, TaskID: task.ID, SessionID: task.SessionID, Title: task.Title,
				State: progress.TaskState, Reason: oneLineProgress(progress.TaskStateReason, maxProgressSummary), At: task.UpdatedAt})
		}
	}
	return recent, landed, failed
}

// fleetAgentsMachine reads a linked machine's own agents through the link.
func (s *Server) fleetAgentsMachine(ctx context.Context, p identity.Principal, name string, since int64) (fleetAgentsView, error) {
	var view fleetAgentsView
	client, machine, _, err := s.fleetMachineClient(p.AccountScopeID, name)
	if err != nil {
		return view, err
	}
	ctx, cancel := context.WithTimeout(ctx, fleetAgentsMachineTimeout)
	defer cancel()
	resp, err := client.Do(ctx, http.MethodGet, fleetAgentsPath+"?scope=machine&since="+strconv.FormatInt(since, 10), nil, http.Header{"Accept": {"application/json"}})
	if err == nil && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		err = &fleet.Error{Status: resp.StatusCode}
	}
	s.noteFleetContact(machine, err)
	if err != nil {
		return view, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, fleetAgentsResponseLimit+1))
	if err != nil {
		return view, err
	}
	if len(data) > fleetAgentsResponseLimit || json.Unmarshal(data, &view) != nil || len(view.Agents) > fleetAgentsMaxAgents {
		return fleetAgentsView{}, errFleetActivityInvalid
	}
	return fleetAgentsClean(view, name), nil
}

// fleetAgentsClean keeps what a linked machine sent within bounds, under
// the name this home knows it by.
func fleetAgentsClean(in fleetAgentsView, name string) fleetAgentsView {
	clip := func(v string) string { return fleetAgentsClip(v, fleetAgentsField) }
	// The kind is re-derived from the tool's name, never taken as sent.
	tool := func(t fleetAgentTool) fleetAgentTool {
		state := t.State
		if state != "running" && state != "done" && state != "failed" {
			state = ""
		}
		return fleetAgentTool{Kind: fleetAgentToolKind(t.Tool), Tool: clip(t.Tool), Target: fleetAgentsClip(t.Target, fleetAgentsTarget), At: t.At, State: state}
	}
	out := fleetAgentsView{Home: name, At: in.At, Since: in.Since, Agents: []fleetAgentRun{}, Recent: []fleetAgentFinished{}, Machines: []fleetAgentMachine{}}
	for _, a := range in.Agents {
		run := fleetAgentRun{Machine: name, Agent: clip(a.Agent), SessionID: clip(a.SessionID), RunID: clip(a.RunID), Title: clip(a.Title), ProjectID: clip(a.ProjectID),
			TaskID: clip(a.TaskID), TaskTitle: clip(a.TaskTitle), WorkerID: clip(a.WorkerID), WorkerName: clip(a.WorkerName), StartedAt: a.StartedAt,
			Counts: a.Counts, FilesTotal: a.FilesTotal, Partial: a.Partial, Timeline: []fleetAgentTool{}, Files: []string{}}
		if a.Now != nil {
			now := tool(*a.Now)
			run.Now = &now
		}
		for i, t := range a.Timeline {
			if i >= fleetAgentsTimeline {
				break
			}
			run.Timeline = append(run.Timeline, tool(t))
		}
		for i, f := range a.Files {
			if i >= fleetAgentsFiles {
				break
			}
			run.Files = append(run.Files, fleetAgentsClip(f, fleetAgentsTarget))
		}
		if a.Stage != nil {
			switch a.Stage.Step {
			case fleetAgentStepCode, fleetAgentStepReview, fleetAgentStepFix, fleetAgentStepPush, fleetAgentStepLanded:
				run.Stage = &fleetAgentStage{Step: a.Stage.Step, Round: max(a.Stage.Round, 0), Workflow: clip(a.Stage.Workflow)}
			}
		}
		out.Agents = append(out.Agents, run)
	}
	for i, f := range in.Recent {
		if i >= fleetAgentsRecent {
			break
		}
		out.Recent = append(out.Recent, fleetAgentFinished{Machine: name, ProjectID: clip(f.ProjectID), TaskID: clip(f.TaskID), SessionID: clip(f.SessionID), Title: clip(f.Title), State: clip(f.State), Reason: clip(f.Reason), At: f.At})
	}
	out.Totals = fleetAgentTotals{Agents: len(out.Agents), LandedToday: max(in.Totals.LandedToday, 0), FailedToday: max(in.Totals.FailedToday, 0)}
	for _, a := range out.Agents {
		out.Totals.LastMinute += max(a.Counts.LastMinute, 0)
	}
	return out
}
