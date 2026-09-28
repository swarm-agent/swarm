package api

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const WorkersPath = "/v3/workers"

func workerHTTPError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, pebblestore.ErrWorkerNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if errors.Is(err, pebblestore.ErrWorkerConflict) || errors.Is(err, pebblestore.ErrActiveScheduleUpdateRejected) {
		writeError(w, http.StatusConflict, err)
		return
	}
	if errors.Is(err, automation.ErrDenied) || errors.Is(err, identity.ErrProductIdentityRequired) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func parseAndValidateQuery(r *http.Request, allowedKeys ...string) (url.Values, error) {
	if r.URL.RawQuery == "" {
		return url.Values{}, nil
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("malformed query string: %w", err)
	}
	allowed := make(map[string]bool, len(allowedKeys))
	for _, k := range allowedKeys {
		allowed[k] = true
	}
	for k, vals := range q {
		if !allowed[k] {
			return nil, fmt.Errorf("unexpected query parameter: %q", k)
		}
		if len(vals) != 1 {
			return nil, fmt.Errorf("duplicate query parameter: %q", k)
		}
	}
	return q, nil
}

func isTriggerCredential(r *pebblestore.ScopedTokenRecord) bool {
	if r == nil {
		return false
	}
	// Note: HasScope handles wildcard (*, admin, automations:*, workers:*),
	// so calling r.HasScope("automations:trigger") on a wildcard/admin token returns true.
	// A true trigger credential explicitly declares trigger scope and does not have full-scope / admin wildcard.
	hasExplicitTrigger := false
	for _, s := range r.Scopes {
		clean := strings.ToLower(strings.TrimSpace(s))
		if clean == "*" || clean == "admin" || clean == "automations:*" || clean == "workers:*" {
			return false
		}
		if clean == "automations:trigger" || clean == "workers:trigger" {
			hasExplicitTrigger = true
		}
	}
	return hasExplicitTrigger
}

func decodeJSONStrict(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid json payload: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing data in payload")
	}
	return nil
}

func readRawJSONStrict(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return nil, errors.New("empty worker definition payload")
	}
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	var dummy any
	if err := dec.Decode(&dummy); err != nil {
		return nil, fmt.Errorf("invalid json payload: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("unexpected trailing data in payload")
	}
	return bodyBytes, nil
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
	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if isTriggerCredential(scopedRec) {
			path := strings.TrimPrefix(r.URL.Path, WorkersPath)
			path = strings.TrimPrefix(path, "/")
			parts := strings.Split(path, "/")
			isTriggerPath := r.Method == http.MethodPost && (
				(len(parts) == 2 && parts[1] == "trigger") ||
				(len(parts) == 4 && parts[1] == "automations" && parts[3] == "trigger"))
			if !isTriggerPath {
				writeError(w, http.StatusForbidden, errors.New("scoped trigger token cannot access worker api"))
				return identity.Principal{}, false
			}
			if scopedRec.WorkerID != "" && parts[0] != scopedRec.WorkerID {
				writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
				return identity.Principal{}, false
			}
		}
	}
	if s.sessions == nil || s.sessions.Store() == nil || s.sessions.Store().Underlying() == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("identity store unavailable"))
		return identity.Principal{}, false
	}
	ids := pebblestore.NewIdentityStore(s.sessions.Store().Underlying())
	m, found, err := ids.GetAccountUser(p.AccountScopeID, p.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return identity.Principal{}, false
	}
	if !found || !strings.EqualFold(strings.TrimSpace(m.Status), pebblestore.AccountUserStatusActive) || m.UserID != p.UserID || m.AccountScopeID != p.AccountScopeID {
		writeError(w, http.StatusForbidden, errors.New("active account membership required"))
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

	scopedRec, hasScopedToken := ScopedTokenFromRequest(r)

	if path == "" {
		s.handleWorkerCollection(w, r, p)
		return
	}

	// Dispatch top-level worker actions
	if path == "validate" {
		if hasScopedToken && scopedRec != nil && scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped token cannot validate workers"))
			return
		}
		s.handleWorkerValidate(w, r, p)
		return
	}
	if path == "import" {
		if hasScopedToken && scopedRec != nil && scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped token cannot manage workers"))
			return
		}
		s.handleWorkerImport(w, r, p)
		return
	}
	if path == "migrate" {
		if hasScopedToken && scopedRec != nil && scopedRec.WorkerID != "" {
			writeError(w, http.StatusForbidden, errors.New("scoped token cannot manage workers"))
			return
		}
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
	if hasScopedToken && scopedRec != nil && scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
		writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
		return
	}

	if len(parts) == 1 {
		s.handleWorkerByID(w, r, p, workerID)
		return
	}

	sub := parts[1]
	switch sub {
	case "activate", "deploy":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerActivate(w, r, p, workerID)
	case "pause":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerPause(w, r, p, workerID)
	case "resume":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerResume(w, r, p, workerID)
	case "archive":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerArchive(w, r, p, workerID)
	case "delete":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerDelete(w, r, p, workerID)
	case "direct", "request":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerDirectRequest(w, r, p, workerID)
	case "test", "test-run":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerTestRun(w, r, p, workerID)
	case "trigger":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerTrigger(w, r, p, workerID, "")
	case "token":
		if len(parts) != 2 {
			writeError(w, http.StatusNotFound, errors.New("not found"))
			return
		}
		s.handleWorkerToken(w, r, p, workerID)
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
		} else if len(parts) == 4 && parts[3] == "cancel" {
			s.handleWorkerRunCancel(w, r, p, workerID, parts[2])
		} else {
			writeError(w, http.StatusNotFound, errors.New("not found"))
		}
	default:
		writeError(w, http.StatusNotFound, errors.New("not found"))
	}
}

type createWorkerRequestBody struct {
	Name                  string                                   `json:"name"`
	Description           string                                   `json:"description,omitempty"`
	Instructions          string                                   `json:"instructions,omitempty"`
	RequestedCapabilities []pebblestore.WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []pebblestore.WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	Automations           []pebblestore.WorkerAutomationDefinition `json:"automations,omitempty"`
	Metadata              map[string]any                           `json:"metadata,omitempty"`
	IdempotencyKey        string                                   `json:"idempotency_key"`
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

		q, err := parseAndValidateQuery(r, "limit", "cursor", "lifecycle_state", "include_deleted")
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
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
		})

	case http.MethodPost:
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
			if scopedRec.WorkerID != "" {
				writeError(w, http.StatusForbidden, errors.New("scoped token cannot manage workers"))
				return
			}
		}

		if _, err := parseAndValidateQuery(r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		var req createWorkerRequestBody
		if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
			workerHTTPError(w, err)
			return
		}

		idempKey := strings.TrimSpace(req.IdempotencyKey)
		if idempKey == "" {
			writeError(w, http.StatusBadRequest, errors.New("idempotency_key is required"))
			return
		}
		if len(idempKey) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("idempotency_key exceeds maximum length of 256 characters"))
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, http.StatusBadRequest, errors.New("worker name is required"))
			return
		}
		if len(req.Name) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("worker name exceeds maximum length of 256 characters"))
			return
		}
		for _, auto := range req.Automations {
			if strings.TrimSpace(auto.ID) != "" {
				writeError(w, http.StatusBadRequest, errors.New("client-supplied automation id is not permitted on create"))
				return
			}
			if strings.TrimSpace(auto.WorkerID) != "" {
				writeError(w, http.StatusBadRequest, errors.New("client-supplied automation worker_id is not permitted on create"))
				return
			}
			if auto.Revision != 0 {
				writeError(w, http.StatusBadRequest, errors.New("client-supplied automation revision is not permitted on create"))
				return
			}
			if auto.CreatedAt != 0 || auto.UpdatedAt != 0 {
				writeError(w, http.StatusBadRequest, errors.New("client-supplied automation timestamps are not permitted on create"))
				return
			}
		}

		created, err := s.sessions.CreateWorker(r.Context(), p.AccountScopeID, p.UserID, pebblestore.CreateWorkerRequest{
			Name:                  req.Name,
			Description:           req.Description,
			Instructions:          req.Instructions,
			RequestedCapabilities: req.RequestedCapabilities,
			WorkspaceRequirements: req.WorkspaceRequirements,
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
	ExpectedRevision      uint64                                   `json:"expected_revision"`
	Name                  *string                                  `json:"name,omitempty"`
	Description           *string                                  `json:"description,omitempty"`
	Instructions          *string                                  `json:"instructions,omitempty"`
	RequestedCapabilities []pebblestore.WorkerCapabilityRequest    `json:"requested_capabilities,omitempty"`
	WorkspaceRequirements []pebblestore.WorkerWorkspaceRequirement `json:"workspace_requirements,omitempty"`
	Automations           []pebblestore.WorkerAutomationDefinition `json:"automations,omitempty"`
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
		if _, err := parseAndValidateQuery(r); err != nil {
			writeError(w, http.StatusBadRequest, err)
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
		if _, err := parseAndValidateQuery(r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		var req updateWorkerRequestBody
		if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
			workerHTTPError(w, err)
			return
		}

		if req.ExpectedRevision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision in body is required"))
			return
		}

		updated, err := s.sessions.UpdateWorker(p.AccountScopeID, p.UserID, workerID, req.ExpectedRevision, pebblestore.UpdateWorkerRequest{
			Name:                  req.Name,
			Description:           req.Description,
			Instructions:          req.Instructions,
			RequestedCapabilities: req.RequestedCapabilities,
			WorkspaceRequirements: req.WorkspaceRequirements,
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
		s.handleWorkerDelete(w, r, p, workerID)

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
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	bodyBytes, err := readRawJSONStrict(w, r, 512*1024)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	def, err := s.sessions.ValidatePortableWorkerDefinition(bodyBytes)
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
			writeError(w, http.StatusForbidden, errors.New("scoped token cannot manage workers"))
			return
		}
	}

	q, err := parseAndValidateQuery(r, "mode", "idempotency_key", "worker_id", "expected_revision")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mode := strings.TrimSpace(q.Get("mode"))
	if mode == "" {
		mode = "new"
	}
	if mode != "new" && mode != "update" {
		writeError(w, http.StatusBadRequest, errors.New("mode must be either 'new' or 'update'"))
		return
	}

	if mode == "new" {
		if q.Has("worker_id") || q.Has("expected_revision") {
			writeError(w, http.StatusBadRequest, errors.New("worker_id and expected_revision are not permitted when mode is 'new'"))
			return
		}
		idempKey := strings.TrimSpace(q.Get("idempotency_key"))
		if idempKey == "" {
			writeError(w, http.StatusBadRequest, errors.New("idempotency_key query parameter is required when mode is 'new'"))
			return
		}
		if len(idempKey) > 256 {
			writeError(w, http.StatusBadRequest, errors.New("idempotency_key exceeds maximum length of 256 characters"))
			return
		}

		bodyBytes, err := readRawJSONStrict(w, r, 512*1024)
		if err != nil {
			workerHTTPError(w, err)
			return
		}

		rec, err := s.sessions.ImportWorkerAsNew(p.AccountScopeID, p.UserID, bodyBytes, idempKey)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"worker": rec,
		})
		return
	}

	// mode == "update"
	if q.Has("idempotency_key") {
		writeError(w, http.StatusBadRequest, errors.New("idempotency_key is not permitted when mode is 'update'"))
		return
	}
	workerID := strings.TrimSpace(q.Get("worker_id"))
	if workerID == "" {
		writeError(w, http.StatusBadRequest, errors.New("worker_id is required when mode is 'update'"))
		return
	}
	if len(workerID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("worker_id exceeds maximum length of 256 characters"))
		return
	}
	revStr := strings.TrimSpace(q.Get("expected_revision"))
	if revStr == "" {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision is required when mode is 'update'"))
		return
	}
	expectedRevision, err := strconv.ParseUint(revStr, 10, 64)
	if err != nil || expectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid expected_revision"))
		return
	}

	bodyBytes, err := readRawJSONStrict(w, r, 512*1024)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	rec, err := s.sessions.ImportWorkerUpdate(p.AccountScopeID, p.UserID, workerID, expectedRevision, bodyBytes)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
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
			writeError(w, http.StatusForbidden, errors.New("scoped token cannot manage workers"))
			return
		}
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if r.Body != nil && r.ContentLength > 0 {
		var empty struct{}
		if err := decodeJSONStrict(w, r, 64*1024, &empty); err != nil && err != io.EOF {
			workerHTTPError(w, err)
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
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	def, _, err := s.sessions.ExportWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": def,
	})
}

type attachWorkerAutomationRequestBody struct {
	ExpectedWorkerRevision uint64                                 `json:"expected_worker_revision"`
	Automation             pebblestore.WorkerAutomationDefinition `json:"automation"`
}

func (s *Server) handleWorkerAutomations(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string, subparts []string) {
	if len(subparts) == 0 {
		// POST /v3/workers/{id}/automations
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !s.requireScope(w, r, "automations:write") {
			return
		}
		if _, err := parseAndValidateQuery(r); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		var req attachWorkerAutomationRequestBody
		if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
			workerHTTPError(w, err)
			return
		}
		if req.ExpectedWorkerRevision == 0 {
			writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision in body is required"))
			return
		}
		if strings.TrimSpace(req.Automation.Name) == "" {
			writeError(w, http.StatusBadRequest, errors.New("automation name is required"))
			return
		}
		if strings.TrimSpace(req.Automation.ID) != "" {
			writeError(w, http.StatusBadRequest, errors.New("client-supplied automation id is not permitted on attach"))
			return
		}
		if strings.TrimSpace(req.Automation.WorkerID) != "" {
			writeError(w, http.StatusBadRequest, errors.New("client-supplied automation worker_id is not permitted on attach"))
			return
		}
		if req.Automation.Revision != 0 {
			writeError(w, http.StatusBadRequest, errors.New("client-supplied automation revision is not permitted on attach"))
			return
		}
		if req.Automation.CreatedAt != 0 || req.Automation.UpdatedAt != 0 {
			writeError(w, http.StatusBadRequest, errors.New("client-supplied automation timestamps are not permitted on attach"))
			return
		}

		updated, err := s.sessions.AttachWorkerAutomation(p.AccountScopeID, p.UserID, workerID, req.ExpectedWorkerRevision, req.Automation)
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
			if _, err := parseAndValidateQuery(r); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}

			var req attachWorkerAutomationRequestBody
			if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
				workerHTTPError(w, err)
				return
			}
			if req.ExpectedWorkerRevision == 0 {
				writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision in body is required"))
				return
			}
			if strings.TrimSpace(req.Automation.Name) == "" {
				writeError(w, http.StatusBadRequest, errors.New("automation name is required"))
				return
			}
			if strings.TrimSpace(req.Automation.ID) != "" && strings.TrimSpace(req.Automation.ID) != automationID {
				writeError(w, http.StatusBadRequest, errors.New("automation id in body does not match path"))
				return
			}
			if strings.TrimSpace(req.Automation.WorkerID) != "" && strings.TrimSpace(req.Automation.WorkerID) != workerID {
				writeError(w, http.StatusBadRequest, errors.New("automation worker_id in body does not match path"))
				return
			}
			req.Automation.ID = automationID
			req.Automation.WorkerID = workerID

			updated, err := s.sessions.UpdateWorkerAutomation(p.AccountScopeID, p.UserID, workerID, automationID, req.ExpectedWorkerRevision, req.Automation)
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
			q, err := parseAndValidateQuery(r, "expected_worker_revision")
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			if !q.Has("expected_worker_revision") {
				writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision is required"))
				return
			}
			expectedRevision, err := strconv.ParseUint(q.Get("expected_worker_revision"), 10, 64)
			if err != nil || expectedRevision == 0 {
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

	if len(subparts) == 2 {
		automationID := strings.TrimSpace(subparts[0])
		action := strings.TrimSpace(subparts[1])
		if automationID == "" {
			writeError(w, http.StatusBadRequest, errors.New("automation id is required"))
			return
		}
		switch action {
		case "enable":
			s.handleWorkerAutomationEnable(w, r, p, workerID, automationID)
		case "disable":
			s.handleWorkerAutomationDisable(w, r, p, workerID, automationID)
		case "trigger":
			s.handleWorkerTrigger(w, r, p, workerID, automationID)
		default:
			writeError(w, http.StatusNotFound, errors.New("not found"))
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
	q, err := parseAndValidateQuery(r, "limit", "cursor")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

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
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
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
	q, err := parseAndValidateQuery(r, "limit", "cursor")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

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
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
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

var workerLifecycleMu sync.Mutex

type activateWorkerRequestBody struct {
	ExpectedRevision uint64            `json:"expected_revision"`
	LocalBindings    map[string]string `json:"local_bindings"`
}

type workerLifecycleRequestBody struct {
	ExpectedRevision uint64 `json:"expected_revision"`
}

type setAutomationEnabledRequestBody struct {
	ExpectedWorkerRevision uint64 `json:"expected_worker_revision"`
}

type directWorkerRequestBody struct {
	Prompt         string         `json:"prompt,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type testWorkerRunRequestBody struct {
	AutomationID   string         `json:"automation_id,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Input          map[string]any `json:"input,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type triggerWorkerRequestBody struct {
	AutomationID   string         `json:"automation_id,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
}

type mintWorkerTokenRequestBody struct {
	Name          string `json:"name,omitempty"`
	SaveToSecrets *bool  `json:"save_to_secrets,omitempty"`
}

func (s *Server) persistWorkerAndHistory(account, user string, worker pebblestore.WorkerRecord, changeSummary string) error {
	if s.sessions == nil || s.sessions.Store() == nil || s.sessions.Store().Underlying() == nil {
		return errors.New("store unavailable")
	}
	store := s.sessions.Store().Underlying()

	worker.UpdatedAt = time.Now().UnixMilli()
	if worker.CreatedAt == 0 {
		worker.CreatedAt = worker.UpdatedAt
	}

	if err := store.PutJSON(pebblestore.KeyWorker(account, worker.ID), worker); err != nil {
		return fmt.Errorf("persist worker: %w", err)
	}

	hist := pebblestore.WorkerRevisionRecord{
		WorkerID:       worker.ID,
		AccountScopeID: account,
		Revision:       worker.Revision,
		Worker:         worker,
		CommittedAt:    worker.UpdatedAt,
		CommittedBy:    user,
		ChangeSummary:  changeSummary,
	}
	if err := store.PutJSON(pebblestore.KeyWorkerHistory(account, worker.ID, worker.Revision), hist); err != nil {
		return fmt.Errorf("persist worker history: %w", err)
	}

	for _, a := range worker.Automations {
		if a.ID != "" {
			_ = store.PutJSON(pebblestore.KeyWorkerByAutomation(account, a.ID), worker.ID)
		}
	}

	if seq, err := s.sessions.CurrentRealtimeOutboxRevision(); err == nil && seq > 0 {
		_ = s.publishCommittedV3RealtimeOutbox(sessionruntime.RealtimeOutboxRecord{
			EndpointSeq:    seq,
			EndpointCursor: pebblestore.V3RealtimeOutboxCursor(seq),
			AccountScopeID: account,
			UserID:         user,
			Event: pebblestore.V3SessionEvent{
				Seq:       seq,
				EventType: pebblestore.WorkerUpdatedEventType,
				TsUnixMs:  worker.UpdatedAt,
			},
			CreatedAt: worker.UpdatedAt,
		})
	}
	return nil
}

func (s *Server) handleWorkerActivate(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req activateWorkerRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision in body is required"))
		return
	}
	if req.LocalBindings == nil {
		writeError(w, http.StatusBadRequest, errors.New("local_bindings in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusBadRequest, errors.New("cannot activate archived worker; resume or recreate first"))
		return
	}

	for _, reqRole := range worker.WorkspaceRequirements {
		if reqRole.Required {
			val, ok := req.LocalBindings[reqRole.Role]
			if !ok || strings.TrimSpace(val) == "" {
				writeError(w, http.StatusBadRequest, fmt.Errorf("missing required workspace role binding: %q", reqRole.Role))
				return
			}
		}
	}
	for role, bid := range req.LocalBindings {
		if strings.TrimSpace(bid) == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("binding for role %q cannot be blank", role))
			return
		}
	}

	worker.LocalBindings = req.LocalBindings
	worker.LifecycleState = pebblestore.WorkerLifecycleStateActive
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, "activated with approved local bindings"); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerPause(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req workerLifecycleRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusBadRequest, errors.New("cannot pause archived worker"))
		return
	}

	now := time.Now().UnixMilli()
	runs, _, _ := s.sessions.ListWorkerRuns(p.AccountScopeID, workerID, 100, "")
	for _, run := range runs {
		if run.Status == "admitted" {
			run.Status = "cancelled"
			run.Error = "worker paused"
			run.CompletedAt = now
			_, _ = s.sessions.RecordWorkerRun(p.AccountScopeID, run)
		}
	}

	worker.LifecycleState = pebblestore.WorkerLifecycleStatePaused
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, "paused worker"); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerResume(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req workerLifecycleRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusBadRequest, errors.New("cannot resume archived worker"))
		return
	}
	if worker.LifecycleState != pebblestore.WorkerLifecycleStatePaused && worker.LifecycleState != pebblestore.WorkerLifecycleStateIdle {
		writeError(w, http.StatusBadRequest, fmt.Errorf("worker is already %s", worker.LifecycleState))
		return
	}

	worker.LifecycleState = pebblestore.WorkerLifecycleStateActive
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, "resumed worker"); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerArchive(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req workerLifecycleRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}

	runs, _, _ := s.sessions.ListWorkerRuns(p.AccountScopeID, workerID, 100, "")
	for _, run := range runs {
		if run.Status == "running" {
			writeError(w, http.StatusBadRequest, errors.New("cannot archive worker with active running executions; stop runs first"))
			return
		}
	}
	now := time.Now().UnixMilli()
	for _, run := range runs {
		if run.Status == "admitted" {
			run.Status = "cancelled"
			run.Error = "worker archived"
			run.CompletedAt = now
			_, _ = s.sessions.RecordWorkerRun(p.AccountScopeID, run)
		}
	}

	for i := range worker.Automations {
		worker.Automations[i].Enabled = false
		worker.Automations[i].Revision++
		worker.Automations[i].UpdatedAt = now
	}

	worker.LifecycleState = pebblestore.WorkerLifecycleStateArchived
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, "archived worker"); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerDelete(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	var expectedRevision uint64
	q, err := parseAndValidateQuery(r, "expected_revision")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if q.Has("expected_revision") {
		expectedRevision, err = strconv.ParseUint(q.Get("expected_revision"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("expected_revision is required"))
			return
		}
	}
	if expectedRevision == 0 && r.Body != nil && r.ContentLength > 0 {
		var req workerLifecycleRequestBody
		if err := decodeJSONStrict(w, r, 64*1024, &req); err == nil {
			expectedRevision = req.ExpectedRevision
		}
	}
	if expectedRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_revision is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != expectedRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}

	runs, _, _ := s.sessions.ListWorkerRuns(p.AccountScopeID, workerID, 100, "")
	for _, run := range runs {
		if run.Status == "running" || run.Status == "admitted" {
			writeError(w, http.StatusBadRequest, errors.New("cannot delete worker with active runs"))
			return
		}
	}

	worker.LifecycleState = pebblestore.WorkerLifecycleStateDeleted
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, "deleted worker"); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"deleted": true,
	})
}

func (s *Server) handleWorkerAutomationEnable(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, automationID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req setAutomationEnabledRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedWorkerRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedWorkerRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}

	foundIdx := -1
	for i, a := range worker.Automations {
		if a.ID == automationID {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		writeError(w, http.StatusNotFound, errors.New("automation not found on worker"))
		return
	}

	now := time.Now().UnixMilli()
	worker.Automations[foundIdx].Enabled = true
	worker.Automations[foundIdx].Revision++
	worker.Automations[foundIdx].UpdatedAt = now
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, fmt.Sprintf("enabled automation %s", automationID)); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerAutomationDisable(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, automationID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req setAutomationEnabledRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	if req.ExpectedWorkerRevision == 0 {
		writeError(w, http.StatusBadRequest, errors.New("expected_worker_revision in body is required"))
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.Revision != req.ExpectedWorkerRevision {
		writeError(w, http.StatusConflict, pebblestore.ErrWorkerConflict)
		return
	}

	foundIdx := -1
	for i, a := range worker.Automations {
		if a.ID == automationID {
			foundIdx = i
			break
		}
	}
	if foundIdx == -1 {
		writeError(w, http.StatusNotFound, errors.New("automation not found on worker"))
		return
	}

	now := time.Now().UnixMilli()
	runs, _, _ := s.sessions.ListWorkerRuns(p.AccountScopeID, workerID, 100, "")
	for _, run := range runs {
		if run.AutomationID == automationID && run.Status == "admitted" {
			run.Status = "cancelled"
			run.Error = "automation disabled"
			run.CompletedAt = now
			_, _ = s.sessions.RecordWorkerRun(p.AccountScopeID, run)
		}
	}

	worker.Automations[foundIdx].Enabled = false
	worker.Automations[foundIdx].Revision++
	worker.Automations[foundIdx].UpdatedAt = now
	worker.Revision++

	if err := s.persistWorkerAndHistory(p.AccountScopeID, p.UserID, worker, fmt.Sprintf("disabled automation %s", automationID)); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"worker": worker,
	})
}

func (s *Server) handleWorkerDirectRequest(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req directWorkerRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" && len(req.Input) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("prompt or input is required for direct request"))
		return
	}
	idempKey := strings.TrimSpace(req.IdempotencyKey)
	if idempKey == "" {
		idempKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStatePaused || worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot admit direct request: worker is %s", worker.LifecycleState))
		return
	}

	if idempKey != "" {
		var existingRun pebblestore.WorkerRunRecord
		ok, err := s.sessions.Store().Underlying().GetJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), &existingRun)
		if err == nil && ok && existingRun.ID != "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": existingRun, "deduplicated": true})
			return
		}
	}

	now := time.Now().UnixMilli()
	inputMap := make(map[string]any)
	for k, v := range req.Input {
		inputMap[k] = v
	}
	if prompt != "" {
		inputMap["prompt"] = prompt
	}

	runID := pebblestore.GenerateWorkerRunID()
	run := pebblestore.WorkerRunRecord{
		ID:             runID,
		AccountScopeID: p.AccountScopeID,
		WorkerID:       workerID,
		WorkerRevision: worker.Revision,
		RequestSource:  "direct",
		Input:          inputMap,
		Status:         "admitted",
		CreatedAt:      now,
	}

	recorded, err := s.sessions.RecordWorkerRun(p.AccountScopeID, run)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	if idempKey != "" {
		_ = s.sessions.Store().Underlying().PutJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), recorded)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":  true,
		"run": recorded,
	})
}

func (s *Server) handleWorkerTestRun(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req testWorkerRunRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}
	idempKey := strings.TrimSpace(req.IdempotencyKey)
	if idempKey == "" {
		idempKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusBadRequest, errors.New("cannot run test on archived worker"))
		return
	}

	autoID := strings.TrimSpace(req.AutomationID)
	var autoRev uint64
	if autoID != "" {
		foundAuto := false
		for _, a := range worker.Automations {
			if a.ID == autoID {
				foundAuto = true
				autoRev = a.Revision
				break
			}
		}
		if !foundAuto {
			writeError(w, http.StatusNotFound, errors.New("automation not found on worker"))
			return
		}
	}

	if idempKey != "" {
		var existingRun pebblestore.WorkerRunRecord
		ok, err := s.sessions.Store().Underlying().GetJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), &existingRun)
		if err == nil && ok && existingRun.ID != "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": existingRun, "deduplicated": true})
			return
		}
	}

	now := time.Now().UnixMilli()
	inputMap := make(map[string]any)
	for k, v := range req.Input {
		inputMap[k] = v
	}
	inputMap["test_run"] = true
	prompt := strings.TrimSpace(req.Prompt)
	if prompt != "" {
		inputMap["prompt"] = prompt
	}

	runID := pebblestore.GenerateWorkerRunID()
	run := pebblestore.WorkerRunRecord{
		ID:                 runID,
		AccountScopeID:     p.AccountScopeID,
		WorkerID:           workerID,
		WorkerRevision:     worker.Revision,
		AutomationID:       autoID,
		AutomationRevision: autoRev,
		RequestSource:      "test_run",
		Input:              inputMap,
		Status:             "admitted",
		CreatedAt:          now,
	}

	recorded, err := s.sessions.RecordWorkerRun(p.AccountScopeID, run)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	if idempKey != "" {
		_ = s.sessions.Store().Underlying().PutJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), recorded)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":  true,
		"run": recorded,
	})
}

func (s *Server) handleWorkerTrigger(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, pathAutoID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScopeAny(w, r, "workers:trigger", "automations:trigger", "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if scopedRec, ok := ScopedTokenFromRequest(r); ok && scopedRec != nil {
		if scopedRec.WorkerID != "" && scopedRec.WorkerID != workerID {
			writeError(w, http.StatusForbidden, fmt.Errorf("scoped token is restricted to worker %q", scopedRec.WorkerID))
			return
		}
	}

	var req triggerWorkerRequestBody
	if err := decodeJSONStrict(w, r, 300*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}

	autoID := pathAutoID
	if autoID == "" {
		autoID = strings.TrimSpace(req.AutomationID)
	} else if req.AutomationID != "" && req.AutomationID != autoID {
		writeError(w, http.StatusBadRequest, errors.New("automation_id in body does not match path"))
		return
	}

	idempKey := strings.TrimSpace(req.IdempotencyKey)
	if idempKey == "" {
		idempKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	}

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}
	if worker.LifecycleState == pebblestore.WorkerLifecycleStatePaused || worker.LifecycleState == pebblestore.WorkerLifecycleStateArchived {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot trigger worker: worker is %s", worker.LifecycleState))
		return
	}

	var autoRev uint64
	if autoID != "" {
		foundAuto := false
		for _, a := range worker.Automations {
			if a.ID == autoID {
				foundAuto = true
				if !a.Enabled {
					writeError(w, http.StatusConflict, fmt.Errorf("automation %q is disabled", autoID))
					return
				}
				autoRev = a.Revision
				break
			}
		}
		if !foundAuto {
			writeError(w, http.StatusNotFound, errors.New("automation not found on worker"))
			return
		}
	}

	if idempKey != "" {
		var existingRun pebblestore.WorkerRunRecord
		ok, err := s.sessions.Store().Underlying().GetJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), &existingRun)
		if err == nil && ok && existingRun.ID != "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": existingRun, "deduplicated": true})
			return
		}
	}

	now := time.Now().UnixMilli()
	payload := req.Payload
	if payload == nil {
		payload = make(map[string]any)
	}

	runID := pebblestore.GenerateWorkerRunID()
	run := pebblestore.WorkerRunRecord{
		ID:                 runID,
		AccountScopeID:     p.AccountScopeID,
		WorkerID:           workerID,
		WorkerRevision:     worker.Revision,
		AutomationID:       autoID,
		AutomationRevision: autoRev,
		RequestSource:      "trigger",
		Input:              payload,
		Status:             "admitted",
		CreatedAt:          now,
	}

	recorded, err := s.sessions.RecordWorkerRun(p.AccountScopeID, run)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	if idempKey != "" {
		_ = s.sessions.Store().Underlying().PutJSON(pebblestore.KeyWorkerIdempotency(p.AccountScopeID, "run:"+idempKey), recorded)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":  true,
		"run": recorded,
	})
}

func (s *Server) handleWorkerToken(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var req mintWorkerTokenRequestBody
	if err := decodeJSONStrict(w, r, 512*1024, &req); err != nil {
		workerHTTPError(w, err)
		return
	}

	worker, found, err := s.sessions.GetWorker(p.AccountScopeID, workerID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found || worker.LifecycleState == pebblestore.WorkerLifecycleStateDeleted {
		writeError(w, http.StatusNotFound, pebblestore.ErrWorkerNotFound)
		return
	}

	tokenName := strings.TrimSpace(req.Name)
	if tokenName == "" {
		tokenName = "Trigger Worker: " + worker.Name
		if strings.TrimSpace(worker.Name) == "" {
			tokenName = "Trigger Worker: " + workerID
		}
	}

	if s.security != nil {
		rawToken, tokenRecord, err := s.security.CreateScopedToken(
			tokenName,
			[]string{"workers:trigger", "automations:trigger"},
			p.AccountScopeID,
			p.UserID,
			0,
			workerID,
			tokenName,
		)
		if err != nil {
			workerHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"token":     rawToken,
			"record":    tokenRecord,
			"worker_id": workerID,
			"scopes":    []string{"workers:trigger", "automations:trigger"},
		})
		return
	}

	authStore := pebblestore.NewClientAuthStore(s.sessions.Store().Underlying())
	var b [32]byte
	_, _ = rand.Read(b[:])
	rawToken := "swk_" + hex.EncodeToString(b[:])
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])
	tokenID := "tok_" + hex.EncodeToString(b[:8])
	now := time.Now().UnixMilli()
	tokenRecord := pebblestore.ScopedTokenRecord{
		ID:             tokenID,
		Name:           tokenName,
		TokenHash:      tokenHash,
		AccountScopeID: p.AccountScopeID,
		UserID:         p.UserID,
		Scopes:         []string{"workers:trigger", "automations:trigger"},
		WorkerID:       workerID,
		CreatedAt:      now,
	}
	if err := authStore.PutScopedToken(tokenRecord); err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"token":     rawToken,
		"record":    tokenRecord,
		"worker_id": workerID,
		"scopes":    []string{"workers:trigger", "automations:trigger"},
	})
}

func (s *Server) handleWorkerRunCancel(w http.ResponseWriter, r *http.Request, p identity.Principal, workerID, runID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.requireScope(w, r, "automations:write") {
		return
	}
	if _, err := parseAndValidateQuery(r); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	workerLifecycleMu.Lock()
	defer workerLifecycleMu.Unlock()

	run, found, err := s.sessions.GetWorkerRun(p.AccountScopeID, workerID, runID)
	if err != nil {
		workerHTTPError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("worker run not found"))
		return
	}
	if run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled" {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot cancel run in terminal status %q", run.Status))
		return
	}

	now := time.Now().UnixMilli()
	run.Status = "cancelled"
	run.Error = "run cancelled"
	run.CompletedAt = now

	recorded, err := s.sessions.RecordWorkerRun(p.AccountScopeID, run)
	if err != nil {
		workerHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":  true,
		"run": recorded,
	})
}

