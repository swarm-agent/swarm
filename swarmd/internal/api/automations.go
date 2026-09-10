package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/automation"
	store "swarm/packages/swarmd/internal/store/pebble"
)

const AutomationsPath = "/v3/automations"

// AutomationCancellation must authorize the exact occurrence revision, durably
// request cancellation through canonical V3 execution, and reconcile the outcome.
// It must never mark an occurrence cancelled before its execution is stopped.
type AutomationCancellation interface {
	Cancel(context.Context, automation.Principal, store.AutomationScope, string, string, uint64, string) (store.AutomationRecord, error)
}

// AutomationEventVerifier verifies credentials bound to the exact authenticated
// account, workspace, automation, revision and event payload. Source names alone
// are not credentials. Verification must not consume the durable replay receipt.
type AutomationEventVerifier interface {
	VerifyEvent(*http.Request, automation.Principal, store.AutomationScope, string, uint64, automation.Trigger) error
}

type automationHTTPServices struct {
	domain    *automation.Service
	execution *automation.ExecutionService
	cancel    AutomationCancellation
	events    AutomationEventVerifier
	approval  *automation.PolicyApproval
}

// ConfigureAutomations is startup-only. Domain Access remains the live authority
// for workspace ownership and execution approval. No adapter means fail closed.
func (s *Server) ConfigureAutomations(domain *automation.Service, execution *automation.ExecutionService, cancel AutomationCancellation, events AutomationEventVerifier) {
	if s.automations == nil {
		s.automations = &automationHTTPServices{}
	}
	s.automations.domain, s.automations.execution, s.automations.cancel, s.automations.events = domain, execution, cancel, events
}

// ConfigureAutomationApproval installs the explicit-user approval authority at startup.
func (s *Server) ConfigureAutomationApproval(approval *automation.PolicyApproval) {
	if s.automations == nil {
		s.automations = &automationHTTPServices{}
	}
	s.automations.approval = approval
}

type automationHTTPRequest struct {
	PolicySHA256      string                      `json:"policy_sha256,omitempty"`
	ApprovalReference string                      `json:"approval_reference,omitempty"`
	Action            string                      `json:"action"`
	WorkspaceID       string                      `json:"workspace_id"`
	ID                string                      `json:"id"`
	MutationID        string                      `json:"mutation_id"`
	ExpectedRevision  uint64                      `json:"expected_revision"`
	Definition        *store.AutomationDefinition `json:"definition,omitempty"`
	UserInstructions  map[string]string           `json:"user_instructions,omitempty"`
	OccurrenceID      string                      `json:"occurrence_id,omitempty"`
	Source            string                      `json:"source,omitempty"`
	Identity          string                      `json:"identity,omitempty"`
	ScheduledAt       int64                       `json:"scheduled_at,omitempty"`
}

func decodeAutomationRequest(w http.ResponseWriter, r *http.Request, out *automationHTTPRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return automation.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return automation.ErrInvalid
	}
	return nil
}

func automationHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "automation operation failed"
	switch {
	case errors.Is(err, automation.ErrDenied):
		status, message = http.StatusForbidden, "automation access denied"
	case errors.Is(err, automation.ErrInvalid), errors.Is(err, store.ErrAutomationInvalid):
		status, message = http.StatusBadRequest, "invalid automation request"
	case errors.Is(err, store.ErrAutomationConflict):
		status, message = http.StatusConflict, "automation revision or idempotency conflict"
	case errors.Is(err, automation.ErrNotFound):
		status, message = http.StatusNotFound, "automation not found"
	}
	writeError(w, status, errors.New(message))
}

func (s *Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted principal required"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	approvalRoute := r.URL.Path == AutomationsPath+"/approve" || r.URL.Path == AutomationsPath+"/revoke"
	if r.URL.Path != AutomationsPath && !approvalRoute {
		http.NotFound(w, r)
		return
	}
	if approvalRoute && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.automations == nil || s.automations.domain == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("automation service unavailable"))
		return
	}
	p := automation.Principal{AccountID: principal.AccountScopeID, SubjectID: principal.UserID, Role: "user"}
	a := s.automations
	// Never upgrade a bound agent/system origin to an explicit user gesture.
	if existing, err := automation.RuntimePrincipal(r.Context()); err == nil && existing != p {
		automationHTTPError(w, automation.ErrDenied)
		return
	}
	ctx, bindErr := automation.BindRuntimeIdentity(r.Context(), principal, "user", "")
	if bindErr != nil {
		automationHTTPError(w, bindErr)
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		scope := store.AutomationScope{AccountID: p.AccountID, WorkspaceID: q.Get("workspace_id")}
		limit := 20
		var err error
		if q.Has("limit") {
			limit, err = strconv.Atoi(q.Get("limit"))
		}
		if err != nil || limit < 1 || limit > 50 {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		switch q.Get("action") {
		case "policy":
			rows, _, err := a.domain.History(ctx, p, scope, q.Get("id"), "definition", q.Get("id"), 0, 1)
			if err != nil {
				automationHTTPError(w, err)
				return
			}
			if len(rows) != 1 || rows[0].Definition == nil {
				automationHTTPError(w, automation.ErrNotFound)
				return
			}
			digest, err := automation.ApprovalPolicyDigest(*rows[0].Definition)
			if err != nil {
				automationHTTPError(w, err)
				return
			}
			var grant *store.AutomationApproval
			if a.approval != nil {
				grant, err = a.approval.CurrentGrant(ctx, p, rows[0])
				if err != nil {
					automationHTTPError(w, err)
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"record": rows[0], "policy_sha256": digest, "approval": grant})
		case "context":
			bundle, err := a.domain.Context(ctx, p, scope, q.Get("id"))
			if err != nil {
				automationHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"context": bundle})
		case "history", "get":
			before := uint64(0)
			if q.Has("before") {
				before, err = strconv.ParseUint(q.Get("before"), 10, 64)
			}
			if err != nil {
				automationHTTPError(w, automation.ErrInvalid)
				return
			}
			kind, recordID := q.Get("kind"), q.Get("record_id")
			if kind == "" {
				kind = "definition"
			}
			if recordID == "" {
				recordID = q.Get("id")
			}
			if q.Get("action") == "get" {
				limit = 1
				if q.Has("revision") {
					revision, parseErr := strconv.ParseUint(q.Get("revision"), 10, 64)
					if parseErr != nil || revision == 0 || revision == ^uint64(0) {
						automationHTTPError(w, automation.ErrInvalid)
						return
					}
					// History's exclusive boundary cannot address head+1; read a
					// bounded head page first, then use the exact historical boundary.
					head, _, readErr := a.domain.History(ctx, p, scope, q.Get("id"), kind, recordID, 0, 1)
					if readErr != nil {
						automationHTTPError(w, readErr)
						return
					}
					if len(head) == 0 || revision > head[0].Revision {
						automationHTTPError(w, automation.ErrNotFound)
						return
					}
					if revision == head[0].Revision {
						writeJSON(w, http.StatusOK, map[string]any{"records": head, "next_before": uint64(0)})
						return
					}
					before = revision + 1
				}
			}
			rows, next, err := a.domain.History(ctx, p, scope, q.Get("id"), kind, recordID, before, limit)
			if err != nil {
				automationHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"records": rows, "next_before": next})
		case "", "search", "list":
			kind := q.Get("kind")
			if q.Get("action") == "list" {
				kind = "definition"
			}
			rows, next, err := a.domain.Search(ctx, p, store.AutomationSearch{Scope: scope, AutomationID: q.Get("id"), Kind: kind, Query: q.Get("query"), Cursor: q.Get("cursor"), Limit: limit})
			if err != nil {
				automationHTTPError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"records": rows, "next_cursor": next})
		default:
			automationHTTPError(w, automation.ErrInvalid)
		}
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req automationHTTPRequest
	if err := decodeAutomationRequest(w, r, &req); err != nil {
		automationHTTPError(w, err)
		return
	}
	if approvalRoute {
		action := strings.TrimPrefix(r.URL.Path, AutomationsPath+"/")
		if req.Action != "" && req.Action != action {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		req.Action = action
	} else if req.Action == "approve" || req.Action == "revoke" {
		automationHTTPError(w, automation.ErrDenied)
		return
	}
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.WorkspaceID) == "" || req.MutationID == "" || len(req.MutationID) > 256 {
		automationHTTPError(w, automation.ErrInvalid)
		return
	}
	scope := store.AutomationScope{AccountID: p.AccountID, WorkspaceID: req.WorkspaceID}
	var record store.AutomationRecord
	var err error
	fresh := false
	switch req.Action {
	case "approve", "revoke":
		if a.approval == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("automation approval unavailable"))
			return
		}
		if req.ExpectedRevision == 0 {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		var grant store.AutomationApproval
		if req.Action == "approve" {
			grant, err = a.approval.ApproveUser(ctx, automation.ApprovalRequest{Scope: scope, AutomationID: req.ID, DefinitionRevision: req.ExpectedRevision, PolicySHA256: req.PolicySHA256})
		} else {
			if req.ApprovalReference == "" {
				automationHTTPError(w, automation.ErrInvalid)
				return
			}
			grant, err = a.approval.RevokeUser(ctx, scope, req.ApprovalReference, req.ExpectedRevision)
		}
		if err != nil {
			automationHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"approval": grant})
		return
	case "enable", "pause":
		rows, _, readErr := a.domain.History(ctx, p, scope, req.ID, "definition", req.ID, 0, 1)
		if readErr != nil {
			automationHTTPError(w, readErr)
			return
		}
		if len(rows) != 1 || rows[0].Definition == nil {
			automationHTTPError(w, automation.ErrNotFound)
			return
		}
		if req.ExpectedRevision == 0 || rows[0].Revision != req.ExpectedRevision {
			automationHTTPError(w, store.ErrAutomationConflict)
			return
		}
		definition := *rows[0].Definition
		definition.Enabled = req.Action == "enable"
		record, fresh, err = a.domain.SaveDefinition(ctx, p, scope, req.ID, req.MutationID, req.ExpectedRevision, definition)
	case "save":
		if req.Definition == nil {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		record, fresh, err = a.domain.SaveDefinition(ctx, p, scope, req.ID, req.MutationID, req.ExpectedRevision, *req.Definition)
	case "context":
		if req.UserInstructions == nil {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		record, fresh, err = a.domain.UpdateContext(ctx, p, scope, req.ID, req.MutationID, req.ExpectedRevision, req.UserInstructions, nil)
	case "run", "event":
		if a.execution == nil || (req.Action == "event" && a.events == nil) {
			writeError(w, http.StatusServiceUnavailable, errors.New("automation execution unavailable"))
			return
		}
		trigger := automation.Trigger{Kind: "manual", Identity: req.MutationID, ScheduledAt: req.ScheduledAt}
		if req.Action == "event" {
			trigger.Kind, trigger.Source, trigger.Identity = "event", req.Source, req.Identity
			if err := a.events.VerifyEvent(r.WithContext(ctx), p, scope, req.ID, req.ExpectedRevision, trigger); err != nil {
				automationHTTPError(w, automation.ErrDenied)
				return
			}
		} else if req.Source != "" || req.Identity != "" {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		record, err = a.execution.Admit(ctx, p, scope, req.ID, req.ExpectedRevision, trigger)
		if err == nil {
			// Admission is durable; dispatch/recovery belongs to the daemon. Never
			// claim a session started or retry execution on a notification failure.
			writeJSON(w, http.StatusAccepted, map[string]any{"record": record})
			return
		}
	case "cancel":
		if a.cancel == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("automation cancellation unavailable"))
			return
		}
		if req.ExpectedRevision == 0 || req.OccurrenceID == "" {
			automationHTTPError(w, automation.ErrInvalid)
			return
		}
		record, err = a.cancel.Cancel(ctx, p, scope, req.ID, req.OccurrenceID, req.ExpectedRevision, req.MutationID)
	default:
		err = automation.ErrInvalid
	}
	if err != nil {
		automationHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"record": record, "fresh": fresh})
}
