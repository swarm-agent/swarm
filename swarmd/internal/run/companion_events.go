package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// The Companion's event loop reads one durable input: its own session's V3
// event log. Every real call path already writes there through
// ApplyV3SessionMutation (the person's messages and queued notes, task-watch
// registrations, outcomes, milestones and cancellations, tool results,
// permission and client-tool confirmation decisions, background and machine
// wakes, completed and failed runs), each event once, in a gapless
// per-session order, idempotent by its mutation key and scoped by the
// session's owner. So there is no second event history: ClassifyCompanionEvent
// says what one event means for the Companion, and MaintainCompanionState
// folds the events after the state's cursor into the materialized state
// (pebblestore.CompanionState), committing state, cursor, focus and topic
// index mark together a page at a time. A crash between pages loses nothing:
// the cursor did not move, and replaying a page writes the same deterministic
// records (a forgotten topic stays forgotten through its tombstone).

// Companion event kinds.
const (
	CompanionEventPersonMessage     = "person_message"
	CompanionEventReply             = "reply"
	CompanionEventTaskWatch         = "task_watch"
	CompanionEventTaskOutcome       = "task_outcome"
	CompanionEventApprovalRequested = "approval_requested"
	CompanionEventApprovalDecided   = "approval_decided"
	CompanionEventConfirmation      = "confirmation"
	CompanionEventToolResult        = "tool_result"
	CompanionEventWake              = "wake"
	CompanionEventRunFailed         = "run_failed"
)

const (
	// companionMaintainPage is the events read and committed per step.
	companionMaintainPage = 256
	// CompanionMaintainRunStartEvents bounds the events a run start folds in
	// before assembling its context, so a foreground turn never waits on a
	// long catch-up (a Companion with history from before its state
	// existed). Ordinarily background maintenance has already consumed the
	// log; a longer backlog stays pending (the state reports it) for the
	// background passes.
	CompanionMaintainRunStartEvents = 64
	// CompanionMaintainBackgroundEvents bounds one background maintenance
	// call: one page, so a run start waiting on the principal's maintenance
	// lock waits for at most one page. A pass repeats calls while events
	// stay pending.
	CompanionMaintainBackgroundEvents = companionMaintainPage
	// companionOutcomeMaxBytes bounds the task outcome body parsed.
	companionOutcomeMaxBytes = 256 * 1024
)

// ErrCompanionNotCurrent: the session is not the account user's current
// Companion (another session, a deleted generation); nothing is maintained.
var ErrCompanionNotCurrent = errors.New("session is not the account user's current Companion")

// CompanionTaskReport is one task row of a task-watch outcome or milestone
// wake, as the daemon reported it. Titles and statuses from a linked machine
// are untrusted data.
type CompanionTaskReport struct {
	Machine   string `json:"machine,omitempty"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
}

// CompanionEvent is what one durable event of the Companion session means.
type CompanionEvent struct {
	Kind   string
	Source pebblestore.CompanionEventSource
	// Message is the message the event appended, if any.
	Message *pebblestore.MessageSnapshot
	// Watches is the session's complete active watch set after the event,
	// when the event carries it (registration, outcome, cancellation,
	// migration): it is authoritative, so open watch commitments are
	// reconciled against it rather than inferred.
	Watches *[]pebblestore.V3TaskWatchSummary
	// WatchID, Delivery, ProjectID, Machine and Tasks describe a task-watch
	// event or outcome/milestone wake.
	WatchID   string
	Delivery  string
	ProjectID string
	Machine   string
	Tasks     []CompanionTaskReport
	// ID, Name, Status and Detail describe a permission, confirmation, tool
	// result, wake or failed run.
	ID     string
	Name   string
	Status string
	Detail string
	// Output is a completed tool's output (manage_companion records).
	Output    string
	Arguments string
}

type companionEventPayload struct {
	Message     *pebblestore.MessageSnapshot      `json:"message"`
	TaskWatches *[]pebblestore.V3TaskWatchSummary `json:"task_watches"`
	RunID       string                            `json:"run_id"`
	Status      string                            `json:"status"`
	Error       string                            `json:"error"`
	RunIntent   *struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	} `json:"run_intent"`
	Permission *struct {
		ID       string `json:"id"`
		ToolName string `json:"tool_name"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
	} `json:"permission"`
	ToolName string `json:"tool_name"`
	// CallID, Output, RawOutput and Arguments are the flat terminal tool
	// payload the provider tool invoker writes
	// (providerManagedV3ToolEventPayload); its tool message is stored beside
	// the event, never inside the payload.
	CallID       string `json:"call_id"`
	Output       string `json:"output"`
	RawOutput    string `json:"raw_output"`
	Arguments    string `json:"arguments"`
	Confirmation *struct {
		ID                    string `json:"id"`
		Status                string `json:"status"`
		Tool                  string `json:"tool"`
		Summary               string `json:"summary"`
		Delivered             bool   `json:"delivered"`
		ExecutionPermissionID string `json:"execution_permission_id"`
	} `json:"client_tool_confirmation"`
}

func companionMetaString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// ClassifyCompanionEvent says what one durable event of a Companion session
// means for its state. ok is false for an event the state does not follow
// (deltas, usage, plans, titles...), which is still consumed.
func ClassifyCompanionEvent(event pebblestore.V3SessionEvent) (CompanionEvent, bool) {
	eventType := strings.TrimSpace(event.EventType)
	source := pebblestore.CompanionEventSource{SessionID: event.SessionID, EventSeq: event.Seq, EventID: event.ID, EventType: eventType, At: event.TsUnixMs}
	if len(event.Payload) == 0 || strings.HasSuffix(eventType, ".delta") {
		return CompanionEvent{}, false
	}
	var payload companionEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return CompanionEvent{}, false
	}
	out := CompanionEvent{Source: source, Watches: payload.TaskWatches}
	if message := payload.Message; message != nil && strings.TrimSpace(message.ID) != "" {
		copied := *message
		if copied.GlobalSeq == 0 {
			copied.GlobalSeq = event.Seq
		}
		copied.SessionID = event.SessionID
		out.Message = &copied
		out.Source.MessageID, out.Source.MessageSeq = copied.ID, copied.GlobalSeq
	}
	switch eventType {
	case "permission.requested", "permission.updated":
		permission := payload.Permission
		if permission == nil || strings.TrimSpace(permission.ID) == "" {
			return CompanionEvent{}, false
		}
		out.ID, out.Name, out.Status = strings.TrimSpace(permission.ID), firstNonEmptyString(permission.ToolName, payload.ToolName), strings.TrimSpace(permission.Status)
		out.Detail = permission.Reason
		if eventType == "permission.requested" || out.Status == pebblestore.PermissionStatusPending || out.Status == "" {
			out.Kind = CompanionEventApprovalRequested
		} else {
			out.Kind = CompanionEventApprovalDecided
		}
		return out, true
	case "session.task_watch.started", "session.task_watch.cancelled", "session.task_watch.migrated":
		out.Kind = CompanionEventTaskWatch
		if out.Message != nil {
			out.WatchID = companionMetaString(out.Message.Metadata, "task_watch_id")
			out.ProjectID = companionMetaString(out.Message.Metadata, "project_id")
			out.Machine = companionMetaString(out.Message.Metadata, "machine")
		}
		return out, out.Watches != nil || out.WatchID != ""
	}
	if c := payload.Confirmation; c != nil && strings.TrimSpace(c.ID) != "" {
		// Only the request and the decision are commitments; the approved
		// call's execution and delivery are their own tool events.
		if c.Delivered || strings.TrimSpace(c.ExecutionPermissionID) != "" {
			return CompanionEvent{}, false
		}
		out.Kind, out.ID, out.Name, out.Status, out.Detail = CompanionEventConfirmation, strings.TrimSpace(c.ID), strings.TrimSpace(c.Tool), strings.TrimSpace(c.Status), c.Summary
		return out, true
	}
	if message := out.Message; message != nil {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch {
		case eventType == "session.assistant.completed" && role == "assistant" && strings.TrimSpace(message.Content) != "":
			out.Kind = CompanionEventReply
			return out, true
		case role == "user" && companionMetaString(message.Metadata, "task_watch_id") != "" && companionMetaString(message.Metadata, "task_watch_delivery") != "":
			out.Kind = CompanionEventTaskOutcome
			out.WatchID = companionMetaString(message.Metadata, "task_watch_id")
			out.Delivery = companionMetaString(message.Metadata, "task_watch_delivery")
			out.Machine = companionMetaString(message.Metadata, "machine")
			out.ProjectID, out.Tasks = companionTaskReports(message.Content, out.Machine)
			return out, true
		case role == "user" && CompanionPersonMessage(*message):
			out.Kind = CompanionEventPersonMessage
			return out, true
		case role == "user" && (companionMetaString(message.Metadata, "wake_id") != "" || companionWakeSource(companionMetaString(message.Metadata, "source"))):
			out.Kind = CompanionEventWake
			out.Name = firstNonEmptyString(companionMetaString(message.Metadata, "source"), "wake")
			out.ID = companionMetaString(message.Metadata, "wake_id")
			out.Status = "delivered"
			return out, true
		case role == "tool" && strings.HasPrefix(eventType, "session.tool."):
			out.Kind = CompanionEventToolResult
			out.Name = firstNonEmptyString(companionMetaString(message.Metadata, "tool_name"), payload.ToolName)
			out.ID = companionMetaString(message.Metadata, "call_id")
			out.Status = providerManagedV3ToolTerminalStatusForEventType(eventType)
			out.Detail = companionMetaString(message.Metadata, "error")
			if record, ok := decodeV3ProviderManagedToolResultRecord(message.Content); ok {
				out.Name = firstNonEmptyString(out.Name, record.ToolName)
				out.ID = firstNonEmptyString(out.ID, record.CallID)
				out.Output, out.Arguments = firstNonEmptyString(record.CompletedOutput, record.Output), record.Arguments
				out.Detail = firstNonEmptyString(out.Detail, record.Error)
			}
			if out.Status == "" {
				return CompanionEvent{}, false
			}
			return out, true
		}
	}
	if strings.HasPrefix(eventType, "session.tool.") {
		// A terminal tool event as the invoker writes it: a flat payload.
		// Its status is the call's, never the run's, so a tool event is a
		// tool result or nothing, and never a run failure.
		out.Status = providerManagedV3ToolTerminalStatusForEventType(eventType)
		out.Kind = CompanionEventToolResult
		out.Name, out.ID = strings.TrimSpace(payload.ToolName), strings.TrimSpace(payload.CallID)
		out.Detail, out.Arguments = strings.TrimSpace(payload.Error), payload.Arguments
		// Only a manage_companion answer is read (its record); no other
		// tool's output, a device's included, is kept. The whole output
		// when the event carries it: the bounded "output" can cut the record.
		if out.Name == "manage_companion" {
			out.Output = firstNonEmptyString(payload.RawOutput, payload.Output)
		}
		if out.Status == "" || out.Name == "" {
			return CompanionEvent{}, false
		}
		return out, true
	}
	runStatus := strings.TrimSpace(payload.Status)
	runID := strings.TrimSpace(payload.RunID)
	if intent := payload.RunIntent; intent != nil {
		runStatus, runID = firstNonEmptyString(strings.TrimSpace(intent.Status), runStatus), firstNonEmptyString(strings.TrimSpace(intent.RunID), runID)
	}
	if eventType == "session.run.failed" || runStatus == pebblestore.V3RunIntentFailed || runStatus == pebblestore.V3RunIntentInterrupted {
		out.Kind, out.ID, out.Name, out.Status, out.Detail = CompanionEventRunFailed, runID, "run", firstNonEmptyString(runStatus, "failed"), payload.Error
		return out, true
	}
	if out.Watches != nil {
		out.Kind = CompanionEventTaskWatch
		return out, true
	}
	return CompanionEvent{}, false
}

func companionWakeSource(source string) bool {
	switch source {
	case pebblestore.SessionWakeSourceBackgroundCommand, pebblestore.SessionWakeSourceEnvironmentWatch, pebblestore.SessionWakeSourceMachineWatch, pebblestore.SessionWakeSourceTaskWatch:
		return true
	}
	return false
}

// companionTaskReports reads the task rows of a task-watch outcome or
// milestone wake ("Delegated project task outcomes:\n{json}").
func companionTaskReports(content, machine string) (string, []CompanionTaskReport) {
	_, body, ok := strings.Cut(content, "\n")
	if !ok || len(body) > companionOutcomeMaxBytes {
		return "", nil
	}
	var parsed struct {
		Machine   string                `json:"machine"`
		ProjectID string                `json:"project_id"`
		Tasks     []CompanionTaskReport `json:"tasks"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &parsed) != nil {
		return "", nil
	}
	rows := make([]CompanionTaskReport, 0, len(parsed.Tasks))
	for _, row := range parsed.Tasks {
		if strings.TrimSpace(row.TaskID) == "" {
			continue
		}
		row.Machine = firstNonEmptyString(row.Machine, parsed.Machine, machine)
		rows = append(rows, row)
	}
	return strings.TrimSpace(parsed.ProjectID), rows
}

// companionStateMutation collects one maintenance page's changes beyond the
// state itself: the focus and the continuity segments to commit with it, and
// the continuity items of the event being applied.
type companionStateMutation struct {
	focus    *pebblestore.CompanionFocus
	segments []pebblestore.CompanionSegment
	items    []pebblestore.CompanionSegmentItem
}

func companionStateTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return companionOneLine(line, pebblestore.CompanionStateMaxTitleBytes)
}

func companionCommitmentID(kind, key string) string { return kind + ":" + key }

func companionOpenCommitment(state *pebblestore.CompanionState, kind, key, title string, source pebblestore.CompanionEventSource) {
	id := companionCommitmentID(kind, key)
	for _, c := range state.Commitments {
		if c.ID == id {
			return // already opened (or already resolved): an event never reopens it
		}
	}
	state.Commitments = append(state.Commitments, pebblestore.CompanionCommitment{ID: id, Kind: kind, Key: key, Title: title, State: pebblestore.CompanionCommitmentOpen, Opened: source})
}

// companionResolveCommitments resolves the open commitments match selects.
// It reports how many it resolved.
func companionResolveCommitments(state *pebblestore.CompanionState, match func(pebblestore.CompanionCommitment) bool, resolution string, source pebblestore.CompanionEventSource) int {
	resolved := 0
	for i := range state.Commitments {
		c := &state.Commitments[i]
		if c.State != pebblestore.CompanionCommitmentOpen || !match(*c) {
			continue
		}
		src := source
		c.State, c.Resolution, c.Resolved = pebblestore.CompanionCommitmentResolved, resolution, &src
		resolved++
	}
	return resolved
}

func companionTaskWorkID(machine, projectID, taskID string) string {
	return "task:" + strings.TrimSpace(machine) + "/" + strings.TrimSpace(projectID) + "/" + strings.TrimSpace(taskID)
}

// companionUpsertWork applies one report to a work reference: Revision
// advances only when something reported changed, so a repeated report is a
// no-op.
func companionUpsertWork(state *pebblestore.CompanionState, ref pebblestore.CompanionWorkRef) {
	for i := range state.Work {
		current := &state.Work[i]
		if current.ID != ref.ID {
			continue
		}
		next := *current
		if ref.Status != "" {
			next.Status = ref.Status
		}
		if ref.AttemptID != "" {
			next.AttemptID = ref.AttemptID
		}
		if ref.SessionID != "" {
			next.SessionID = ref.SessionID
		}
		if ref.Objective != "" {
			next.Objective = ref.Objective
		}
		if ref.RecordID != "" {
			next.RecordID = ref.RecordID
		}
		if ref.WatchID != "" || ref.Kind == "" {
			next.WatchID = ref.WatchID
		}
		next.Untrusted = next.Untrusted || ref.Untrusted
		if next.Status == current.Status && next.AttemptID == current.AttemptID && next.SessionID == current.SessionID && next.Objective == current.Objective && next.RecordID == current.RecordID && next.WatchID == current.WatchID {
			return
		}
		next.Revision = current.Revision + 1
		next.Source = ref.Source
		*current = next
		return
	}
	if ref.Kind == "" {
		return // a clear of an unknown reference
	}
	ref.Revision, ref.Origin = 1, ref.Source
	state.Work = append(state.Work, ref)
}

// companionReconcileWatches makes the open task-watch commitments and the
// watched work references match watches, the session's complete active set
// after this event. A watch that is gone was settled by this event's outcome
// or ended otherwise.
func companionReconcileWatches(state *pebblestore.CompanionState, ev CompanionEvent) {
	if ev.Watches == nil {
		return
	}
	active := map[string]pebblestore.V3TaskWatchSummary{}
	for _, watch := range *ev.Watches {
		if id := strings.TrimSpace(watch.WatchID); id != "" {
			active[id] = watch
		}
	}
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		watch := active[id]
		title := companionOneLine(strings.Join(watch.Titles, "; "), pebblestore.CompanionStateMaxTitleBytes)
		companionOpenCommitment(state, pebblestore.CompanionCommitmentTaskWatch, id, title, ev.Source)
		for i, taskID := range watch.TaskIDs {
			objective := ""
			if i < len(watch.Titles) {
				objective = companionOneLine(watch.Titles[i], pebblestore.CompanionStateMaxTitleBytes)
			}
			companionUpsertWork(state, pebblestore.CompanionWorkRef{
				ID: companionTaskWorkID(watch.Machine, watch.ProjectID, taskID), Kind: pebblestore.CompanionWorkTask,
				Machine: watch.Machine, ProjectID: watch.ProjectID, TaskID: taskID, WatchID: id, Objective: objective,
				Status: companionWatchingStatus(state, companionTaskWorkID(watch.Machine, watch.ProjectID, taskID)), Untrusted: watch.Machine != "", Source: ev.Source,
			})
		}
	}
	for _, c := range state.Commitments {
		if c.Kind != pebblestore.CompanionCommitmentTaskWatch || c.State != pebblestore.CompanionCommitmentOpen {
			continue
		}
		if _, still := active[c.Key]; still {
			continue
		}
		resolution := "ended"
		if ev.Kind == CompanionEventTaskOutcome && ev.WatchID == c.Key {
			resolution = "settled"
		}
		key := c.Key
		companionResolveCommitments(state, func(o pebblestore.CompanionCommitment) bool {
			return o.Kind == pebblestore.CompanionCommitmentTaskWatch && o.Key == key
		}, resolution, ev.Source)
		for i := range state.Work {
			if state.Work[i].WatchID == key {
				ref := state.Work[i]
				ref.WatchID, ref.Kind, ref.Status, ref.Source = "", "", "", ev.Source
				companionUpsertWork(state, ref)
			}
		}
	}
}

// companionWatchingStatus keeps a task's last reported status when a new
// watch starts on it; a task never reported is "watching".
func companionWatchingStatus(state *pebblestore.CompanionState, id string) string {
	for _, ref := range state.Work {
		if ref.ID == id && ref.Status != "" {
			return ""
		}
	}
	return "watching"
}

func companionAddResult(state *pebblestore.CompanionState, result pebblestore.CompanionResultRef) {
	result.Detail = companionOneLine(result.Detail, pebblestore.CompanionStateMaxDetailBytes)
	state.Results = append(state.Results, result)
}

// companionRecordedContext reads the context bank record a completed
// manage_companion call wrote (data.record in its answer).
func companionRecordedContext(output string) (pebblestore.CompanionContextRecord, bool) {
	var envelope struct {
		Data struct {
			Record *pebblestore.CompanionContextRecord `json:"record"`
		} `json:"data"`
		Record *pebblestore.CompanionContextRecord `json:"record"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &envelope) != nil {
		return pebblestore.CompanionContextRecord{}, false
	}
	record := envelope.Data.Record
	if record == nil {
		record = envelope.Record
	}
	if record == nil || strings.TrimSpace(record.ID) == "" || record.State != pebblestore.CompanionContextStateActive {
		return pebblestore.CompanionContextRecord{}, false
	}
	return *record, true
}

func companionAppendRef(list []pebblestore.CompanionStateRef, ref pebblestore.CompanionStateRef) []pebblestore.CompanionStateRef {
	for i := range list {
		if list[i].RecordID == ref.RecordID {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	return append(list, ref)
}

// applyCompanionEvent folds one classified event into state. topic is the
// topic record indexed from a person's message (nil when it made none).
func applyCompanionEvent(state *pebblestore.CompanionState, ev CompanionEvent, topic *pebblestore.CompanionContextRecord, mutation *companionStateMutation) {
	var answered []string
	switch ev.Kind {
	case CompanionEventPersonMessage:
		title := ""
		if ev.Message != nil && !shouldDropSensitiveConversationMessage(*ev.Message) {
			title = companionStateTitle(ev.Message.Content)
		}
		companionOpenCommitment(state, pebblestore.CompanionCommitmentReply, ev.Source.MessageID, title, ev.Source)
		if topic != nil {
			state.Topics = companionAppendRef(state.Topics, pebblestore.CompanionStateRef{RecordID: topic.ID, Kind: topic.Kind, Title: companionOneLine(topic.Title, pebblestore.CompanionStateMaxTitleBytes), Source: ev.Source})
			source := ev.Source
			mutation.focus = &pebblestore.CompanionFocus{RecordID: topic.ID, Keywords: topic.Keywords, MessageSeq: ev.Source.MessageSeq, UpdatedAt: ev.Source.At, Source: &source}
		}
	case CompanionEventReply:
		seq := ev.Source.EventSeq
		companionResolveCommitments(state, func(c pebblestore.CompanionCommitment) bool {
			if c.Kind != pebblestore.CompanionCommitmentReply || c.Opened.EventSeq >= seq {
				return false
			}
			// The topic record a request would have: forgetting it also
			// removes this reply from continuity.
			answered = append(answered, pebblestore.CompanionMessageContextID(c.Opened.SessionID, c.Opened.MessageSeq))
			return true
		}, "answered", ev.Source)
	case CompanionEventApprovalRequested:
		companionOpenCommitment(state, pebblestore.CompanionCommitmentApproval, ev.ID, companionOneLine(ev.Name, pebblestore.CompanionStateMaxTitleBytes), ev.Source)
	case CompanionEventApprovalDecided:
		id := ev.ID
		if companionResolveCommitments(state, func(c pebblestore.CompanionCommitment) bool {
			return c.Kind == pebblestore.CompanionCommitmentApproval && c.Key == id
		}, ev.Status, ev.Source) == 0 {
			state.Counters.UnmatchedResolution++
		}
	case CompanionEventConfirmation:
		if ev.Status == ClientToolConfirmationPending {
			companionOpenCommitment(state, pebblestore.CompanionCommitmentConfirmation, ev.ID, companionOneLine(firstNonEmptyString(ev.Detail, ev.Name), pebblestore.CompanionStateMaxTitleBytes), ev.Source)
			break
		}
		id := ev.ID
		if companionResolveCommitments(state, func(c pebblestore.CompanionCommitment) bool {
			return c.Kind == pebblestore.CompanionCommitmentConfirmation && c.Key == id
		}, ev.Status, ev.Source) == 0 {
			state.Counters.UnmatchedResolution++
		}
	case CompanionEventTaskOutcome:
		for _, row := range ev.Tasks {
			companionUpsertWork(state, pebblestore.CompanionWorkRef{
				ID: companionTaskWorkID(row.Machine, ev.ProjectID, row.TaskID), Kind: pebblestore.CompanionWorkTask,
				Machine: row.Machine, ProjectID: ev.ProjectID, TaskID: row.TaskID, AttemptID: row.AttemptID, SessionID: row.SessionID,
				Objective: companionOneLine(row.Title, pebblestore.CompanionStateMaxTitleBytes), Status: firstNonEmptyString(row.Status, "reported"),
				// Only the event's complete watch set assigns or clears a
				// reference's watch (companionReconcileWatches), so a
				// repeated report is a no-op.
				Untrusted: row.Machine != "", Source: ev.Source,
			})
		}
		companionAddResult(state, pebblestore.CompanionResultRef{Kind: "task_" + firstNonEmptyString(ev.Delivery, "outcome"), Name: ev.WatchID, Status: "delivered", Source: ev.Source})
	case CompanionEventToolResult:
		if ev.Name == "manage_companion" && ev.Status == "completed" {
			if record, ok := companionRecordedContext(ev.Output); ok {
				ref := pebblestore.CompanionStateRef{RecordID: record.ID, Kind: record.Kind, Title: companionOneLine(record.Title, pebblestore.CompanionStateMaxTitleBytes), Source: ev.Source}
				switch record.Kind {
				case pebblestore.CompanionContextKindDecision:
					state.Decisions = companionAppendRef(state.Decisions, ref)
				case pebblestore.CompanionContextKindWork:
					companionUpsertWork(state, pebblestore.CompanionWorkRef{
						ID: "session:" + record.Source.SessionID, Kind: pebblestore.CompanionWorkSession, SessionID: record.Source.SessionID,
						RecordID: record.ID, Objective: ref.Title, Status: "recorded", Source: ev.Source,
					})
				}
			}
		}
		if pebblestore.IsMachineToolName(ev.Name) || pebblestore.IsBrowserToolName(ev.Name) {
			companionAddResult(state, pebblestore.CompanionResultRef{Kind: "device", Name: ev.Name, CallID: ev.ID, Status: ev.Status, Detail: ev.Detail, Source: ev.Source})
		}
	case CompanionEventWake:
		companionAddResult(state, pebblestore.CompanionResultRef{Kind: "wake", Name: ev.Name, CallID: ev.ID, Status: ev.Status, Source: ev.Source})
	case CompanionEventRunFailed:
		companionAddResult(state, pebblestore.CompanionResultRef{Kind: "run", Name: ev.ID, Status: ev.Status, Detail: ev.Detail, Source: ev.Source})
	}
	companionReconcileWatches(state, ev)
	boundCompanionState(state)
	mutation.items = companionContinuityItems(ev, topic, answered)
}

// boundCompanionState keeps every list within its fixed limit, oldest first
// out, counting what it evicted. Open commitments are kept before resolved
// ones; watched work before work no longer watched.
func boundCompanionState(state *pebblestore.CompanionState) {
	if n := len(state.Topics) - pebblestore.CompanionStateMaxTopics; n > 0 {
		state.Topics, state.Counters.EvictedTopics = state.Topics[n:], state.Counters.EvictedTopics+uint64(n)
	}
	if n := len(state.Decisions) - pebblestore.CompanionStateMaxDecisions; n > 0 {
		state.Decisions, state.Counters.EvictedDecisions = state.Decisions[n:], state.Counters.EvictedDecisions+uint64(n)
	}
	open, resolved := 0, 0
	for _, c := range state.Commitments {
		if c.State == pebblestore.CompanionCommitmentOpen {
			open++
		} else {
			resolved++
		}
	}
	dropOpen, dropResolved := max(0, open-pebblestore.CompanionStateMaxOpen), max(0, resolved-pebblestore.CompanionStateMaxResolved)
	if dropOpen+dropResolved > 0 {
		kept := state.Commitments[:0]
		for _, c := range state.Commitments {
			if c.State == pebblestore.CompanionCommitmentOpen && dropOpen > 0 {
				dropOpen--
				state.Counters.EvictedCommitments++
				continue
			}
			if c.State != pebblestore.CompanionCommitmentOpen && dropResolved > 0 {
				dropResolved--
				continue
			}
			kept = append(kept, c)
		}
		state.Commitments = kept
	}
	if extra := len(state.Work) - pebblestore.CompanionStateMaxWork; extra > 0 {
		kept := state.Work[:0]
		for _, ref := range state.Work {
			if extra > 0 && ref.WatchID == "" {
				extra--
				state.Counters.EvictedWork++
				continue
			}
			kept = append(kept, ref)
		}
		state.Work = kept
		if extra > 0 { // every remaining reference is watched
			state.Work, state.Counters.EvictedWork = state.Work[extra:], state.Counters.EvictedWork+uint64(extra)
		}
	}
	if n := len(state.Results) - pebblestore.CompanionStateMaxResults; n > 0 {
		state.Results = state.Results[n:]
	}
}

// CompanionMaintenance is one maintenance call's outcome: the committed
// state, the events it consumed and how far the log extends beyond it.
type CompanionMaintenance struct {
	State     pebblestore.CompanionState `json:"state"`
	Consumed  int                        `json:"consumed"`
	LatestSeq uint64                     `json:"latest_seq"`
	Pending   uint64                     `json:"pending"`
}

var companionMaintainLocks sync.Map // account\x00user -> *sync.Mutex

func companionMaintainLock(account, user string) *sync.Mutex {
	lock, _ := companionMaintainLocks.LoadOrStore(account+"\x00"+user, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// companionMaintainHook, when set by a test, runs after a page's topic
// writes and before its state commit (a crash window).
var companionMaintainHook func(page int) error

// MaintainCompanionState folds up to maxEvents events of the account user's
// current Companion after the state's cursor into its materialized state.
// It reads the log a page at a time and commits each page atomically (state,
// cursor, focus, index mark), so its reads and writes are bounded by
// maxEvents whatever the history size. Calls for one principal are
// serialized; a concurrent writer outside this process loses on the
// revision check and leaves the state as it was.
//
// A forget, correction or revocation of a context record moves the state's
// revision under the maintainer (its propagation rewrites the state), so a
// refused commit is retried from the new state, at most
// companionMaintainConflictRetries times; Consumed counts only committed
// events.
func MaintainCompanionState(store *pebblestore.SessionStore, session pebblestore.SessionSnapshot, maxEvents int, now int64) (CompanionMaintenance, error) {
	if maxEvents <= 0 {
		maxEvents = CompanionMaintainRunStartEvents
	}
	consumed := 0
	for attempt := 0; ; attempt++ {
		out, err := maintainCompanionStateOnce(store, session, maxEvents-consumed, now)
		consumed += out.Consumed
		out.Consumed = consumed
		if err == nil || !errors.Is(err, pebblestore.ErrCompanionStateConflict) || attempt == companionMaintainConflictRetries || consumed >= maxEvents {
			return out, err
		}
	}
}

// companionMaintainConflictRetries bounds the retries of a maintenance call
// whose commit lost to a concurrent record change.
const companionMaintainConflictRetries = 2

func maintainCompanionStateOnce(store *pebblestore.SessionStore, session pebblestore.SessionSnapshot, maxEvents int, now int64) (CompanionMaintenance, error) {
	var out CompanionMaintenance
	if store == nil {
		return out, errors.New("companion state store unavailable")
	}
	account, user := strings.TrimSpace(session.AccountScopeID), strings.TrimSpace(session.UserID)
	if !isCompanionSession(session) || account == "" || user == "" {
		return out, ErrCompanionNotCurrent
	}
	lock := companionMaintainLock(account, user)
	lock.Lock()
	defer lock.Unlock()
	current, err := store.IsCurrentCompanion(session, account, user)
	if err != nil {
		return out, err
	}
	if !current {
		return out, ErrCompanionNotCurrent
	}
	identity, found, err := store.GetCompanion(account, user)
	if err != nil || !found {
		return out, errors.Join(ErrCompanionNotCurrent, err)
	}
	state, stored, err := store.GetCompanionState(account, user)
	if err != nil {
		return out, err
	}
	expected := int64(0)
	if stored {
		expected = state.Revision
	}
	if !stored || state.SessionID != session.ID || state.Generation != identity.Generation {
		state = pebblestore.NewCompanionState(identity, now)
	}
	// Messages at or before the topic index mark were already considered for
	// the context bank (before this state existed); they are not indexed again.
	mark, err := store.GetCompanionIndexMark(account, user)
	if err != nil {
		return out, err
	}
	floor := uint64(0)
	if mark.SessionID == session.ID {
		floor = mark.MessageSeq
	}
	if maxEvents <= 0 {
		maxEvents = CompanionMaintainRunStartEvents
	}
	for page := 0; out.Consumed < maxEvents; page++ {
		events, err := store.ListV3SessionEvents(session.ID, state.Cursor.EventSeq, min(companionMaintainPage, maxEvents-out.Consumed))
		if err != nil {
			return out, fmt.Errorf("read companion events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		next := state
		next.Topics = append([]pebblestore.CompanionStateRef(nil), state.Topics...)
		next.Decisions = append([]pebblestore.CompanionStateRef(nil), state.Decisions...)
		next.Commitments = append([]pebblestore.CompanionCommitment(nil), state.Commitments...)
		next.Work = append([]pebblestore.CompanionWorkRef(nil), state.Work...)
		next.Results = append([]pebblestore.CompanionResultRef(nil), state.Results...)
		next.Continuity.Open = append([]pebblestore.CompanionSegmentItem(nil), state.Continuity.Open...)
		mutation := companionStateMutation{}
		for _, event := range events {
			if event.SessionID != session.ID || event.Seq != next.Cursor.EventSeq+1 {
				return out, fmt.Errorf("companion event log gap: event %d after cursor %d", event.Seq, next.Cursor.EventSeq)
			}
			next.Counters.Events++
			if ev, ok := ClassifyCompanionEvent(event); ok {
				var topic *pebblestore.CompanionContextRecord
				if ev.Kind == CompanionEventPersonMessage && ev.Message != nil && ev.Message.GlobalSeq > floor {
					record, indexed, err := indexCompanionTopic(store, session, *ev.Message, now)
					if err != nil {
						return out, fmt.Errorf("index companion topic: %w", err)
					}
					if indexed {
						topic = &record
					}
				}
				applyCompanionEvent(&next, ev, topic, &mutation)
				next.Counters.Applied++
			}
			mutation.segments = append(mutation.segments, companionContinuityStep(&next, event.Seq, mutation.items)...)
			mutation.items = nil
			next.Cursor = pebblestore.CompanionStateCursor{SessionID: session.ID, EventSeq: event.Seq, EventID: event.ID, UpdatedAt: now}
		}
		if companionMaintainHook != nil {
			if err := companionMaintainHook(page); err != nil {
				return out, err
			}
		}
		next.UpdatedAt = now
		if mutation.focus != nil {
			mutation.focus.Revision = expected + 1
		}
		committed, err := store.CommitCompanionState(pebblestore.CompanionStateCommit{
			Next: next, ExpectedRevision: expected, Focus: mutation.focus, Segments: mutation.segments,
			IndexMark: &pebblestore.CompanionIndexMark{SessionID: session.ID, MessageSeq: max(floor, next.Cursor.EventSeq)},
		})
		if err != nil {
			return out, err
		}
		out.Consumed += len(events)
		state, expected = committed, committed.Revision
		floor = max(floor, committed.Cursor.EventSeq)
		if len(events) < companionMaintainPage {
			break
		}
	}
	if !stored && out.Consumed == 0 {
		// An empty log still gets its (empty) state, so reads see version,
		// generation and cursor.
		committed, err := store.CommitCompanionState(pebblestore.CompanionStateCommit{Next: state, ExpectedRevision: expected})
		if err != nil {
			return out, err
		}
		state = committed
	}
	out.State = state
	projection, found, err := store.GetV3SessionProjection(session.ID)
	if err != nil {
		return out, err
	}
	out.LatestSeq = state.Cursor.EventSeq
	if found && projection.LastEventSeq > out.LatestSeq {
		out.LatestSeq = projection.LastEventSeq
	}
	out.Pending = out.LatestSeq - state.Cursor.EventSeq
	return out, nil
}

// indexCompanionTopic records a substantive message of the person as a topic
// of the context bank (idempotent per message; a forgotten one stays
// forgotten). indexed is false when the message makes no topic.
func indexCompanionTopic(store *pebblestore.SessionStore, session pebblestore.SessionSnapshot, message pebblestore.MessageSnapshot, now int64) (pebblestore.CompanionContextRecord, bool, error) {
	if message.GlobalSeq == 0 || !CompanionPersonMessage(message) || shouldDropSensitiveConversationMessage(message) {
		return pebblestore.CompanionContextRecord{}, false, nil
	}
	keywords := pebblestore.CompanionContextKeywords(message.Content)
	if len(keywords) < companionIndexMinKeywords {
		return pebblestore.CompanionContextRecord{}, false, nil
	}
	title, _, _ := strings.Cut(strings.TrimSpace(message.Content), "\n")
	record, _, err := store.PutCompanionContext(pebblestore.CompanionContextRecord{
		ID:             pebblestore.CompanionMessageContextID(session.ID, message.GlobalSeq),
		AccountScopeID: session.AccountScopeID,
		UserID:         session.UserID,
		Kind:           pebblestore.CompanionContextKindTopic,
		Title:          truncateUTF8Bytes(title, companionIndexTitleBytes),
		Summary:        truncateUTF8Bytes(strings.TrimSpace(message.Content), companionIndexSummaryBytes),
		Keywords:       keywords,
		Source:         pebblestore.CompanionContextSource{Kind: pebblestore.CompanionContextSourceMessage, SessionID: session.ID, MessageSeq: message.GlobalSeq},
	}, 0, now)
	if errors.Is(err, pebblestore.ErrCompanionContextSource) {
		return pebblestore.CompanionContextRecord{}, false, nil // the message is gone: nothing to index
	}
	if err != nil {
		return pebblestore.CompanionContextRecord{}, false, err
	}
	if record.State != pebblestore.CompanionContextStateActive {
		return pebblestore.CompanionContextRecord{}, false, nil // forgotten
	}
	return record, true, nil
}
