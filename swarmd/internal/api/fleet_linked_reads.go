package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// The home's Orchestrator reads a linked machine the way an MCP client
// holding that machine's key reads it, each read a thin mapping over the
// machine's own route through the existing link:
//
//	machine_status    GET /v3/box/summary            (swarm_status; fleet_machine_status.go)
//	get_task_session  GET /v3/sessions/{id}           (swarm_get_session), for a task this home sent
//	activity          GET /v3/activity?cursor=N       (swarm_activity with since_cursor)
//	                  GET /v3/signals?after=N&limit=L (swarm_activity without it)
//
// The views are built by the same functions MCP uses
// (controlMCPSessionStateView, controlMCPSignalPage, clipActivityEvent). A
// read runs whenever the machine's linked work rule is not off. Everything a
// machine says is untrusted.

const (
	fleetTaskSessionDefaultMessages = 5
	fleetTaskSessionMaxMessages     = 20
	fleetLinkedReadTimeout          = 15 * time.Second
)

// requireLinkedRead refuses a read of machine unless it is linked to this
// account and its linked work rule is not off.
func (s *Server) requireLinkedRead(ctx context.Context, p identity.Principal, machine string) error {
	if s == nil || s.fleet == nil || s.fleetTokens == nil || s.sessions == nil {
		return linkedTaskFail(http.StatusServiceUnavailable, "fleet service unavailable")
	}
	if !p.Valid() {
		return linkedTaskFail(http.StatusUnauthorized, "%s", identity.ErrPrincipalRequired.Error())
	}
	if strings.TrimSpace(machine) == "" {
		return linkedTaskFail(http.StatusBadRequest, "machine is required")
	}
	access, err := s.sessions.Store().GetAccountPermissions(p.AccountScopeID)
	if err != nil {
		return linkedTaskFail(http.StatusInternalServerError, "read permissions: %v", err)
	}
	machines, err := s.ListFleetMachines(ctx, p)
	if err != nil {
		return linkedTaskFail(http.StatusInternalServerError, "%v", err)
	}
	known := false
	for i := range machines {
		machines[i].Access = access.Link(machines[i].Name).Work
		known = known || machines[i].Name == machine
	}
	if !known {
		return linkedTaskFail(http.StatusNotFound, "%v", tool.FleetMachineUnavailableError(machine, machines, false))
	}
	if !pebblestore.LinkedWorkOn(access.Link(machine).Work) {
		return linkedTaskFail(http.StatusForbidden, "%v", tool.FleetMachineUnavailableError(machine, machines, true))
	}
	return nil
}

// ReadFleetMachineStatusView is machine_status: the machine's status as MCP
// swarm_status shows it, after the rule check.
func (s *Server) ReadFleetMachineStatusView(ctx context.Context, p identity.Principal, machine string) (map[string]any, error) {
	if err := s.requireLinkedRead(ctx, p, machine); err != nil {
		return nil, err
	}
	return s.ReadFleetMachineStatus(ctx, p, machine)
}

// ReadFleetTaskSessionView is get_task_session: the session of a task this
// home sent to machine, as MCP swarm_get_session shows a session (state,
// active run, pending permissions and questions, the latest messages
// trimmed), with its active plan and last activity time. The session is the
// one the machine reports for the task; a task this home did not send is
// refused.
func (s *Server) ReadFleetTaskSessionView(ctx context.Context, p identity.Principal, machine, projectID, taskID string, messages int) (map[string]any, error) {
	if err := s.requireLinkedRead(ctx, p, machine); err != nil {
		return nil, err
	}
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	if projectID == "" || taskID == "" {
		return nil, linkedTaskFail(http.StatusBadRequest, "project_id and task_id are required")
	}
	if messages < 0 {
		return nil, linkedTaskFail(http.StatusBadRequest, "messages must be positive")
	}
	if messages == 0 {
		messages = fleetTaskSessionDefaultMessages
	}
	messages = min(messages, fleetTaskSessionMaxMessages)
	if _, sent, err := s.fleet.GetLinkedTask(p.AccountScopeID, machine, projectID, taskID); err != nil {
		return nil, linkedTaskFail(http.StatusInternalServerError, "read linked task: %v", err)
	} else if !sent {
		return nil, linkedTaskFail(http.StatusForbidden, "task %s on %s was not sent from this home; its session is read on that machine", taskID, machine)
	}
	sessionID, err := s.linkedTaskSession(ctx, p, machine, projectID, taskID)
	if err != nil {
		return nil, err
	}
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, fleetLinkedReadTimeout)
	defer cancel()
	path := "/v3/sessions/" + url.PathEscape(sessionID)
	var detail map[string]any
	err = client.JSON(ctx, http.MethodGet, path+"?message_limit="+strconv.Itoa(messages)+"&event_limit=0", nil, &detail)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, linkedFleetCallError(machine, linked, "the session read", err)
	}
	// A cut message reads on through read_task_session, which pages within
	// one message of this same task session.
	view := controlMCPSessionStateView(detail, controlMCPTextLimit, controlMCPToolOutputLimit, map[string]any{"action": tool.FleetReadTaskSessionAction, "machine": machine, "project_id": projectID, "task_id": taskID})
	var active map[string]any
	if err := client.JSON(ctx, http.MethodGet, path+"/plans/active", nil, &active); err == nil {
		if plan := controlMCPActivePlan(active); plan != nil {
			view["plan"] = plan
		}
	}
	view["last_activity_at"] = fleetSessionLastActivity(detail)
	view["machine"], view["project_id"], view["task_id"], view["session_id"] = machine, projectID, taskID, sessionID
	view["read_only"], view["untrusted_content"] = true, true
	return view, nil
}

// fleetSessionLastActivity is the latest time (Unix ms) the session detail
// names: the session's last change or message, its run's last change, or
// its newest message.
func fleetSessionLastActivity(detail map[string]any) int64 {
	latest := int64(0)
	note := func(value any) {
		if at, ok := value.(float64); ok && int64(at) > latest {
			latest = int64(at)
		}
	}
	session := controlMCPMap(detail["session"])
	note(session["updated_at"])
	note(session["last_message_at"])
	if intent := controlMCPMap(detail["active_run_intent"]); intent != nil {
		note(intent["updated_at"])
		note(intent["started_at"])
	}
	for _, raw := range controlMCPList(detail["messages"]) {
		note(controlMCPMap(raw)["created_at"])
	}
	return latest
}

// ReadFleetActivity is activity: the machine's feed as MCP swarm_activity
// reads it. With sinceCursor it is one page of the machine's activity
// (jobs, tasks and sessions that started, finished, failed, were stopped or
// wait on a person), metadata only; without it, the machine's signals after
// after (job failures, update drains, refused keys) with their next cursor.
func (s *Server) ReadFleetActivity(ctx context.Context, p identity.Principal, machine, sinceCursor, after string, limit int) (map[string]any, error) {
	if err := s.requireLinkedRead(ctx, p, machine); err != nil {
		return nil, err
	}
	sinceCursor, after = strings.TrimSpace(sinceCursor), strings.TrimSpace(after)
	out := map[string]any{"machine": machine, "untrusted_content": true}
	if sinceCursor != "" {
		cursor, err := strconv.ParseUint(sinceCursor, 10, 64)
		if err != nil {
			return nil, linkedTaskFail(http.StatusBadRequest, "since_cursor must be a decimal cursor string")
		}
		page, err := s.fleetActivityMachinePage(ctx, p, machine, cursor)
		if err != nil {
			_, linked, _, _ := s.fleetMachineClient(p.AccountScopeID, machine)
			return nil, linkedFleetCallError(machine, linked, "the activity read", err)
		}
		events := make([]activityEvent, 0, len(page.Events))
		for _, event := range page.Events {
			events = append(events, clipActivityEvent(event))
		}
		out["events"], out["cursor"], out["gap"] = events, page.Cursor, page.Gap
		return out, nil
	}
	if after != "" {
		if _, err := strconv.ParseUint(after, 10, 64); err != nil {
			return nil, linkedTaskFail(http.StatusBadRequest, "after must be a decimal cursor string")
		}
	}
	if limit <= 0 {
		limit = 30
	}
	limit = min(limit, 200)
	client, linked, status, err := s.fleetMachineClient(p.AccountScopeID, machine)
	if err != nil {
		if status == 0 {
			status = http.StatusBadGateway
		}
		return nil, linkedTaskFail(status, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, fleetLinkedReadTimeout)
	defer cancel()
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		query.Set("after", after)
	}
	var raw json.RawMessage
	err = client.JSON(ctx, http.MethodGet, "/v3/signals?"+query.Encode(), nil, &raw)
	s.noteFleetContact(linked, err)
	if err != nil {
		return nil, linkedFleetCallError(machine, linked, "the signal read", err)
	}
	var page map[string]any
	if json.Unmarshal(raw, &page) != nil {
		return nil, linkedTaskFail(http.StatusBadGateway, "%s returned an unreadable signal page", machine)
	}
	view := controlMCPSignalPage(page)
	events, _ := view["events"].([]map[string]any)
	if len(events) > 200 {
		events = events[:200]
	}
	for _, event := range events {
		for key, value := range event {
			if text, ok := value.(string); ok {
				event[key] = clipActivityText(text, fleetActivityFieldLimit)
			}
		}
	}
	view["events"] = events
	for key, value := range view {
		out[key] = value
	}
	return out, nil
}

var _ tool.FleetLinkedReader = (*Server)(nil)
