package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const WorkersPath = "/v3/workers"

func workerHTTPError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, pebblestore.ErrWorkerNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, pebblestore.ErrWorkerConflict) || errors.Is(err, pebblestore.ErrActiveScheduleUpdateRejected) {
		writeError(w, http.StatusConflict, err)
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "not found") {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if strings.Contains(msg, "conflict") || strings.Contains(msg, "revision") || strings.Contains(msg, "cannot update active scheduled worker") {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func (s *Server) authenticateWorkerRequest(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := PrincipalFromRequest(r)
	if !ok || !p.Valid() {
		writeError(w, http.StatusUnauthorized, errors.New("trusted user required"))
		return identity.Principal{}, false
	}
	want := automation.Principal{AccountID: p.AccountScopeID, SubjectID: p.UserID, Role: "user"}
	if p.Type != "user" {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return identity.Principal{}, false
	}
	if bound, err := automation.RuntimePrincipal(r.Context()); err == nil && bound != want {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return identity.Principal{}, false
	}
	if _, err := automation.BindRuntimeIdentity(r.Context(), p, "user", ""); err != nil {
		writeError(w, http.StatusForbidden, errors.New("explicit user required"))
		return identity.Principal{}, false
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("session service unavailable"))
		return identity.Principal{}, false
	}
	return p, true
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticateWorkerRequest(w, r)
	if !ok {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, WorkersPath)
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		s.handleWorkerCollection(w, r, p)
		return
	}

	// Dispatch top-level worker actions
	if path == "validate" {
		s.handleWorkerValidate(w, r, p)
		return
	}
	if path == "import" {
		s.handleWorkerImport(w, r, p)
		return
	}
	if path == "migrate" {
		s.handleWorkerMigrate(w, r, p)
		return
	}

	// Single worker routes: /v3/workers/{id}...
	parts := strings.Split(path, "/")
	workerID := strings.TrimSpace(parts[0])
	if workerID == "" {
		writeError(w, http.StatusBadRequest, errors.New("worker id is required"))
		return
	}

	if len(parts) == 1 {
		s.handleWorkerByID(w, r, p, workerID)
		return
	}

	sub := parts[1]
	switch sub {
	case "export":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerExport(w, r, p, workerID)
	case "automations":
		s.handleWorkerAutomations(w, r, p, workerID, parts[2:])
	case "history":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerHistory(w, r, p, workerID)
	case "revisions":
		if len(parts) != 3 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerRevisionByNumber(w, r, p, workerID, parts[2])
	case "runs":
		if len(parts) == 2 {
			s.handleWorkerRuns(w, r, p, workerID)
		} else if len(parts) == 3 {
			s.handleWorkerRunByID(w, r, p, workerID, parts[2])
		} else {
			writeError(w, http.StatusNotFound, errors.New("not found"))
		}
	default:
		writeError(w, http.StatusNotFound, errors.New("not found"))
	}
}

type createWorkerRequestBody struct {
	ID                    string                                  `json:"id,omitempty"`
	Name                  string                                  `json:"name"`
	Description           string                                  `json:"description,omitempty"`
	Instructions          string                                  `json:"instructions,omitempty"`
	RequestedCapabilities []pebblestore.WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []pebblestore.WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	LocalBindings         map[string]string                       `json:"local_bindings,omitempty"`
	Automations           []pebblestore.WorkerAutomationDefinition `json:"automations,omitempty"`
	Metadata              map[string]any                          `json:"metadata,omitempty"`
	IdempotencyKey        string                                  `json:"idempotency_key,omitempty"`
}

func (s *Server) handleWorkerCollection(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	switch r.Method {
	case http.MethodGet:
		if !s.requireScope(w, r, "automations:read") {
			return
		}
		if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
			if scopedRec.WorkerID != "" {
				writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot enumerate workers"))
				return
			}
		}

		q := r.URL.Query()
		for key := range q {
			if key != "limit" && key != "cursor" && key != "lifecycle_state" && key != "include_deleted" {
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid query parameter: %q", key))
				return
			}
		}

		limit := 50
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 100 {
				writeError(w, http.StatusBadRequest, errors.New("limit must be an integer between 1 and 100"))
				return
			}
			limit = n
		}

		cursor := strings.TrimSpace(q.Get("cursor"))
		if len(cursor) > 1024 {
			writeError(w, http.StatusBadRequest, errors.New("cursor exceeds maximum length of 1024"))
			return
		}

		var lifecycleState pebblestore.WorkerLifecycleState
		if q.Has("lifecycle_state") {
			val := strings.TrimSpace(q.Get("lifecycle_state"))
			switch pebblestore.WorkerLifecycleState(val) {
			case pebblestore.WorkerLifecycleStateIdle,
				pebblestore.WorkerLifecycleStateActive,
				pebblestore.WorkerLifecycleStatePaused,
				pebblestore.WorkerLifecycleStateArchived,
				pebblestore.WorkerLifecycleStateDeleted:
				lifecycleState = pebblestore.WorkerLifecycleState(val)
			default:
				writeError(w, http.StatusBadRequest, fmt.Errorf("invalid lifecycle_state: %q", val))
				return
			}
		}

		includeDeleted := false
		if q.Has("include_deleted") {
			val, err := strconv.ParseBool(q.Get("include_deleted"))
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("include_deleted must be a boolean"))
				return
			}
			includeDeleted = val
		}

		res, err := s.sessions.ListWorkers(p.AccountScopeID, pebblestore.ListWorkersQuery{
			Limit:          limit,
			After:          cursor,
			LifecycleState: lifecycleState,
			IncludeDeleted: includeDeleted,
		})
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		if res.Workers == nil {
			res.Workers = []pebblestore.WorkerRecord{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"workers":     res.Workers,
			"next_cursor": res.NextCursor,
			"total_count": res.TotalCount,
		})

	case http.MethodPost:
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
			if scopedRec.WorkerID != "" {
				writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
				return
			}
		}

		if len(r.URL.Query()) > 0 {
			writeError(w, http.StatusBadRequest, errors.New("unexpected query parameters on worker creation"))
			return
		}

		var req createWorkerRequestBody
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			workerHTTPError(w, fmt.Errorf("invalid json payload: %w", err))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			workerHTTPError(w, errors.New("unexpected trailing data in payload"))
			return
		}

		idempKey := strings.TrimSpace(req.IdempotencyKey)
		if idempKey == "" {
			if headerIdemp := strings.TrimSpace(r.Header.Get("Idempotency-Key")); headerIdemp != "" {
				idempKey = headerIdemp
			} else if headerIdemp := strings.TrimSpace(r.Header.Get("X-Idempotency-Key")); headerIdemp != "" {
				idempKey = headerIdemp
			}
		}

		created, err := s.sessions.CreateWorker(r.Context(), p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{
			ID:                    req.ID,
			Name:                  req.Name,
			Description:           req.Description,
			Instructions:          req.Instructions,
			RequestedCapabilities: req.RequestedCapabilities,
			WorkspaceRequirements: req.WorkspaceRequirements,
			LocalBindings:         req.LocalBindings,
			Automations:           req.Automations,
			Metadata:              req.Metadata,
			IdempotencyKey:        idempKey,
		})
		if err != nil {
			workerHTTPError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"worker": created,
		})

	default:
		methodNotAllowed(w)
	}
}

type updateWorkerRequestBody struct {
	ExpectedRevision      uint64                                   `json:"expected_revision,omitempty"`
	Name                  *string                                  `json:"name,omitempty"`
	Description           *string                                  `json:"description,omitempty"`
	Instructions          *string                                  `json:"instructions,omitempty"`
	RequestedCapabilities []pebblestore.WorkerCapabilityRequest     `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []pebblestore.WorkerWorkspaceRequirement  `json:"workspace_requirements,omitempty"`
	LocalBindings         map[string]string                        `json:"local_bindings,omitempty"`
	Automations           []pebblestore.WorkerAutomationDefinition  `json:"automations,omitempty"`
	Metadata              map[string]any                           `json:"metadata,omitempty"`
	ChangeSummary         string                                   `json:"change_summary,omitempty"`
}

func (s *Server) handleWorkerByID(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		if !s.requireScope(w, r, "automations:read") {
			return
		}
		if len(r.URL.Query()) > 0 {
			writeError(w, http.StatusBadRequest, errors.New("unexpected query parameters"))
			return
		}

		record, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		if !found || record.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
			writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"worker": record,
		})

	case http.MethodPut:
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
			if scopedRec.WorkerID != "" {
				writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
				return
			}
		}

		var req updateWorkerRequestBody
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			workerHTTPError(w, fmt.Errorf("invalid json payload: %w", err))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			workerHTTPError(w, errors.New("unexpected trailing data in payload"))
			return
		}

		expectedRevision := req.ExpectedRevision
		if expectedRevision == 0 && r.URL.Query().Has("expected_revision") {
			if v, err := strconv.ParseUint(r.URL.Query().Get("expected_revision"), 10, 64); err == nil {
				expectedRevision = v
			}
		}
		if expectedRevision == 0 {
			if match := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			} else if match := strings.TrimSpace(r.Header.Get("X-Expected-Revision")); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			}
		}
		if expectedRevision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision is required"))
			return
		}

		updated, err := s.sessions.UpdateWorker(p.AccountScopeID, p.UserID, workerID, expectedRevision, pebblestore.UpdateWorkerRequest{
			Name:                  req.Name,
			Description:           req.Description,
			Instructions:          req.Instructions,
			RequestedCapabilities: req.RequestedCapabilities,
			WorkspaceRequirements: req.WorkspaceRequirements,
			LocalBindings:         req.LocalBindings,
			Automations:           req.Automations,
			Metadata:              req.Metadata,
			ChangeSummary:         req.ChangeSummary,
		})
		if err != nil {
			workerHTTPError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"worker": updated,
		})

	case http.MethodDelete:
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
			if scopedRec.WorkerID != "" {
				writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
				return
			}
		}

		var expectedRevision uint64
		if r.URL.Query().Has("expected_revision") {
			if v, err := strconv.ParseUint(r.URL.Query().Get("expected_revision"), 10, 64); err == nil {
				expectedRevision = v
			}
		}
		if expectedRevision == 0 {
			if match := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			} else if match := strings.TrimSpace(r.Header.Get("X-Expected-Revision")); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			}
		}
		if expectedRevision == 0 && r.Body != nil && r.ContentLength > 0 {
			var delReq struct {
				ExpectedRevision uint64 `json:"expected_revision"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&delReq); err == nil {
				expectedRevision = delReq.ExpectedRevision
			}
		}
		if expectedRevision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision is required"))
			return
		}

		if err := s.sessions.DeleteWorker(p.AccountScopeID, p.UserID, workerID, expectedRevision); err != nil {
			workerHTTPError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
		})

	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleWorkerValidate(w http.ResponseWriter, r *http.Request, _ identity.Principal) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "automations:read", "automations:write") {
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	if len(bodyBytes) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("empty worker definition payload"))
		return
	}

	targetBytes := extractWorkerPayloadBytes(bodyBytes)
	def, err := s.sessions.ValidatePortableWorkerDefinition(targetBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"valid":  true,
		"worker": def,
	})
}

func (s *Server) handleWorkerImport(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
			return
		}
	}

	q := r.URL.Query()
	for key := range q {
		if key != "mode" && key != "worker_id" && key != "expected_revision" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid query parameter: %q", key))
			return
		}
	}

	mode := strings.TrimSpace(q.Get("mode"))
	if mode == "" {
		mode = "new"
	}
	if mode != "new" && mode != "update" {
		writeError(w, http.StatusBadRequest, errors.New("mode must be either 'new' or 'update'"))
		return
	}

	var workerID string
	var expectedRevision uint64
	if mode == "update" {
		workerID = strings.TrimSpace(q.Get("worker_id"))
		if workerID == "" {
			writeError(w, http.StatusBadRequest, errors.New("worker_id is required for update mode"))
			return
		}
		revStr := strings.TrimSpace(q.Get("expected_revision"))
		if revStr == "" {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision is required for update mode"))
			return
		}
		v, err := strconv.ParseUint(revStr, 10, 64)
		if err != nil || v == 0 {
			writeError(w, http.StatusBadRequest, errors.New("invalid expected_revision"))
			return
		}
		expectedRevision = v
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
		return
	}
	if len(bodyBytes) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("empty worker definition payload"))
		return
	}

	targetBytes := extractWorkerPayloadBytes(bodyBytes)

	if mode == "update" {
		rec, err := s.sessions.ImportWorkerUpdate(p.AccountScopeID, p.UserID, workerID, expectedRevision, targetBytes)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"worker": rec,
		})
		return
	}

	rec, err := s.sessions.ImportWorkerAsNew(p.AccountScopeID, p.UserID, targetBytes)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"worker": rec,
	})
}

func (s *Server) handleWorkerMigrate(w http.ResponseWriter, r *http.Request, p identity.Principal) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
			return
		}
	}
	if len(r.URL.Query()) > 0 {
		writeError(w, http.StatusBadRequest, errors.New("unexpected query parameters on worker migration"))
		return
	}

	if r.Body != nil && r.ContentLength > 0 {
		var empty struct{}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&empty); err != nil && err != io.EOF {
			workerHTTPError(w, fmt.Errorf("invalid json payload: %w", err))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			workerHTTPError(w, errors.New("unexpected trailing data in payload"))
			return
		}
	}

	summary, err := s.sessions.MigrateLegacyAutomationsV2(p.AccountScopeID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summary": summary,
	})
}

func (s *Server) handleWorkerExport(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:read") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	def, rawJSON, err := s.sessions.ExportWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	q := r.URL.Query()
	if q.Get("raw") == "true" || q.Get("format") == "raw" || strings.Contains(r.Header.Get("Accept"), "application/octet-stream") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rawJSON)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": def,
	})
}

type attachWorkerAutomationRequestBody struct {
	ExpectedWorkerRevision  uint64                                     `json:"expected_worker_revision,omitempty"`
	Automation              *pebblestore.WorkerAutomationDefinition    `json:"automation,omitempty"`
	ID                      string                                     `json:"id,omitempty"`
	Name                    string                                     `json:"name,omitempty"`
	Description             string                                     `json:"description,omitempty"`
	ActivationMode          string                                     `json:"activation_mode,omitempty"`
	Schedule                *pebblestore.AutomationV2Schedule          `json:"schedule,omitempty"`
	Trigger                 *pebblestore.WorkerTriggerConfig           `json:"trigger,omitempty"`
	Enabled                 *bool                                      `json:"enabled,omitempty"`
	PlanDocument            *pebblestore.SessionPlanDocument           `json:"plan_document,omitempty"`
	Plan                    *pebblestore.SessionPlanDocument           `json:"plan,omitempty"`
	InputRequirements       []pebblestore.WorkerInputRequirement       `json:"input_requirements,omitempty"`
	DeliverableRequirements []pebblestore.WorkerDeliverableRequirement `json:"deliverable_requirements,omitempty"`
}

func (s *Server) handleWorkerAutomations(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string, subparts []string) {
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot manage workers"))
			return
		}
	}

	if len(subparts) == 0 {
		// POST /v3/workers/{id}/automations
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !s.requireScope(w, r, "automations:write") {
			return
		}

		var req attachWorkerAutomationRequestBody
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			workerHTTPError(w, fmt.Errorf("invalid json payload: %w", err))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			workerHTTPError(w, errors.New("unexpected trailing data in payload"))
			return
		}

		expectedRevision := req.ExpectedWorkerRevision
		if expectedRevision == 0 && r.URL.Query().Has("expected_worker_revision") {
			if v, err := strconv.ParseUint(r.URL.Query().Get("expected_worker_revision"), 10, 64); err == nil {
				expectedRevision = v
			}
		}
		if expectedRevision == 0 {
			if match := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			} else if match := strings.TrimSpace(r.Header.Get("X-Expected-Revision")); match != "" {
				if v, err := strconv.ParseUint(match, 10, 64); err == nil {
					expectedRevision = v
				}
			}
		}
		if expectedRevision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision is required"))
			return
		}

		autoDef := resolveWorkerAutomationDefinition(req)
		updated, err := s.sessions.AttachWorkerAutomation(p.AccountScopeID, p.UserID, workerID, expectedRevision, autoDef)
		if err != nil {
			workerHTTPError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"worker": updated,
		})
		return
	}

	if len(subparts) == 1 {
		automationID := strings.TrimSpace(subparts[0])
		if automationID == "" {
			writeError(w, http.StatusBadRequest, errors.New("automation id is required"))
			return
		}

		switch r.Method {
		case http.MethodPut:
			if !s.requireScope(w, r, "automations:write") {
				return
			}
			var req attachWorkerAutomationRequestBody
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				workerHTTPError(w, fmt.Errorf("invalid json payload: %w", err))
				return
			}
			if err := dec.Decode(new(any)); err != io.EOF {
				workerHTTPError(w, errors.New("unexpected trailing data in payload"))
				return
			}

			expectedRevision := req.ExpectedWorkerRevision
			if expectedRevision == 0 && r.URL.Query().Has("expected_worker_revision") {
				if v, err := strconv.ParseUint(r.URL.Query().Get("expected_worker_revision"), 10, 64); err == nil {
					expectedRevision = v
				}
			}
			if expectedRevision == 0 {
				if match := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); match != "" {
					if v, err := strconv.ParseUint(match, 10, 64); err == nil {
						expectedRevision = v
					}
				} else if match := strings.TrimSpace(r.Header.Get("X-Expected-Revision")); match != "" {
					if v, err := strconv.ParseUint(match, 10, 64); err == nil {
						expectedRevision = v
					}
				}
			}
			if expectedRevision == 0 {
				writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision is required"))
				return
			}

			autoDef := resolveWorkerAutomationDefinition(req)
			updated, err := s.sessions.UpdateWorkerAutomation(p.AccountScopeID, p.UserID, workerID, automationID, expectedRevision, autoDef)
			if err != nil {
				workerHTTPError(w, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"worker": updated,
			})

		case http.MethodDelete:
			if !s.requireScope(w, r, "automations:write") {
				return
			}
			var expectedRevision uint64
			if r.URL.Query().Has("expected_worker_revision") {
				if v, err := strconv.ParseUint(r.URL.Query().Get("expected_worker_revision"), 10, 64); err == nil {
					expectedRevision = v
				}
			}
			if expectedRevision == 0 {
				if match := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`); match != "" {
					if v, err := strconv.ParseUint(match, 10, 64); err == nil {
						expectedRevision = v
					}
				} else if match := strings.TrimSpace(r.Header.Get("X-Expected-Revision")); match != "" {
					if v, err := strconv.ParseUint(match, 10, 64); err == nil {
						expectedRevision = v
					}
				}
			}
			if expectedRevision == 0 && r.Body != nil && r.ContentLength > 0 {
				var delReq struct {
					ExpectedWorkerRevision uint64 `json:"expected_worker_revision"`
				}
				dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&delReq); err == nil {
					expectedRevision = delReq.ExpectedWorkerRevision
				}
			}
			if expectedRevision == 0 {
				writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision is required"))
				return
			}

			updated, err := s.sessions.RemoveWorkerAutomation(p.AccountScopeID, p.UserID, workerID, automationID, expectedRevision)
			if err != nil {
				workerHTTPError(w, err)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"worker": updated,
			})

		default:
			methodNotAllowed(w)
		}
		return
	}

	writeError(w, http.StatusNotFound, errors.New("not found"))
}

func (s *Server) handleWorkerHistory(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:read") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	q := r.URL.Query()
	limit := 50
	if q.Has("limit") {
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	after := strings.TrimSpace(q.Get("cursor"))

	revs, next, err := s.sessions.GetWorkerHistory(p.AccountScopeID, workerID, limit, after)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if revs == nil {
		revs = []pebblestore.WorkerRevisionRecord{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revisions":   revs,
		"next_cursor": next,
	})
}

func (s *Server) handleWorkerRevisionByNumber(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, revStr string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:read") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	rev, err := strconv.ParseUint(revStr, 10, 64)
	if err != nil || rev == 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid revision number"))
		return
	}

	revRecord, found, err := s.sessions.GetWorkerRevision(p.AccountScopeID, workerID, rev)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("revision not found"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revision": revRecord,
	})
}

func (s *Server) handleWorkerRuns(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:read") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	q := r.URL.Query()
	limit := 50
	if q.Has("limit") {
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	after := strings.TrimSpace(q.Get("cursor"))

	runs, next, err := s.sessions.ListWorkerRuns(p.AccountScopeID, workerID, limit, after)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if runs == nil {
		runs = []pebblestore.WorkerRunRecord{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"runs":        runs,
		"next_cursor": next,
	})
}

func (s *Server) handleWorkerRunByID(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, runID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:read") {
		return
	}
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	run, found, err := s.sessions.GetWorkerRun(p.AccountScopeID, workerID, runID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("worker run not found"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run": run,
	})
}

func extractWorkerPayloadBytes(data []byte) []byte {
	var env struct {
		Worker json.RawMessage `json:"worker"`
	}
	if err := json.Unmarshal(data, &env); err == nil && len(env.Worker) > 0 {
		return env.Worker
	}
	return data
}

func resolveWorkerAutomationDefinition(req attachWorkerAutomationRequestBody) pebblestore.WorkerAutomationDefinition {
	if req.Automation != nil {
		return *req.Automation
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	var planDoc pebblestore.SessionPlanDocument
	if req.PlanDocument != nil {
		planDoc = *req.PlanDocument
	} else if req.Plan != nil {
		planDoc = *req.Plan
	}
	return pebblestore.WorkerAutomationDefinition{
		ID:                      req.ID,
		Name:                    req.Name,
		Description:             req.Description,
		ActivationMode:          req.ActivationMode,
		Schedule:                req.Schedule,
		Trigger:                 req.Trigger,
		Enabled:                 enabled,
		PlanDocument:            planDoc,
		InputRequirements:       req.InputRequirements,
		DeliverableRequirements: req.DeliverableRequirements,
	}
}
