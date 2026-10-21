package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// sessionAccessScope is the key level that may choose what a session's agent
// may do: the same level that answers its permission prompts.
const sessionAccessScope = "swarm:approve"

type sessionsV3AccessRequest struct {
	Tasks           string `json:"tasks,omitempty"`
	Machine         string `json:"machine,omitempty"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

// sessionAccessAuthority refuses callers that may not choose a session's
// access: a gateway (agent-bound) token, or a scoped key without
// swarm:approve. The owner's own session needs nothing more.
func sessionAccessAuthority(r *http.Request) error {
	if _, bound := r.Context().Value(applicationAgentContextKey{}).(applicationAgentBinding); bound {
		return errors.New("a gateway token cannot choose a session's access")
	}
	if record, ok := ScopedTokenFromRequest(r); ok && !record.HasScope(sessionAccessScope) {
		return errors.New("choosing a session's access needs the " + sessionAccessScope + " permission")
	}
	return nil
}

// handleSessionV3Access reads (GET) or changes (POST) what the person lets
// the session's agent do: start tasks freely or ask, and its access to the
// person's machine. Fields left out keep their value.
func (s *Server) handleSessionV3Access(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "access": pebblestore.SessionAccessFromMetadata(session.Metadata)})
		return
	}
	if err := sessionAccessAuthority(r); err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	var req sessionsV3AccessRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	change, err := pebblestore.NormalizeSessionAccess(pebblestore.SessionAccess{Tasks: req.Tasks, Machine: req.Machine})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	access := pebblestore.MergeSessionAccess(pebblestore.SessionAccessFromMetadata(session.Metadata), change)
	next := session
	next.Metadata = cloneSessionsV3Metadata(session.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata[pebblestore.SessionAccessMetadataKey] = pebblestore.SessionAccessMetadata(access)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"access": access})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "access": access})
}

// sessionAccessInstructions tells the agent, every turn, what the person lets
// this session do, so it neither asks for approval it does not need nor
// assumes access it does not have. A session without a choice gets nothing.
func sessionAccessInstructions(session pebblestore.SessionSnapshot) string {
	access := pebblestore.SessionAccessFromMetadata(session.Metadata)
	var lines []string
	switch access.Tasks {
	case pebblestore.SessionTasksFree:
		lines = append(lines, "- Tasks: the person lets you start tasks freely. When they ask for work, delegate it with manage_projects propose_task (coder, swarm or finder; a task_program for staged work) and it starts at once, with nothing for them to approve. Do not write a structured plan for them to review unless they ask for one, and pass auto_approve false only when they ask to review a task before it starts. They follow tasks in their app, stop one or message you to change course, and merge finished work themselves.")
	case pebblestore.SessionTasksAsk:
		lines = append(lines, "- Tasks: the person reviews each new task first. Every task card you create waits for their Approve before work starts (auto_approve is ignored); tell them it is waiting.")
	}
	switch access.Machine {
	case pebblestore.SessionMachineNone:
		lines = append(lines, "- The person's machine: you have no access to it. Your shell runs as Swarm's own user and sees only the project's folders.")
	case pebblestore.SessionMachineAsk:
		lines = append(lines, "- The person's machine: you have access, and the person runs or denies each command you ask to run on it.")
	case pebblestore.SessionMachineFull:
		lines = append(lines, "- The person's machine: you have full access; commands you run on it run as the person without asking.")
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\nSession access (the person's choice in their app; only they change it):\n" + strings.Join(lines, "\n")
}
