package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	actionruntime "swarm/packages/swarmd/internal/action"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type workspaceActionRequest struct {
	Action        string                              `json:"action"`
	WorkspacePath string                              `json:"workspace_path"`
	ID            string                              `json:"id"`
	Name          *string                             `json:"name"`
	Description   *string                             `json:"description"`
	Icon          *string                             `json:"icon"`
	Entrypoint    *string                             `json:"entrypoint"`
	Arguments     *[]string                           `json:"arguments"`
	Inputs        *[]pebblestore.WorkspaceActionInput `json:"inputs"`
	Pinned        *bool                               `json:"pinned"`
	OrderedIDs    []string                            `json:"ordered_ids"`
}

func (s *Server) handleWorkspaceActions(w http.ResponseWriter, r *http.Request) {
	if s.actions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("action service not configured"))
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errors.New("workspace service not configured"))
		return
	}
	if _, ok := PrincipalFromRequest(r); !ok {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	resolve := func(rawPath string) (actionruntime.Scope, error) {
		return s.resolveWorkspaceActionScope(r, rawPath, r.URL.Query().Get("session_id"))
	}

	switch r.Method {
	case http.MethodGet:
		scope, err := resolve(r.URL.Query().Get("workspace_path"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" {
			action, found, err := s.actions.Get(scope, id)
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if !found {
				writeError(w, http.StatusNotFound, fmt.Errorf("action %q not found", id))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_path": scope.WorkspacePath, "workspace_id": scope.WorkspaceID, "action": action})
			return
		}
		actions, err := s.actions.List(scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "workspace_path": scope.WorkspacePath, "workspace_id": scope.WorkspaceID, "actions": actions, "count": len(actions)})
	case http.MethodPost:
		var req workspaceActionRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		scope, err := resolve(req.WorkspacePath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.handleWorkspaceActionMutation(w, scope, req)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleWorkspaceActionMutation(w http.ResponseWriter, scope actionruntime.Scope, req workspaceActionRequest) {
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "create":
		if req.Name == nil || req.Entrypoint == nil {
			writeError(w, http.StatusBadRequest, errors.New("name and entrypoint are required"))
			return
		}
		action, err := s.actions.Create(actionruntime.CreateInput{Scope: scope, Name: *req.Name, Description: dereferenceString(req.Description), Icon: dereferenceString(req.Icon), Entrypoint: *req.Entrypoint, Arguments: dereferenceStringSlice(req.Arguments), Inputs: dereferenceActionInputs(req.Inputs), Pinned: dereferenceBool(req.Pinned)})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "action": action})
	case "update":
		action, err := s.actions.Update(actionruntime.UpdateInput{Scope: scope, ID: req.ID, Name: req.Name, Description: req.Description, Icon: req.Icon, Entrypoint: req.Entrypoint, Arguments: req.Arguments, Inputs: req.Inputs, Pinned: req.Pinned})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action})
	case "delete":
		deleted, err := s.actions.Delete(scope, req.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !deleted {
			writeError(w, http.StatusNotFound, fmt.Errorf("action %q not found", strings.TrimSpace(req.ID)))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": strings.TrimSpace(req.ID)})
	case "reorder":
		actions, err := s.actions.Reorder(scope, req.OrderedIDs)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "actions": actions, "count": len(actions)})
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("unsupported action management operation %q", strings.TrimSpace(req.Action)))
	}
}

func dereferenceBool(value *bool) bool {
	return value != nil && *value
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func dereferenceStringSlice(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func dereferenceActionInputs(value *[]pebblestore.WorkspaceActionInput) []pebblestore.WorkspaceActionInput {
	if value == nil {
		return nil
	}
	return append([]pebblestore.WorkspaceActionInput(nil), (*value)...)
}
