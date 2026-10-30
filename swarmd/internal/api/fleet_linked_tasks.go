package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Linked tasks: the home's record of tasks its sessions sent to linked
// machines. The machine owns each task; the home keeps who sent it and what it
// last observed (from get_task/list_tasks answers, including the linked wait's
// reads), so the sending session and project can show it. The read-only
// session view reads the task's remote session through the same link.
//
//	GET /v3/fleet/linked-tasks?session_id=ID | ?project_id=ID   the records
//	GET /v3/fleet/linked-tasks/session?machine=&project_id=&task_id=[&limit=&max_chars=&role=]
//	                                                           the remote task session's latest messages
//	    [&message_id=&message_seq=&content_offset=]            the next chunk of one message a read cut
//
// The session view needs sessions:read, the records sessions:read or
// projects:read; gateway tokens read neither.
// Everything observed from a machine is untrusted.

const (
	fleetLinkedTasksPath       = "/v3/fleet/linked-tasks"
	fleetLinkedTaskSessionPath = "/v3/fleet/linked-tasks/session"

	// A read defaults to the latest few assistant/tool messages, its whole
	// reply within 5 KB; max_chars raises the content budget to 20 KB.
	fleetLinkedTaskSessionDefaultLimit    = 5
	fleetLinkedTaskSessionMaxLimit        = 100
	fleetLinkedTaskSessionDefaultMaxChars = 3500
	fleetLinkedTaskSessionMaxChars        = 20000
	// fleetLinkedTaskSessionScanLimit is how many of the latest remote
	// messages a role-filtered read looks through.
	fleetLinkedTaskSessionScanLimit     = 100
	fleetLinkedTaskSessionTitleBytes    = 160
	fleetLinkedTaskSessionProgressBytes = 300
)

// linkedTaskError carries the HTTP status the routes answer with.
type linkedTaskError struct {
	status int
	err    error
}

func (e *linkedTaskError) Error() string { return e.err.Error() }
func (e *linkedTaskError) Unwrap() error { return e.err }

func linkedTaskFail(status int, format string, args ...any) error {
	return &linkedTaskError{status: status, err: fmt.Errorf(format, args...)}
}

func linkedTaskStatus(err error) int {
	var typed *linkedTaskError
	if errors.As(err, &typed) {
		return typed.status
	}
	return http.StatusInternalServerError
}

func (s *Server) registerFleetLinkedTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc(fleetLinkedTasksPath, s.handleFleetLinkedTasks)
	mux.HandleFunc(fleetLinkedTaskSessionPath, s.handleFleetLinkedTasks)
}

// RecordFleetLinkedTask keeps the home's record of a task a home session just
// sent to a machine, from the machine's answer to propose_task/create_task.
func (s *Server) RecordFleetLinkedTask(_ context.Context, p identity.Principal, origin tool.FleetLinkedTaskOrigin, machine, projectID string, response json.RawMessage) (pebblestore.FleetLinkedTask, error) {
	if s == nil || s.fleet == nil {
		return pebblestore.FleetLinkedTask{}, errors.New("linked task records unavailable")
	}
	// The sender is the sending session, or, for a Swarm Control caller
	// (which has no session), its AI key: never both, never neither.
	sessionID, sender := strings.TrimSpace(origin.SessionID), strings.TrimSpace(origin.Sender)
	if !p.Valid() || (sessionID == "") == (sender == "") {
		return pebblestore.FleetLinkedTask{}, errors.New("a linked task record needs its sender: the sending session or the Swarm Control caller")
	}
	var answer struct {
		Task *pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(response, &answer); err != nil || answer.Task == nil || strings.TrimSpace(answer.Task.ID) == "" {
		return pebblestore.FleetLinkedTask{}, fmt.Errorf("%s returned no task to record", machine)
	}
	task := *answer.Task
	if remote := strings.TrimSpace(task.ProjectID); remote != "" && projectID != "" && remote != projectID {
		return pebblestore.FleetLinkedTask{}, fmt.Errorf("%s returned a task of another project", machine)
	}
	observed := linkedTaskObservation(task)
	record := pebblestore.FleetLinkedTask{
		AccountScopeID: p.AccountScopeID, Machine: machine, ProjectID: projectID, TaskID: task.ID, Title: observed.Title,
		HomeSessionID: sessionID, HomeProjectID: origin.ProjectID, HomeSender: sender,
		Status: observed.Status, ActionNeeded: observed.ActionNeeded, LastError: observed.LastError, Progress: observed.Progress,
		RemoteSessionID: observed.RemoteSessionID, Archived: observed.Archived,
	}
	return s.fleet.PutLinkedTask(record)
}

// ListFleetLinkedTasksBySender lists the account's records one Swarm Control
// caller sent (its sender, as ExecuteControlCatalog names it).
func (s *Server) ListFleetLinkedTasksBySender(_ context.Context, p identity.Principal, sender string) ([]pebblestore.FleetLinkedTask, error) {
	if s == nil || s.fleet == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "linked task records unavailable")
	}
	if !p.Valid() {
		return nil, linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	if strings.TrimSpace(sender) == "" {
		return nil, linkedTaskFail(http.StatusBadRequest, "a sender is required")
	}
	return s.fleet.ListLinkedTasksForSender(p.AccountScopeID, sender)
}

// ListFleetLinkedTasks lists the account's records sent by one home session
// (which must be the principal's) or from one home project.
func (s *Server) ListFleetLinkedTasks(_ context.Context, p identity.Principal, sessionID, projectID string) ([]pebblestore.FleetLinkedTask, error) {
	if s == nil || s.fleet == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "linked task records unavailable")
	}
	if !p.Valid() {
		return nil, linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	sessionID, projectID = strings.TrimSpace(sessionID), strings.TrimSpace(projectID)
	switch {
	case sessionID != "" && projectID != "":
		return nil, linkedTaskFail(http.StatusBadRequest, "give session_id or project_id, not both")
	case sessionID != "":
		if s.sessions == nil {
			return nil, linkedTaskFail(http.StatusServiceUnavailable, "session service unavailable")
		}
		session, found, err := s.sessions.GetSession(sessionID)
		if err != nil {
			return nil, linkedTaskFail(http.StatusInternalServerError, "read session: %v", err)
		}
		if !found || session.AccountScopeID != p.AccountScopeID || session.UserID != p.UserID {
			return nil, linkedTaskFail(http.StatusNotFound, "session not found")
		}
		return s.fleet.ListLinkedTasksForSession(p.AccountScopeID, sessionID)
	case projectID != "":
		return s.fleet.ListLinkedTasksForProject(p.AccountScopeID, projectID)
	}
	return nil, linkedTaskFail(http.StatusBadRequest, "session_id or project_id is required")
}

// ReadFleetLinkedTaskSession reads the latest messages of a linked task's
// session on its machine, bounded by read (fleetLinkedTaskSessionRead).
// Read-only: it sends nothing and controls nothing.
func (s *Server) ReadFleetLinkedTaskSession(ctx context.Context, p identity.Principal, machine, projectID, taskID string, read tool.FleetTaskSessionRead) (map[string]any, error) {
	if s == nil || s.fleet == nil || s.fleetTokens == nil || s.sessions == nil {
		return nil, linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	machine, projectID, taskID = strings.TrimSpace(machine), strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if machine == "" || projectID == "" || taskID == "" {
		return nil, linkedTaskFail(http.StatusBadRequest, "machine, project_id and task_id are required")
	}
	read, err := normalizeFleetTaskSessionRead(read)
	if err != nil {
		return nil, linkedTaskFail(http.StatusBadRequest, "%v", err)
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read permissions: %v", err)
	}
	// A wrong name or a rule that is off answers with the linked machines
	// and their access, so the caller can pick one or say which rule is off.
	machines, err := s.ListFleetMachines(ctx, p)
	if err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "%v", err)
	}
	known := false
	for i := range machines {
		machines[i].Access = access.Link(machines[i].Name).Work
		known = known || machines[i].Name == machine
	}
	if !known || !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		status := http.StatusForbidden
		if !known {
			status = http.StatusNotFound
		}
		return nil, linkedTaskFail(status, "%v", tool.FleetMachineUnavailableError(machine, machines, known))
	}
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	// The task read also refreshes the home's record (CallFleetProject).
	raw, err := s.CallFleetProject(ctx, p, machine, "get_task", map[string]any{"project_id": projectID, "task_id": taskID})
	if err != nil {
		var refused *fleet.Error
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			return nil, linkedTaskFail(http.StatusNotFound, "task %s not found on %s", taskID, machine)
		}
		return nil, linkedTaskFail(http.StatusBadGateway, "%v", err)
	}
	var answer struct {
		Task pebblestore.ProjectTaskRecord `json:"task"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.Task.ID != taskID {
		return nil, linkedTaskFail(http.StatusBadGateway, "%s returned an unexpected record for task %s", machine, taskID)
	}
	observed := linkedTaskObservation(answer.Task)
	view := map[string]any{
		"machine": machine, "project_id": projectID, "task_id": taskID,
		"title": clipLinkedTaskText(observed.Title, fleetLinkedTaskSessionTitleBytes), "status": observed.Status,
		"progress":  clipLinkedTaskText(observed.Progress, fleetLinkedTaskSessionProgressBytes),
		"read_only": true, "untrusted_content": true,
	}
	if observed.RemoteSessionID == "" {
		return nil, linkedTaskFail(http.StatusConflict, "task %s on %s has no session yet", taskID, machine)
	}
	view["session_id"] = observed.RemoteSessionID
	var page struct {
		Messages     []pebblestore.MessageSnapshot `json:"messages"`
		HasMoreOlder bool                          `json:"has_more_older"`
	}
	path := "/v3/sessions/" + url.PathEscape(observed.RemoteSessionID) + "/messages?"
	if next := read.Continue; next != nil {
		// The exact message: the first after the one before its sequence.
		path += "after_seq=" + strconv.FormatUint(next.MessageSeq-1, 10) + "&limit=1"
	} else {
		fetch := read.Limit
		if read.Role != "all" {
			fetch = fleetLinkedTaskSessionScanLimit
		}
		path += "tail=true&limit=" + strconv.Itoa(fetch)
	}
	err = client.JSON(ctx, http.MethodGet, path, nil, &page)
	s.noteFleetContact(linked, err)
	if err != nil {
		var refused *fleet.Error
		if errors.As(err, &refused) {
			if refused.Status == http.StatusNotFound {
				return nil, linkedTaskFail(http.StatusNotFound, "the session of task %s was not found on %s", taskID, machine)
			}
			return nil, linkedTaskFail(http.StatusBadGateway, "%s refused the session read: %v", machine, refused)
		}
		return nil, linkedTaskFail(http.StatusBadGateway, "%s", fleetUnreachableError(machine, linked).Error())
	}
	var messages []map[string]any
	if next := read.Continue; next != nil {
		if len(page.Messages) == 0 || page.Messages[0].ID != next.MessageID || page.Messages[0].GlobalSeq != next.MessageSeq {
			return nil, linkedTaskFail(http.StatusNotFound, "message %s (message_seq %d) was not found in the session of task %s on %s", next.MessageID, next.MessageSeq, taskID, machine)
		}
		chunk, err := tool.ChunkMessageContent(page.Messages[0].Content, next.ContentOffset, read.MaxChars)
		if err != nil {
			return nil, linkedTaskFail(http.StatusBadRequest, "%v", err)
		}
		messages = []map[string]any{messageChunkItem(page.Messages[0], chunk)}
	} else {
		var more bool
		messages, more = boundLinkedTaskMessages(page.Messages, read)
		view["has_more_older"] = page.HasMoreOlder || more
		view["role"] = read.Role
	}
	// A cut message says how to read on: the exact parameters of the next
	// chunk.
	for _, item := range messages {
		if item["truncated"] == true {
			item["next"] = map[string]any{
				"action": tool.FleetReadTaskSessionAction, "machine": machine, "project_id": projectID, "task_id": taskID,
				"message_id": item["id"], "message_seq": item["global_seq"], "content_offset": item["content_offset"], "max_chars": read.MaxChars,
			}
		}
	}
	view["messages"] = messages
	view["max_chars"] = read.MaxChars
	return view, nil
}

// normalizeFleetTaskSessionRead applies the read defaults and limits.
func normalizeFleetTaskSessionRead(read tool.FleetTaskSessionRead) (tool.FleetTaskSessionRead, error) {
	if read.Limit < 0 || read.MaxChars < 0 {
		return read, errors.New("limit and max_chars must be positive")
	}
	if read.Limit == 0 {
		read.Limit = fleetLinkedTaskSessionDefaultLimit
	}
	read.Limit = min(read.Limit, fleetLinkedTaskSessionMaxLimit)
	if read.MaxChars == 0 {
		read.MaxChars = fleetLinkedTaskSessionDefaultMaxChars
	}
	read.MaxChars = min(read.MaxChars, fleetLinkedTaskSessionMaxChars)
	if next := read.Continue; next != nil {
		if strings.TrimSpace(next.MessageID) == "" || next.MessageSeq < 1 || next.ContentOffset < 0 {
			return read, errors.New("continuing a message needs message_id, a positive message_seq and a content_offset that is not negative")
		}
	}
	switch read.Role = strings.ToLower(strings.TrimSpace(read.Role)); read.Role {
	case "":
		read.Role = "assistant,tool"
	case "assistant", "tool", "user", "all":
	default:
		return read, errors.New("role must be assistant, tool, user or all")
	}
	return read, nil
}

// boundLinkedTaskMessages keeps the latest read.Limit messages of read.Role
// whose encoded content fits read.MaxChars in total, newest first, so the
// newest work is always kept; the oldest kept one may be cut, and then says
// where its next chunk starts. more reports that matching messages were left
// out.
func boundLinkedTaskMessages(page []pebblestore.MessageSnapshot, read tool.FleetTaskSessionRead) ([]map[string]any, bool) {
	matches := func(role string) bool {
		switch read.Role {
		case "all":
			return true
		case "assistant,tool":
			return role == "assistant" || role == "tool"
		}
		return role == read.Role
	}
	var picked []map[string]any
	used, more := 0, false
	for i := len(page) - 1; i >= 0; i-- {
		message := page[i]
		if !matches(message.Role) {
			continue
		}
		remain := read.MaxChars - used
		if len(picked) >= read.Limit || remain <= 0 {
			more = true
			break
		}
		chunk, err := tool.ChunkMessageContent(message.Content, 0, remain)
		if err != nil {
			// Offset 0 is always inside the content.
			continue
		}
		used += chunk.EncodedLen()
		picked = append(picked, messageChunkItem(message, chunk))
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	if picked == nil {
		picked = []map[string]any{}
	}
	return picked, more
}

func clipLinkedTaskText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func linkedTaskObservation(task pebblestore.ProjectTaskRecord) pebblestore.FleetLinkedTaskObservation {
	task.EnsureTaskAttempts()
	observed := pebblestore.FleetLinkedTaskObservation{
		TaskID: task.ID, Title: task.Title, Status: task.Status, ActionNeeded: task.ActionNeeded,
		LastError: task.LastError, RemoteSessionID: task.SessionID, Archived: task.Archived,
	}
	if attempt := task.ActiveAttempt(); attempt != nil {
		observed.Progress = attempt.Summary
		if attempt.SessionID != "" {
			observed.RemoteSessionID = attempt.SessionID
		}
	}
	return observed
}

// observeFleetLinkedTasks refreshes the home's records from an answer the
// link just gave (get_task, list_tasks and the task lifecycle actions). Only
// tasks this home recorded are touched; failures never change the answer.
func (s *Server) observeFleetLinkedTasks(account, machine, action string, args map[string]any, result json.RawMessage, callErr error) {
	if s == nil || s.fleet == nil {
		return
	}
	projectID, _ := args["project_id"].(string)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || action == "list" || action == "get" {
		return
	}
	var observations []pebblestore.FleetLinkedTaskObservation
	if callErr != nil {
		var refused *fleet.Error
		taskID, _ := args["task_id"].(string)
		if action != "get_task" || !errors.As(callErr, &refused) || refused.Status != http.StatusNotFound || strings.TrimSpace(taskID) == "" {
			return
		}
		observations = append(observations, pebblestore.FleetLinkedTaskObservation{TaskID: strings.TrimSpace(taskID), Missing: true})
	} else {
		var answer struct {
			Task  *pebblestore.ProjectTaskRecord  `json:"task"`
			Tasks []pebblestore.ProjectTaskRecord `json:"tasks"`
		}
		if json.Unmarshal(result, &answer) != nil {
			return
		}
		if answer.Task != nil {
			answer.Tasks = append(answer.Tasks, *answer.Task)
		}
		for _, task := range answer.Tasks {
			if task.ID == "" || (task.ProjectID != "" && task.ProjectID != projectID) {
				continue
			}
			observations = append(observations, linkedTaskObservation(task))
		}
	}
	for _, observed := range observations {
		if _, err := s.fleet.ObserveLinkedTask(account, machine, projectID, observed); err != nil {
			log.Printf("swarmd fleet: keep linked task observation from %s: %v", machine, err)
		}
	}
}

func (s *Server) handleFleetLinkedTasks(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	// The remote session view shows session content (sessions:read); the
	// records read like a project's task list (projects:read too).
	if r.URL.Path == fleetLinkedTaskSessionPath {
		if !s.requireScope(w, r, "sessions:read") {
			return
		}
	} else if !s.requireScopeAny(w, r, "sessions:read", "projects:read") {
		return
	}
	if fleetActivityGatewayCaller(r) {
		writeError(w, http.StatusForbidden, errors.New("gateway tokens cannot read linked tasks"))
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	if r.URL.Path == fleetLinkedTaskSessionPath {
		limit := 0
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
				return
			}
			limit = parsed
		}
		maxChars := 0
		if raw := strings.TrimSpace(query.Get("max_chars")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				writeError(w, http.StatusBadRequest, errors.New("max_chars must be a positive integer"))
				return
			}
			maxChars = parsed
		}
		read := tool.FleetTaskSessionRead{Limit: limit, MaxChars: maxChars, Role: query.Get("role")}
		next, err := parseMessageContinuationQuery(query)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		read.Continue = next
		view, err := s.ReadFleetLinkedTaskSession(r.Context(), principal, query.Get("machine"), query.Get("project_id"), query.Get("task_id"), read)
		if err != nil {
			writeError(w, linkedTaskStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	tasks, err := s.ListFleetLinkedTasks(r.Context(), principal, query.Get("session_id"), query.Get("project_id"))
	if err != nil {
		writeError(w, linkedTaskStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"linked_tasks": tasks, "count": len(tasks), "untrusted_content": true})
}
