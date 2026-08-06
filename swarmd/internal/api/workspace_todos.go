package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/todo"
)

func (s *Server) handleWorkspaceTodos(w http.ResponseWriter, r *http.Request) {
	if s.todos == nil {
		writeError(w, http.StatusInternalServerError, errors.New("todo service not configured"))
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	resolveWorkspacePath := func(raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", errors.New("workspace path is required")
		}
		scope, err := s.workspace.ScopeForPathForPrincipal(principal, raw)
		if err != nil {
			return "", err
		}
		if scope.Matched && strings.TrimSpace(scope.WorkspacePath) != "" {
			return strings.TrimSpace(scope.WorkspacePath), nil
		}
		return "", errAccountOwnedWorkspacePathRequired
	}

	switch r.Method {
	case http.MethodGet:
		workspacePath, err := resolveWorkspacePath(r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		ownerKind, err := normalizeWorkspaceTodoOwnerKindRequest(r.URL.Query().Get("owner_kind"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
		items, summary, err := s.todos.List(workspacePath, todo.ListOptions{AccountScopeID: principal.AccountScopeID, OwnerKind: ownerKind, SessionID: sessionID})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_path": workspacePath, "owner_kind": ownerKind, "session_id": sessionID, "items": items, "summary": summary})
	case http.MethodPost:
		s.handleWorkspaceTodoMutation(w, r, principal, resolveWorkspacePath)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleWorkspaceTodoMutation(w http.ResponseWriter, r *http.Request, principal identity.Principal, resolveWorkspacePath func(string) (string, error)) {
	var req struct {
		Action          string   `json:"action"`
		WorkspacePath   string   `json:"workspace_path"`
		OwnerKind       string   `json:"owner_kind"`
		ID              string   `json:"id"`
		Text            string   `json:"text"`
		Done            *bool    `json:"done"`
		Priority        string   `json:"priority"`
		Group           string   `json:"group"`
		Tags            []string `json:"tags"`
		InProgress      *bool    `json:"in_progress"`
		SessionID       string   `json:"session_id"`
		OriginSessionID string   `json:"origin_session_id"`
		Mode            string   `json:"mode"`
		ParentID        string   `json:"parent_id"`
		OrderedIDs      []string `json:"ordered_ids"`
		Operations      []struct {
			Action     string   `json:"action"`
			ID         string   `json:"id"`
			OwnerKind  string   `json:"owner_kind"`
			Text       *string  `json:"text"`
			Done       *bool    `json:"done"`
			Priority   *string  `json:"priority"`
			Group      *string  `json:"group"`
			Tags       []string `json:"tags"`
			InProgress *bool    `json:"in_progress"`
			SessionID  *string  `json:"session_id"`
			ParentID   *string  `json:"parent_id"`
			OrderedIDs []string `json:"ordered_ids"`
		} `json:"operations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "upsert"
	}
	originSessionID := strings.TrimSpace(req.OriginSessionID)
	var origin pebblestore.SessionSnapshot
	var err error
	originLoaded := false
	if action == "ai_task" && originSessionID != "" {
		if s.sessions == nil {
			writeError(w, http.StatusInternalServerError, errors.New("session service not configured"))
			return
		}
		var found bool
		origin, found, err = s.sessions.GetSession(originSessionID)
		if err != nil || !found || strings.TrimSpace(origin.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) || strings.TrimSpace(origin.UserID) != strings.TrimSpace(principal.UserID) {
			writeError(w, http.StatusBadRequest, errors.New("origin session must belong to the request principal and canonical workspace"))
			return
		}
		originLoaded = true
	}
	workspacePath, err := resolveWorkspacePath(req.WorkspacePath)
	if err != nil && action == "ai_task" && originLoaded {
		workspacePath, err = s.resolveWorkspaceTodoAITaskCanonicalPath(principal, origin, req.WorkspacePath)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ownerKind, err := normalizeWorkspaceTodoOwnerKindRequest(req.OwnerKind)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	options := todo.ListOptions{AccountScopeID: principal.AccountScopeID, OwnerKind: ownerKind, SessionID: strings.TrimSpace(req.SessionID)}
	switch action {
	case "ai_task":
		if ownerKind != pebblestore.WorkspaceTodoOwnerKindUser {
			writeError(w, http.StatusBadRequest, errors.New("AI tasks must be user-owned"))
			return
		}
		if s.aiTasks == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("AI task queue is not configured"))
			return
		}
		workspaceScope, scopeErr := s.workspace.ScopeForPathForPrincipal(principal, workspacePath)
		if scopeErr != nil || strings.TrimSpace(workspaceScope.WorkspaceID) == "" {
			writeError(w, http.StatusBadRequest, errors.New("canonical workspace identity is required"))
			return
		}
		var originModelProfile *pebblestore.SessionModelProfileSnapshot
		if originLoaded {
			originWorkspaceMatches, originErr := s.workspaceTodoAITaskOriginMatchesCanonical(principal, origin, workspaceScope.WorkspaceID)
			if originErr != nil || !originWorkspaceMatches {
				writeError(w, http.StatusBadRequest, errors.New("origin session must belong to the request principal and canonical workspace"))
				return
			}
			originModelProfile = cloneSessionsV3ModelProfileSnapshotPointer(origin.ModelProfile)
			if originModelProfile == nil || strings.TrimSpace(originModelProfile.Action.Provider) == "" || strings.TrimSpace(originModelProfile.Action.Model) == "" {
				writeError(w, http.StatusBadRequest, errors.New("origin session is missing its immutable Action model selection"))
				return
			}
			mode := strings.ToLower(strings.TrimSpace(req.Mode))
			if mode == "" {
				mode = sessionruntime.ModeAuto
			}
			if mode == sessionruntime.ModePlan && (originModelProfile.Plan == nil || strings.TrimSpace(originModelProfile.Plan.Provider) == "" || strings.TrimSpace(originModelProfile.Plan.Model) == "") {
				writeError(w, http.StatusBadRequest, errors.New("origin session has Plan mode disabled"))
				return
			}
		}
		item, summary, _, replayed, err := s.todos.CreateAITaskWithReplay(todo.CreateAITaskInput{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, WorkspaceID: workspaceScope.WorkspaceID, WorkspacePath: workspacePath, OriginSessionID: originSessionID, ModelProfile: originModelProfile, Request: req.Text, Mode: req.Mode, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key"))})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !replayed {
			if publishErr := s.todos.PublishAITaskLifecycle(item); publishErr != nil {
				failed, _, _, _ := s.todos.TransitionAITask(todo.AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: item.WorkspacePath, ID: item.ID, ExpectedState: pebblestore.WorkspaceTodoAIStateQueued, ExpectedVersion: item.AIStateVersion, State: pebblestore.WorkspaceTodoAIStateFailed, Error: "publish queued task lifecycle: " + publishErr.Error(), Disposition: "publish_failed"})
				_ = s.todos.PublishAITaskLifecycle(failed)
				writeError(w, http.StatusServiceUnavailable, errors.New("AI task lifecycle is unavailable"))
				return
			}
			if !s.aiTasks.Enqueue(item) {
				failed, _, _, _ := s.todos.TransitionAITask(todo.AITaskTransitionInput{AccountScopeID: item.AccountScopeID, WorkspacePath: item.WorkspacePath, ID: item.ID, ExpectedState: pebblestore.WorkspaceTodoAIStateQueued, ExpectedVersion: item.AIStateVersion, State: pebblestore.WorkspaceTodoAIStateFailed, Error: "AI task queue rejected the accepted job", Disposition: "enqueue_failed"})
				_ = s.todos.PublishAITaskLifecycle(failed)
				writeError(w, http.StatusServiceUnavailable, errors.New("AI task queue is unavailable"))
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "item": item, "summary": summary, "status": item.AIState, "replayed": replayed})
	case "create":
		item, summary, _, err := s.todos.Create(todo.CreateInput{AccountScopeID: principal.AccountScopeID, WorkspacePath: workspacePath, OwnerKind: ownerKind, Text: req.Text, Priority: req.Priority, Group: req.Group, Tags: req.Tags, InProgress: req.InProgress != nil && *req.InProgress, SessionID: req.SessionID, ParentID: req.ParentID})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
	case "update", "upsert":
		item, summary, _, err := s.todos.Update(todo.UpdateInput{AccountScopeID: principal.AccountScopeID, WorkspacePath: workspacePath, ID: req.ID, Text: stringPointerIfPresent(req.Text), Done: req.Done, Priority: stringPointerIfPresent(req.Priority), Group: stringPointerIfPresent(req.Group), Tags: req.Tags, InProgress: req.InProgress, SessionID: stringPointerIfPresent(req.SessionID), ParentID: stringPointerIfPresent(req.ParentID)}, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
	case "delete":
		summary, _, err := s.todos.Delete(workspacePath, req.ID, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": strings.TrimSpace(req.ID), "summary": summary})
	case "delete_done":
		items, summary, _, err := s.todos.DeleteDone(workspacePath, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
	case "delete_all":
		items, summary, _, err := s.todos.DeleteAll(workspacePath, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
	case "reorder":
		items, summary, _, err := s.todos.Reorder(todo.ReorderInput{AccountScopeID: principal.AccountScopeID, WorkspacePath: workspacePath, OwnerKind: ownerKind, OrderedIDs: req.OrderedIDs}, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "summary": summary})
	case "in_progress":
		item, summary, _, err := s.todos.SetInProgress(workspacePath, req.ID, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item, "summary": summary})
	case "batch":
		operations := make([]todo.BatchOperation, 0, len(req.Operations))
		for _, rawOp := range req.Operations {
			opOwnerKind := rawOp.OwnerKind
			if strings.TrimSpace(opOwnerKind) == "" {
				opOwnerKind = ownerKind
			}
			operations = append(operations, todo.BatchOperation{Action: rawOp.Action, ID: rawOp.ID, OwnerKind: opOwnerKind, Text: rawOp.Text, Done: rawOp.Done, Priority: rawOp.Priority, Group: rawOp.Group, Tags: rawOp.Tags, InProgress: rawOp.InProgress, SessionID: rawOp.SessionID, ParentID: rawOp.ParentID, OrderedIDs: rawOp.OrderedIDs})
		}
		results, items, summary, _, err := s.todos.ApplyBatch(workspacePath, operations, options)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results, "items": items, "summary": summary, "operation_count": len(req.Operations)})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported todo action %q", action))
	}
}

func (s *Server) resolveWorkspaceTodoAITaskCanonicalPath(principal identity.Principal, origin pebblestore.SessionSnapshot, requestedPath string) (string, error) {
	if !origin.WorktreeEnabled || !sameWorkspaceTodoPath(requestedPath, origin.WorkspacePath) {
		return "", errAccountOwnedWorkspacePathRequired
	}
	binding, ok, err := s.workspaceTodoOriginBinding(principal, origin)
	if err != nil || !ok {
		return "", errAccountOwnedWorkspacePathRequired
	}
	return strings.TrimSpace(binding.SourceWorkspacePath), nil
}

func (s *Server) workspaceTodoAITaskOriginMatchesCanonical(principal identity.Principal, origin pebblestore.SessionSnapshot, canonicalWorkspaceID string) (bool, error) {
	binding, bound, err := s.workspaceTodoOriginBinding(principal, origin)
	if err != nil {
		return false, err
	}
	if bound {
		return strings.TrimSpace(binding.SourceWorkspaceID) == strings.TrimSpace(canonicalWorkspaceID), nil
	}
	if origin.WorktreeEnabled {
		return false, nil
	}
	originScope, err := s.workspace.ScopeForPathForPrincipal(principal, origin.WorkspacePath)
	return err == nil && originScope.Matched && strings.TrimSpace(originScope.WorkspaceID) == strings.TrimSpace(canonicalWorkspaceID), err
}

func (s *Server) workspaceTodoOriginBinding(principal identity.Principal, origin pebblestore.SessionSnapshot) (pebblestore.TopologyWorkspaceBindingRecord, bool, error) {
	bindingID := firstNonEmpty(
		strings.TrimSpace(sessionsV3MetadataString(origin.Metadata, "swarm_v3_workspace_binding_id")),
		strings.TrimSpace(sessionsV3MetadataString(origin.Metadata, "local_workspace_binding_id")),
	)
	if bindingID == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, nil
	}
	if s.topology == nil {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("topology service not configured")
	}
	binding, ok, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, bindingID)
	if err != nil || !ok {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) ||
		(strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID)) ||
		!strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) ||
		strings.TrimSpace(binding.SourceWorkspaceID) == "" || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("origin session workspace binding is invalid")
	}
	entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, binding.SourceWorkspaceID)
	if err != nil || !found {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, err
	}
	if !sameWorkspaceTodoPath(entry.Path, binding.SourceWorkspacePath) || entry.WorkspaceGeneration != binding.SourceWorkspaceGeneration {
		return pebblestore.TopologyWorkspaceBindingRecord{}, false, errors.New("origin session workspace binding is stale")
	}
	return binding, true, nil
}

func sameWorkspaceTodoPath(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && right != "" && filepath.Clean(left) == filepath.Clean(right)
}

func stringPointerIfPresent(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	copyValue := value
	return &copyValue
}

func normalizeWorkspaceTodoOwnerKindRequest(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	normalized, ok := pebblestore.ParseWorkspaceTodoOwnerKind(trimmed)
	if !ok {
		return "", fmt.Errorf("owner_kind must be user or agent")
	}
	return normalized, nil
}
