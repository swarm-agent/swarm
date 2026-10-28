package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// WorkerRunAdmission is validated against the current worker under workersMu.
// SessionID is allocated by the store with the run identity and never supplied
// by callers. Dispatch must use the returned receipt only.
type WorkerRunAdmission struct {
	// CapturedReviewTarget is trusted daemon evidence, excluded from request identity.
	CapturedReviewTarget   map[string]any
	Placement              *WorkerPlacementAdmission
	ExpectedWorkerRevision uint64
	ModelOverride          *SessionModelProfileSnapshot
	ModelSource            string
	ResolvedModelProfile   *SessionModelProfileSnapshot
	WorkflowTriggerID      string
	WorkerID               string
	AutomationID           string
	UserID                 string
	RequestSource          string
	Input                  map[string]any
	IdempotencyKey         string
	OccurrenceID           string
	SessionID              string
	// WorkspaceID is the workspace the caller named for this job (part of the
	// idempotency payload). ResolvedWorkspaceID is the workspace the execution
	// service authorized for the job; admission recomputes the per-job rule
	// under the worker lock and refuses a mismatch, then pins it on the receipt.
	WorkspaceID         string
	ResolvedWorkspaceID string
	// ResolvedProjectID is supplied only by the execution authority, not run input.
	ResolvedProjectID string
}

type workerRunIdempotency struct {
	PayloadHash string `json:"payload_hash"`
	WorkerID    string `json:"worker_id"`
	RunID       string `json:"run_id"`
}

// AdmitWorkerRun commits revision-pinned input, occurrence/idempotency indexes
// and an account-scoped realtime event in one durable batch. A replay returns
// the original receipt even if the worker is subsequently paused or edited.
func (ws *WorkerStore) AdmitWorkerRun(account string, req WorkerRunAdmission) (WorkerRunRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRunRecord{}, errors.New("store is not open")
	}
	account, req.WorkerID, req.AutomationID = strings.TrimSpace(account), strings.TrimSpace(req.WorkerID), strings.TrimSpace(req.AutomationID)
	if account == "" || !workerIDRegexp.MatchString(req.WorkerID) || strings.TrimSpace(req.UserID) == "" {
		return WorkerRunRecord{}, fmt.Errorf("%w: account, worker and user required", ErrWorkerConflict)
	}
	if req.SessionID != "" {
		return WorkerRunRecord{}, fmt.Errorf("%w: session identity is allocated by the store", ErrWorkerConflict)
	}
	if req.IdempotencyKey == "" && req.OccurrenceID == "" {
		return WorkerRunRecord{}, fmt.Errorf("%w: idempotency key or occurrence required", ErrWorkerConflict)
	}
	if req.IdempotencyKey != "" && (len(req.IdempotencyKey) > 256 || strings.TrimSpace(req.IdempotencyKey) == "" || strings.ContainsAny(req.IdempotencyKey, "/\\\r\n") || req.IdempotencyKey != strings.TrimSpace(req.IdempotencyKey)) {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	if req.OccurrenceID != "" && !workerIDRegexp.MatchString(req.OccurrenceID) {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	if req.OccurrenceID != "" && req.OccurrenceID == req.IdempotencyKey {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	if req.OccurrenceID != "" && req.IdempotencyKey != "" {
		return WorkerRunRecord{}, fmt.Errorf("%w: use exactly one occurrence or idempotency key", ErrWorkerConflict)
	}
	switch {
	case req.RequestSource == "direct" || req.RequestSource == "orchestrator" || req.RequestSource == "test_run" || req.RequestSource == "automation" || req.RequestSource == "workflow_trigger" || IsWorkflowItemSource(req.RequestSource):
		if req.OccurrenceID != "" {
			return WorkerRunRecord{}, ErrWorkerConflict
		}
		if prompt, ok := req.Input["prompt"].(string); req.AutomationID == "" && (!ok || strings.TrimSpace(prompt) == "") {
			return WorkerRunRecord{}, fmt.Errorf("%w: a non-empty direct request prompt is required", ErrWorkerConflict)
		}
	case req.RequestSource == "trigger":
		if req.AutomationID == "" || req.IdempotencyKey == "" || req.OccurrenceID != "" {
			return WorkerRunRecord{}, ErrWorkerConflict
		}
	case req.RequestSource == "schedule":
		if req.AutomationID == "" || req.OccurrenceID == "" {
			return WorkerRunRecord{}, ErrWorkerConflict
		}
	default:
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	if (req.RequestSource == "workflow_trigger") != (req.WorkflowTriggerID != "") {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	inputBytes, err := json.Marshal(req.Input)
	if err != nil || len(inputBytes) > 64*1024 {
		return WorkerRunRecord{}, fmt.Errorf("%w: invalid or oversized input: %v", ErrWorkerConflict, err)
	}
	if _, exists := req.Input["captured_review_target"]; exists {
		return WorkerRunRecord{}, fmt.Errorf("%w: review target is daemon-owned", ErrWorkerConflict)
	}
	var acceptedInput map[string]any
	if err := json.Unmarshal(inputBytes, &acceptedInput); err != nil {
		return WorkerRunRecord{}, err
	}
	if req.CapturedReviewTarget != nil {
		acceptedInput["captured_review_target"] = req.CapturedReviewTarget
	}
	hash, err := hashWorkerPayload(struct {
		Worker, Automation, User, Source, Occurrence string
		WorkflowTrigger                              string `json:",omitempty"`
		Input                                        json.RawMessage
		ModelOverride                                *SessionModelProfileSnapshot `json:",omitempty"`
	}{req.WorkerID, req.AutomationID, req.UserID, req.RequestSource, req.OccurrenceID, req.WorkflowTriggerID, inputBytes, req.ModelOverride})
	if err != nil {
		return WorkerRunRecord{}, err
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	if req.WorkspaceID != "" {
		if req.Placement != nil || len(req.WorkspaceID) > 128 {
			return WorkerRunRecord{}, fmt.Errorf("%w: invalid job workspace", ErrWorkerConflict)
		}
		// Only a named workspace extends the payload, so receipts admitted
		// before per-job workspaces keep replaying under their original hash.
		hash, err = hashWorkerPayload(struct {
			Base      string
			Workspace string
		}{hash, req.WorkspaceID})
		if err != nil {
			return WorkerRunRecord{}, err
		}
	}
	if req.Placement != nil {
		hash, err = hashWorkerPayload(struct {
			Base      string
			Placement *WorkerPlacementAdmission
			Revision  uint64
		}{hash, req.Placement, req.ExpectedWorkerRevision})
		if err != nil {
			return WorkerRunRecord{}, err
		}
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	if req.IdempotencyKey != "" {
		var prior workerRunIdempotency
		found, err := ws.store.GetJSON(KeyWorkerRunIdempotency(account, req.IdempotencyKey), &prior)
		if err != nil {
			return WorkerRunRecord{}, err
		}
		if found {
			if prior.PayloadHash != hash || prior.WorkerID != req.WorkerID {
				return WorkerRunRecord{}, ErrWorkerConflict
			}
			receipt, exists, err := ws.GetWorkerRun(account, prior.WorkerID, prior.RunID)
			if err != nil {
				return WorkerRunRecord{}, err
			}
			if !exists || receipt.UserID != req.UserID || (req.SessionID != "" && receipt.SessionID != req.SessionID) {
				return WorkerRunRecord{}, ErrWorkerConflict
			}
			return receipt, nil
		}
	}
	if req.OccurrenceID != "" {
		var link string
		found, err := ws.store.GetJSON(KeyWorkerRunByOccurrence(account, req.OccurrenceID), &link)
		if err != nil {
			return WorkerRunRecord{}, err
		}
		if found {
			var prior workerRunIdempotency
			ok, err := ws.store.GetJSON(KeyWorkerRunIdempotency(account, req.OccurrenceID), &prior)
			if err != nil {
				return WorkerRunRecord{}, err
			}
			if !ok || prior.PayloadHash != hash || prior.WorkerID != req.WorkerID || link != prior.WorkerID+":"+prior.RunID {
				return WorkerRunRecord{}, ErrWorkerConflict
			}
			receipt, ok, err := ws.GetWorkerRun(account, prior.WorkerID, prior.RunID)
			if err != nil {
				return WorkerRunRecord{}, err
			}
			if !ok || receipt.UserID != req.UserID || (req.SessionID != "" && receipt.SessionID != req.SessionID) {
				return WorkerRunRecord{}, ErrWorkerConflict
			}
			return receipt, nil
		}
	}
	var worker WorkerRecord
	found, err := ws.store.GetJSON(KeyWorker(account, req.WorkerID), &worker)
	if err != nil {
		return WorkerRunRecord{}, err
	}
	if !found || worker.AccountScopeID != account {
		return WorkerRunRecord{}, ErrWorkerNotFound
	}
	if worker.LifecycleState != WorkerLifecycleStateActive && !(req.RequestSource == "test_run" && worker.LifecycleState == WorkerLifecycleStateIdle) {
		return WorkerRunRecord{}, fmt.Errorf("%w: worker admission closed (%s)", ErrWorkerConflict, worker.LifecycleState)
	}
	if worker.Provenance != nil && (worker.Provenance.MigratedAt != 0 || worker.Provenance.SourceProposalID != "") {
		return WorkerRunRecord{}, fmt.Errorf("%w: legacy migration is read-only until cutover", ErrWorkerConflict)
	}
	if len(worker.RequestedCapabilities) != 0 {
		return WorkerRunRecord{}, fmt.Errorf("%w: no unapproved capabilities are allowed", ErrWorkerConflict)
	}
	primary, err := WorkerPrimaryBinding(worker.WorkspaceRequirements, worker.LocalBindings)
	if err != nil {
		return WorkerRunRecord{}, fmt.Errorf("%w: %v", ErrWorkerConflict, err)
	}
	var workspaceID, workspaceSource string
	if req.Placement != nil {
		// Remote placement keeps its bound-worker contract.
		if primary == "" {
			return WorkerRunRecord{}, fmt.Errorf("%w: remote placement requires an approved primary workspace binding", ErrWorkerConflict)
		}
	} else {
		workspaceID, workspaceSource, err = ResolveWorkerRunWorkspace(worker, req.AutomationID, req.WorkspaceID)
		if err != nil {
			return WorkerRunRecord{}, fmt.Errorf("%w: %w", ErrWorkerConflict, err)
		}
		// A caller that resolved (and checked) the workspace first must see the
		// same one here, under the worker lock; one that did not lets admission
		// resolve it, as before per-job workspaces.
		if resolved := strings.TrimSpace(req.ResolvedWorkspaceID); resolved != "" && resolved != workspaceID {
			return WorkerRunRecord{}, fmt.Errorf("%w: job workspace changed before admission", ErrWorkerConflict)
		}
	}
	unlockBudget := ws.store.sessionMutations.lockSessions("account:" + account)
	defer unlockBudget()
	if err := NewSessionStore(ws.store).checkWorkerBudgetAdmissionLocked(account, req.WorkerID); err != nil {
		return WorkerRunRecord{}, err
	}
	limits, err := ws.store.GetLimits(account)
	if err != nil {
		return WorkerRunRecord{}, err
	}
	active, err := ws.unfinishedWorkerRuns(account, "", "")
	if err != nil {
		return WorkerRunRecord{}, err
	}
	if err := ws.store.observeLimit(account, "", "worker_job_concurrency", limits.WorkerJobConcurrency, len(active) >= limits.WorkerJobConcurrency); err != nil {
		return WorkerRunRecord{}, err
	}
	var automationRevision uint64
	if req.AutomationID != "" {
		for _, a := range worker.Automations {
			if a.ID == req.AutomationID {
				if !a.Enabled && req.RequestSource != "test_run" {
					return WorkerRunRecord{}, ErrWorkerConflict
				}
				if req.RequestSource == "schedule" && a.ActivationMode != "interval" && a.ActivationMode != "cron" {
					return WorkerRunRecord{}, ErrWorkerConflict
				}
				if req.RequestSource == "trigger" && a.ActivationMode != "external_trigger" {
					return WorkerRunRecord{}, ErrWorkerConflict
				}
				automationRevision = a.Revision
				break
			}
		}
		if automationRevision == 0 {
			return WorkerRunRecord{}, ErrWorkerNotFound
		}
	}
	model := CloneSessionModelProfileSnapshot(req.ResolvedModelProfile)
	if model != nil {
		if req.ExpectedWorkerRevision != worker.Revision || model.UseAccountDefault || model.ActionUseAccountDefault || model.PlanUseAccountDefault {
			return WorkerRunRecord{}, ErrWorkerConflict
		}
		if err := ValidateWorkerModelProfile(model); err != nil {
			return WorkerRunRecord{}, err
		}
	} else if worker.ModelProfile != nil && (worker.ModelProfile.UseAccountDefault || worker.ModelProfile.ActionUseAccountDefault || worker.ModelProfile.PlanUseAccountDefault) {
		return WorkerRunRecord{}, fmt.Errorf("%w: inherited worker models must resolve before admission", ErrWorkerConflict)
	}
	now := time.Now().UnixMilli()
	runID := GenerateWorkerRunID()
	req.SessionID = "worker-execution-" + runID
	receipt := WorkerRunRecord{WorkflowTriggerID: req.WorkflowTriggerID, ProjectID: req.ResolvedProjectID, ModelSource: req.ModelSource, ModelProfile: model, ID: runID, AccountScopeID: account, UserID: req.UserID, WorkerID: worker.ID, WorkerRevision: worker.Revision, AutomationID: req.AutomationID, AutomationRevision: automationRevision, OccurrenceID: req.OccurrenceID, WorkspaceID: workspaceID, WorkspaceSource: workspaceSource, Role: worker.Role, SessionID: req.SessionID, RequestSource: req.RequestSource, Input: acceptedInput, Status: "admitted", CreatedAt: now}
	m := &workerRealtimeMutation{accountScopeID: account, userID: req.UserID, workerID: worker.ID}
	if err := ws.pinWorkerPlacement(account, req, worker, &receipt, m); err != nil {
		return WorkerRunRecord{}, err
	}
	if err := m.put(KeyWorkerRun(account, worker.ID, receipt.ID), receipt); err != nil {
		return WorkerRunRecord{}, err
	}
	if req.OccurrenceID != "" {
		if err := m.put(KeyWorkerRunByOccurrence(account, req.OccurrenceID), worker.ID+":"+receipt.ID); err != nil {
			return WorkerRunRecord{}, err
		}
		if err := m.put(KeyWorkerRunIdempotency(account, req.OccurrenceID), workerRunIdempotency{hash, worker.ID, receipt.ID}); err != nil {
			return WorkerRunRecord{}, err
		}
	}
	if req.IdempotencyKey != "" {
		if err := m.put(KeyWorkerRunIdempotency(account, req.IdempotencyKey), workerRunIdempotency{hash, worker.ID, receipt.ID}); err != nil {
			return WorkerRunRecord{}, err
		}
	}
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: worker.ID, Revision: worker.Revision, LifecycleState: worker.LifecycleState, ChangeSummary: "run admitted"}); err != nil {
		return WorkerRunRecord{}, err
	}
	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRunRecord{}, err
	}
	published = m
	return receipt, nil
}

// ListWorkersForScheduling scans bounded worker rows across accounts for the
// local daemon. Cursor is the full storage key; no caller-provided account or
// user may select this privileged enumeration.
func (ws *WorkerStore) ListWorkersForScheduling(cursor string, limit int) (ListWorkersResult, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return ListWorkersResult{}, errors.New("store is not open")
	}
	prefix := WorkerAccountPrefix("")
	if cursor != "" && !strings.HasPrefix(cursor, prefix) {
		return ListWorkersResult{}, ErrWorkerConflict
	}
	if limit < 1 || limit > 25 {
		limit = 25
	}
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return ListWorkersResult{}, err
	}
	defer it.Close()
	rows := make([]WorkerRecord, 0, limit)
	ok := it.First()
	if cursor != "" {
		ok = it.SeekGE([]byte(cursor))
		if ok && string(it.Key()) == cursor {
			ok = it.Next()
		}
	}
	for ; ok; ok = it.Next() {
		if len(rows) == limit {
			return ListWorkersResult{Workers: rows, NextCursor: KeyWorker(rows[len(rows)-1].AccountScopeID, rows[len(rows)-1].ID)}, it.Error()
		}
		var w WorkerRecord
		if err = json.Unmarshal(it.Value(), &w); err != nil {
			return ListWorkersResult{}, err
		}
		if w.AccountScopeID == "" || KeyWorker(w.AccountScopeID, w.ID) != string(it.Key()) {
			return ListWorkersResult{}, ErrWorkerConflict
		}
		rows = append(rows, w)
	}
	return ListWorkersResult{Workers: rows}, it.Error()
}

// SetWorkerLifecycle closes or opens admission using the same mutex as admission.
// Stopping remains durable until the execution service observes terminal status.
func (ws *WorkerStore) SetWorkerLifecycle(account, user, id string, revision uint64, next WorkerLifecycleState, stopTarget ...WorkerLifecycleState) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	var w WorkerRecord
	found, err := ws.store.GetJSON(KeyWorker(account, id), &w)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !found || w.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "") {
		return WorkerRecord{}, ErrWorkerConflict
	}
	allowed := false
	switch next {
	case WorkerLifecycleStateActive:
		allowed = w.LifecycleState == WorkerLifecycleStateIdle || w.LifecycleState == WorkerLifecycleStatePaused
	case WorkerLifecycleStateStopping:
		allowed = w.LifecycleState == WorkerLifecycleStateActive || w.LifecycleState == WorkerLifecycleStateIdle || w.LifecycleState == WorkerLifecycleStatePaused || (w.LifecycleState == WorkerLifecycleStateArchived && len(stopTarget) == 1 && stopTarget[0] == WorkerLifecycleStateDeleted) || (w.LifecycleState == WorkerLifecycleStatePending && len(stopTarget) == 1 && (stopTarget[0] == WorkerLifecycleStateDeleted || stopTarget[0] == WorkerLifecycleStateArchived))
	case WorkerLifecycleStatePaused, WorkerLifecycleStateArchived, WorkerLifecycleStateDeleted:
		allowed = w.LifecycleState == WorkerLifecycleStateStopping
	}
	if !allowed {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if next == WorkerLifecycleStatePaused || next == WorkerLifecycleStateArchived || next == WorkerLifecycleStateDeleted {
		pending, err := ws.unfinishedWorkerRuns(account, id, "")
		if err != nil {
			return WorkerRecord{}, err
		}
		if len(pending) != 0 {
			return WorkerRecord{}, fmt.Errorf("%w: run cancellation not acknowledged", ErrWorkerConflict)
		}
	}
	if next == WorkerLifecycleStateStopping {
		w.StopTarget = WorkerLifecycleStatePaused
		if len(stopTarget) == 1 {
			w.StopTarget = stopTarget[0]
		}
		if w.StopTarget != WorkerLifecycleStatePaused && w.StopTarget != WorkerLifecycleStateArchived && w.StopTarget != WorkerLifecycleStateDeleted {
			return WorkerRecord{}, ErrWorkerConflict
		}
	} else {
		w.StopTarget = ""
	}
	now := time.Now().UnixMilli()
	w.PendingReview = nil // a stop/resume invalidates previously proposed changes
	w.LifecycleState = next
	w.Revision++
	w.UpdatedAt = now
	h := WorkerRevisionRecord{WorkerID: id, AccountScopeID: account, Revision: w.Revision, Worker: w, CommittedAt: now, CommittedBy: user, ChangeSummary: "worker lifecycle " + string(next)}
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if err := m.put(KeyWorker(account, id), w); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.put(KeyWorkerHistory(account, id, w.Revision), h); err != nil {
		return WorkerRecord{}, err
	}
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: id, Revision: w.Revision, LifecycleState: next, ChangeSummary: h.ChangeSummary}); err != nil {
		return WorkerRecord{}, err
	}
	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	published = m
	return w, nil
}

// DisableWorkerAutomation closes only this automation's admission and returns
// all still-unacknowledged receipts for the execution service to cancel.
func (ws *WorkerStore) DisableWorkerAutomation(account, user, id, autoID string, revision uint64) (WorkerRecord, []WorkerRunRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, nil, errors.New("store is not open")
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	var w WorkerRecord
	found, err := ws.store.GetJSON(KeyWorker(account, id), &w)
	if err != nil {
		return WorkerRecord{}, nil, err
	}
	if !found || w.AccountScopeID != account {
		return WorkerRecord{}, nil, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision || w.LifecycleState == WorkerLifecycleStateDeleted || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return WorkerRecord{}, nil, ErrWorkerConflict
	}
	index := -1
	for i := range w.Automations {
		if w.Automations[i].ID == autoID {
			index = i
			break
		}
	}
	if index < 0 {
		return WorkerRecord{}, nil, ErrWorkerNotFound
	}
	if !w.Automations[index].Enabled {
		pending, err := ws.unfinishedWorkerRuns(account, id, autoID)
		if err != nil {
			return WorkerRecord{}, nil, err
		}
		if len(pending) == 0 {
			return WorkerRecord{}, nil, ErrWorkerConflict
		}
	}
	w.PendingReview = nil // never resurrect a disabled job through stale review
	w.Automations[index].Enabled = false
	w.Automations[index].Revision++
	now := time.Now().UnixMilli()
	w.Automations[index].UpdatedAt = now
	w.Revision++
	w.UpdatedAt = now
	pending, err := ws.unfinishedWorkerRuns(account, id, autoID)
	if err != nil {
		return WorkerRecord{}, nil, err
	}
	h := WorkerRevisionRecord{WorkerID: id, AccountScopeID: account, Revision: w.Revision, Worker: w, CommittedAt: now, CommittedBy: user, ChangeSummary: "disabled automation " + autoID}
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if err := m.put(KeyWorker(account, id), w); err != nil {
		return WorkerRecord{}, nil, err
	}
	if err := m.put(KeyWorkerHistory(account, id, w.Revision), h); err != nil {
		return WorkerRecord{}, nil, err
	}
	if err := m.setPayload(WorkerRealtimePayload{WorkerID: id, Revision: w.Revision, LifecycleState: w.LifecycleState, ChangeSummary: h.ChangeSummary}); err != nil {
		return WorkerRecord{}, nil, err
	}
	if err := ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, nil, err
	}
	published = m
	return w, pending, nil
}

// unfinishedWorkerRuns is called under workersMu; never truncates an active set.
func (ws *WorkerStore) unfinishedWorkerRuns(account, id, autoID string) ([]WorkerRunRecord, error) {
	prefix := keyWorkerRunActivePrefix + keyPart(account) + "/"
	if id != "" {
		prefix += keyPart(id) + "/"
	}
	it, err := ws.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	var result []WorkerRunRecord
	scanned := 0
	for ok := it.First(); ok; ok = it.Next() {
		scanned++
		if scanned > 10000 {
			return nil, errors.New("worker active run scan budget exceeded")
		}
		var entry workerRunIndexEntry
		if err := json.Unmarshal(it.Value(), &entry); err != nil {
			return nil, err
		}
		r, found, err := ws.GetWorkerRun(account, entry.WorkerID, entry.RunID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if r.AccountScopeID != account || (id != "" && r.WorkerID != id) {
			return nil, ErrWorkerConflict
		}
		if (r.Status == "admitted" || r.Status == "queued" || r.Status == "preparing" || r.Status == "running" || r.Status == WorkerRunStatusWaiting) && (autoID == "" || r.AutomationID == autoID) {
			result = append(result, r)
		}
	}
	return result, it.Error()
}

func (ws *WorkerStore) UnfinishedWorkerRuns(account, id, autoID string) ([]WorkerRunRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return nil, errors.New("store is not open")
	}
	ws.store.workersMu.Lock()
	defer ws.store.workersMu.Unlock()
	return ws.unfinishedWorkerRuns(account, id, autoID)
}

// AcceptWorker accepts a pending worker proposal with its exact revision and approved bindings.
// Authenticated user ingress owns approval; AI tools cannot self-approve.
func (ws *WorkerStore) AcceptWorker(account, user, id string, revision uint64, bindings map[string]string, models ...*SessionModelProfileSnapshot) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	id = strings.TrimSpace(id)
	if account == "" || id == "" {
		return WorkerRecord{}, errors.New("account and worker id are required")
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	var w WorkerRecord
	found, err := ws.store.GetJSON(KeyWorker(account, id), &w)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !found || w.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision || (w.LifecycleState != WorkerLifecycleStatePending && w.PendingReview == nil) || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return WorkerRecord{}, ErrWorkerConflict
	}
	resumeState := WorkerLifecycleStateActive
	if w.PendingReview != nil {
		if w.PendingReviewStale {
			return WorkerRecord{}, fmt.Errorf("%w: pending review predates a direct save; cannot overwrite saved changes", ErrWorkerConflict)
		}
		if w.LifecycleState != WorkerLifecycleStateActive && w.LifecycleState != WorkerLifecycleStateIdle && w.LifecycleState != WorkerLifecycleStatePaused {
			return WorkerRecord{}, ErrWorkerConflict
		}
		if w.LifecycleState == WorkerLifecycleStatePaused {
			resumeState = WorkerLifecycleStatePaused
		}
		candidate := *w.PendingReview
		candidate.Revision = w.Revision
		w = candidate
		w.PendingReview = nil
	}
	if len(w.RequestedCapabilities) != 0 {
		return WorkerRecord{}, fmt.Errorf("%w: capability approvals unsupported", ErrWorkerConflict)
	}
	// A proposal is either box-level (no roles, no bindings) or proposes exactly
	// one required primary workspace that the caller accepts verbatim.
	primary, err := WorkerPrimaryBinding(w.WorkspaceRequirements, bindings)
	if err != nil {
		return WorkerRecord{}, fmt.Errorf("%w: %v", ErrWorkerConflict, err)
	}
	if primary != "" && len(w.ProposedBindings) == 0 {
		return WorkerRecord{}, fmt.Errorf("%w: worker proposal has no proposed bindings", ErrWorkerConflict)
	}
	if len(bindings) != len(w.ProposedBindings) {
		return WorkerRecord{}, fmt.Errorf("%w: accepted bindings must match exact proposed bindings", ErrWorkerConflict)
	}
	for k, v := range w.ProposedBindings {
		if strings.TrimSpace(bindings[k]) != strings.TrimSpace(v) {
			return WorkerRecord{}, fmt.Errorf("%w: accepted bindings must match exact proposed bindings", ErrWorkerConflict)
		}
	}
	now := time.Now().UnixMilli()
	if len(models) > 1 {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if len(models) == 1 {
		if err := ValidateWorkerModelProfile(models[0]); err != nil {
			return WorkerRecord{}, err
		}
		w.ModelProfile = CloneSessionModelProfileSnapshot(models[0])
	}
	w.LocalBindings = nil
	if primary != "" {
		w.LocalBindings = map[string]string{"primary": primary}
	}
	var budgetPolicy *WorkerBudgetPolicy
	if w.Budget != nil {
		// Match admission and SetLimits: workersMu precedes the account lock.
		unlockBudget := ws.store.sessionMutations.lockSessions("account:" + account)
		defer unlockBudget()
		ss := NewSessionStore(ws.store)
		existing, err := ss.GetWorkerBudget(account, id)
		if err != nil {
			return WorkerRecord{}, err
		}
		policy, err := ss.prepareWorkerBudget(account, id, existing.Revision, w.Budget.DailyUSD, w.Budget.DailyTokens)
		if err != nil {
			return WorkerRecord{}, err
		}
		budgetPolicy = &policy
		w.Budget = nil
	}
	w.LifecycleState = resumeState
	w.Revision++
	w.UpdatedAt = now
	hist := WorkerRevisionRecord{
		WorkerID:       id,
		AccountScopeID: account,
		Revision:       w.Revision,
		Worker:         w,
		CommittedAt:    now,
		CommittedBy:    user,
		ChangeSummary:  "accepted worker proposal",
	}
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if budgetPolicy != nil {
		if err := m.put(workerBudgetKey(account, id), *budgetPolicy); err != nil {
			return WorkerRecord{}, err
		}
	}
	for _, a := range w.Automations {
		if err = m.put(KeyWorkerByAutomation(account, a.ID), id); err != nil {
			return WorkerRecord{}, err
		}
	}
	if err = m.put(KeyWorker(account, id), w); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.put(KeyWorkerHistory(account, id, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.setPayload(WorkerRealtimePayload{
		WorkerID:       id,
		Revision:       w.Revision,
		LifecycleState: w.LifecycleState,
		ChangeSummary:  hist.ChangeSummary,
	}); err != nil {
		return WorkerRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	published = m
	return w, nil
}

// ActivateWorker commits approved local roles and admission state together.
// The service must authorize every binding before calling this method; callers
// cannot grant capabilities by passing a portable definition.
func (ws *WorkerStore) ActivateWorker(account, user, id string, revision uint64, bindings map[string]string) (WorkerRecord, error) {
	return ws.ConfigureWorkerBindings(account, user, id, revision, bindings, true)
}

// ConfigureWorkerBindings records user-approved roles without opening schedule
// admission when activate is false (an explicitly requested idle test).
func (ws *WorkerStore) ConfigureWorkerBindings(account, user, id string, revision uint64, bindings map[string]string, activate bool) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	var w WorkerRecord
	found, err := ws.store.GetJSON(KeyWorker(account, id), &w)
	if err != nil {
		return WorkerRecord{}, err
	}
	if !found || w.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision || (w.LifecycleState != WorkerLifecycleStateIdle && w.LifecycleState != WorkerLifecycleStatePaused) || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return WorkerRecord{}, ErrWorkerConflict
	}
	if len(w.RequestedCapabilities) != 0 {
		return WorkerRecord{}, fmt.Errorf("%w: capability approvals unsupported", ErrWorkerConflict)
	}
	if w.LifecycleState == WorkerLifecycleStatePaused {
		pending, err := ws.unfinishedWorkerRuns(account, id, "")
		if err != nil {
			return WorkerRecord{}, err
		}
		if len(pending) != 0 {
			return WorkerRecord{}, ErrWorkerConflict
		}
	}
	primary, err := WorkerPrimaryBinding(w.WorkspaceRequirements, bindings)
	if err != nil {
		return WorkerRecord{}, fmt.Errorf("%w: a box-level worker takes no bindings; a worker with a primary workspace role must bind exactly that role", ErrWorkerConflict)
	}
	now := time.Now().UnixMilli()
	w.LocalBindings = nil
	if primary != "" {
		w.LocalBindings = map[string]string{"primary": primary}
	}
	if activate {
		w.LifecycleState = WorkerLifecycleStateActive
	} else if w.LifecycleState != WorkerLifecycleStateIdle {
		return WorkerRecord{}, ErrWorkerConflict
	}
	w.Revision++
	w.UpdatedAt = now
	hist := WorkerRevisionRecord{WorkerID: id, AccountScopeID: account, Revision: w.Revision, Worker: w, CommittedAt: now, CommittedBy: user, ChangeSummary: "approved worker bindings"}
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if err = m.put(KeyWorker(account, id), w); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.put(KeyWorkerHistory(account, id, w.Revision), hist); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.setPayload(WorkerRealtimePayload{WorkerID: id, Revision: w.Revision, LifecycleState: w.LifecycleState, ChangeSummary: hist.ChangeSummary}); err != nil {
		return WorkerRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	published = m
	return w, nil
}

// EnableWorkerAutomation cannot reopen admission until the prior runs have stopped.
func (ws *WorkerStore) EnableWorkerAutomation(account, user, id, autoID string, revision uint64) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, errors.New("store is not open")
	}
	ws.store.workersMu.Lock()
	var published *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if published != nil {
			ws.store.publishWorkerRealtime(published)
		}
	}()
	w, found, err := ws.GetWorker(account, id)
	if err != nil {
		return w, err
	}
	if !found {
		return w, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision || (w.LifecycleState != WorkerLifecycleStateActive && w.LifecycleState != WorkerLifecycleStateIdle && w.LifecycleState != WorkerLifecycleStatePaused) || (w.Provenance != nil && (w.Provenance.MigratedAt != 0 || w.Provenance.SourceProposalID != "")) {
		return w, ErrWorkerConflict
	}
	pending, err := ws.unfinishedWorkerRuns(account, id, autoID)
	if err != nil {
		return w, err
	}
	if len(pending) != 0 {
		return w, ErrWorkerConflict
	}
	index := -1
	for i, a := range w.Automations {
		if a.ID == autoID {
			index = i
			break
		}
	}
	if index < 0 {
		return w, ErrWorkerNotFound
	}
	if w.Automations[index].Enabled {
		return w, ErrWorkerConflict
	}
	now := time.Now().UnixMilli()
	w.PendingReview = nil
	w.Automations[index].Enabled = true
	w.Automations[index].Revision++
	w.Automations[index].UpdatedAt = now
	w.Revision++
	w.UpdatedAt = now
	h := WorkerRevisionRecord{WorkerID: id, AccountScopeID: account, Revision: w.Revision, Worker: w, CommittedAt: now, CommittedBy: user, ChangeSummary: "enabled automation"}
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if err = m.put(KeyWorker(account, id), w); err != nil {
		return w, err
	}
	if err = m.put(KeyWorkerHistory(account, id, w.Revision), h); err != nil {
		return w, err
	}
	if err = m.setPayload(WorkerRealtimePayload{WorkerID: id, Revision: w.Revision, LifecycleState: w.LifecycleState, ChangeSummary: h.ChangeSummary}); err != nil {
		return w, err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return w, err
	}
	published = m
	return w, nil
}

// WorkerRunAdmissionExists reports whether an idempotency key or occurrence
// already admitted a run for this account, so callers can let a replay return
// its original receipt while still validating genuinely new admissions.
func (ws *WorkerStore) WorkerRunAdmissionExists(account string, req WorkerRunAdmission) (bool, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return false, errors.New("store is not open")
	}
	account = strings.TrimSpace(account)
	for _, key := range []string{req.IdempotencyKey, req.OccurrenceID} {
		if key == "" {
			continue
		}
		var prior workerRunIdempotency
		found, err := ws.store.GetJSON(KeyWorkerRunIdempotency(account, key), &prior)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

var workflowItemSource = regexp.MustCompile(`^workflow/[A-Za-z0-9._:-]{1,200}/delivery/[A-Za-z0-9._:-]{1,200}/step/[a-z0-9][a-z0-9_-]{0,63}$`)

// IsWorkflowItemSource identifies independently queued workflow jobs.
func IsWorkflowItemSource(source string) bool { return workflowItemSource.MatchString(source) }
