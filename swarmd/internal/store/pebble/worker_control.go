package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrWorkerRemoteUnavailable = errors.New("remote worker execution unavailable: no qualified adapter installed")

// WorkerTargetReference binds existing connection/topology authority, not a
// second credential store. ReferenceDigest is computed by the service.
type WorkerTargetReference struct {
	Kind            string `json:"kind"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	ReferenceID     string `json:"reference_id"`
	ReferenceDigest string `json:"reference_digest"`
	Capacity        int    `json:"capacity"`
}
type WorkerDeploymentRequest struct {
	WorkerRevision  uint64                `json:"worker_revision"`
	ContextRevision uint64                `json:"context_revision"`
	Target          WorkerTargetReference `json:"target"`
	Lifecycle       string                `json:"lifecycle"`
	IdempotencyKey  string                `json:"idempotency_key"`
}
type WorkerDeploymentRecord struct {
	ID              string                `json:"id"`
	AccountScopeID  string                `json:"account_scope_id"`
	WorkerID        string                `json:"worker_id"`
	WorkerRevision  uint64                `json:"worker_revision"`
	ContextRevision uint64                `json:"context_revision"`
	Target          WorkerTargetReference `json:"target"`
	Lifecycle       string                `json:"lifecycle"`
	Revision        uint64                `json:"revision"`
	Generation      uint64                `json:"generation"`
	DesiredState    string                `json:"desired_state"`
	ObservedState   string                `json:"observed_state"`
	ApprovalState   string                `json:"approval_state"`
	ApprovalDigest  string                `json:"approval_digest"`
	ApprovedBy      string                `json:"approved_by,omitempty"`
	CleanupScope    string                `json:"cleanup_scope"`
	CleanupState    string                `json:"cleanup_state"`
	ActiveJobID     string                `json:"active_job_id,omitempty"`
	CreatedAt       int64                 `json:"created_at"`
	UpdatedAt       int64                 `json:"updated_at"`
}
type WorkerContextRecord struct {
	WorkerID    string                   `json:"worker_id"`
	Revision    uint64                   `json:"revision"`
	Text        string                   `json:"text"`
	References  []WorkerContextReference `json:"references,omitempty"`
	Provenance  string                   `json:"provenance"`
	PublishedBy string                   `json:"published_by"`
	PublishedAt int64                    `json:"published_at"`
}

// References are opaque authorized catalog/checkpoint references, not arbitrary
// files to ingest. Consumers must reauthorize them before materialization.
type WorkerContextReference struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}
type WorkerContextUpdate struct {
	ExpectedRevision uint64                   `json:"expected_revision"`
	Text             string                   `json:"text"`
	References       []WorkerContextReference `json:"references,omitempty"`
	Provenance       string                   `json:"provenance"`
}
type WorkerPlacementAdmission struct {
	DeploymentID       string `json:"deployment_id"`
	DeploymentRevision uint64 `json:"deployment_revision"`
	ContextRevision    uint64 `json:"context_revision"`
}
type WorkerRunPlacement struct {
	DeploymentID       string                `json:"deployment_id"`
	DeploymentRevision uint64                `json:"deployment_revision"`
	Target             WorkerTargetReference `json:"target"`
	ContextRevision    uint64                `json:"context_revision"`
	Generation         uint64                `json:"generation"`
	AttemptID          string                `json:"attempt_id"`
	AttemptNumber      uint64                `json:"attempt_number"`
	State              string                `json:"state"`
}
type WorkerCommandRequest struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	Generation       uint64 `json:"generation"`
	Kind             string `json:"kind"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type WorkerCommandRecord struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id"`
	Generation   uint64 `json:"generation"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	RequestedBy  string `json:"requested_by"`
	CreatedAt    int64  `json:"created_at"`
}
type workerControlReceipt struct {
	Hash string `json:"hash"`
	ID   string `json:"id"`
}

const workerControlPrefix = "worker_control/account/"

func workerControlKey(account, worker, kind, id string) string {
	return workerControlPrefix + keyPart(account) + "/" + keyPart(worker) + "/" + kind + "/" + keyPart(id)
}
func validWorkerControlKey(key string) bool {
	return key != "" && len(key) <= 256 && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "/\\\r\n\x00")
}
func (ws *WorkerStore) controlWorker(account, id string) (WorkerRecord, error) {
	w, ok, err := ws.GetWorker(account, id)
	if err != nil {
		return w, err
	}
	if !ok || w.ID != id || w.LifecycleState == WorkerLifecycleStateDeleted {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	return w, nil
}
func (ws *WorkerStore) GetWorkerContext(account, id string, revision uint64) (WorkerContextRecord, error) {
	if _, err := ws.controlWorker(account, id); err != nil {
		return WorkerContextRecord{}, err
	}
	suffix := "current"
	if revision != 0 {
		suffix = fmt.Sprintf("revision-%020d", revision)
	}
	var c WorkerContextRecord
	ok, err := ws.store.GetJSON(workerControlKey(account, id, "context", suffix), &c)
	if err != nil {
		return c, err
	}
	if !ok {
		if revision == 0 {
			return WorkerContextRecord{WorkerID: id}, nil
		}
		return c, ErrWorkerNotFound
	}
	return c, nil
}
func (ws *WorkerStore) PutWorkerContext(account, user, id string, req WorkerContextUpdate) (WorkerContextRecord, error) {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(req.Provenance) == "" || len(req.Provenance) > 1024 || len(req.Text) > 64*1024 || len(req.References) > 32 {
		return WorkerContextRecord{}, ErrWorkerInvalid
	}
	for _, r := range req.References {
		if (r.Kind != "artifact" && r.Kind != "checkpoint") || strings.TrimSpace(r.Reference) == "" || len(r.Reference) > 2048 {
			return WorkerContextRecord{}, ErrWorkerInvalid
		}
	}
	ws.store.workersMu.Lock()
	var m *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if m != nil {
			ws.store.publishWorkerRealtime(m)
		}
	}()
	c, err := ws.GetWorkerContext(account, id, 0)
	if err != nil {
		return c, err
	}
	if c.Revision != req.ExpectedRevision {
		return WorkerContextRecord{}, ErrWorkerConflict
	}
	runs, err := ws.unfinishedWorkerRuns(account, id, "")
	if err != nil {
		return WorkerContextRecord{}, err
	}
	if len(runs) != 0 {
		return WorkerContextRecord{}, fmt.Errorf("%w: worker has active jobs", ErrWorkerConflict)
	}
	var writer string
	if _, err = ws.store.GetJSON(workerControlKey(account, id, "writer", "current"), &writer); err != nil {
		return WorkerContextRecord{}, err
	}
	if writer != "" {
		return WorkerContextRecord{}, fmt.Errorf("%w: worker context writer is reserved", ErrWorkerConflict)
	}
	c = WorkerContextRecord{WorkerID: id, Revision: c.Revision + 1, Text: req.Text, References: append([]WorkerContextReference(nil), req.References...), Provenance: req.Provenance, PublishedBy: user, PublishedAt: time.Now().UnixMilli()}
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	if err = candidate.put(workerControlKey(account, id, "context", "current"), c); err != nil {
		return WorkerContextRecord{}, err
	}
	if err = candidate.put(workerControlKey(account, id, "context", fmt.Sprintf("revision-%020d", c.Revision)), c); err != nil {
		return WorkerContextRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerContextRecord{}, err
	}
	m = candidate
	return c, nil
}
func (ws *WorkerStore) GetWorkerDeployment(account, worker, id string) (WorkerDeploymentRecord, error) {
	if _, err := ws.controlWorker(account, worker); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	var d WorkerDeploymentRecord
	ok, err := ws.store.GetJSON(workerControlKey(account, worker, "deployment", id), &d)
	if err != nil {
		return d, err
	}
	if !ok || d.AccountScopeID != account || d.WorkerID != worker || d.ID != id {
		return WorkerDeploymentRecord{}, ErrWorkerNotFound
	}
	return d, nil
}
func (ws *WorkerStore) ListWorkerDeployments(account, worker string) ([]WorkerDeploymentRecord, error) {
	if _, err := ws.controlWorker(account, worker); err != nil {
		return nil, err
	}
	out := []WorkerDeploymentRecord{}
	err := ws.store.IteratePrefix(workerControlKey(account, worker, "deployment", ""), 101, func(_ string, b []byte) error {
		var d WorkerDeploymentRecord
		if err := json.Unmarshal(b, &d); err != nil {
			return err
		}
		if d.AccountScopeID != account || d.WorkerID != worker {
			return ErrWorkerInvalid
		}
		out = append(out, d)
		return nil
	})
	if len(out) > 100 {
		return nil, errors.New("deployment scan budget exceeded")
	}
	return out, err
}
func (ws *WorkerStore) ProposeWorkerDeployment(account, user, worker string, req WorkerDeploymentRequest) (WorkerDeploymentRecord, error) {
	if !validWorkerControlKey(req.IdempotencyKey) || strings.TrimSpace(user) == "" || req.WorkerRevision == 0 || !validWorkerControlKey(req.Target.ReferenceID) || req.Target.ReferenceDigest == "" || req.Target.Capacity < 1 || req.Target.Capacity > 100 {
		return WorkerDeploymentRecord{}, ErrWorkerInvalid
	}
	if req.Lifecycle != "persistent" && req.Lifecycle != "on_demand" {
		return WorkerDeploymentRecord{}, ErrWorkerInvalid
	}
	if req.Target.Kind != "ssh" && req.Target.Kind != "gcp" {
		return WorkerDeploymentRecord{}, ErrWorkerInvalid
	}
	if req.Target.Kind == "ssh" && !validWorkerControlKey(req.Target.WorkspaceID) {
		return WorkerDeploymentRecord{}, ErrWorkerInvalid
	}
	hash, err := hashWorkerPayload(struct {
		User    string
		Request WorkerDeploymentRequest
	}{user, req})
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	ws.store.workersMu.Lock()
	var m *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if m != nil {
			ws.store.publishWorkerRealtime(m)
		}
	}()
	w, err := ws.controlWorker(account, worker)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	ik := workerControlKey(account, worker, "deployment_idempotency", req.IdempotencyKey)
	var prior workerControlReceipt
	ok, err := ws.store.GetJSON(ik, &prior)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if ok {
		if prior.Hash != hash {
			return WorkerDeploymentRecord{}, ErrWorkerConflict
		}
		return ws.GetWorkerDeployment(account, worker, prior.ID)
	}
	if w.Revision != req.WorkerRevision || w.PendingReview != nil || w.LifecycleState == WorkerLifecycleStatePending {
		return WorkerDeploymentRecord{}, ErrWorkerConflict
	}
	c, err := ws.GetWorkerContext(account, worker, 0)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if c.Revision != req.ContextRevision {
		return WorkerDeploymentRecord{}, ErrWorkerConflict
	}
	existing, err := ws.ListWorkerDeployments(account, worker)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if err := ws.validateWorkerTargetCapacity(account, req.Target); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if len(existing) >= 100 {
		return WorkerDeploymentRecord{}, errors.New("worker deployment limit reached")
	}
	now := time.Now().UnixMilli()
	d := WorkerDeploymentRecord{ID: "wdep_" + strings.TrimPrefix(GenerateWorkerID(), "worker_"), AccountScopeID: account, WorkerID: worker, WorkerRevision: req.WorkerRevision, ContextRevision: req.ContextRevision, Target: req.Target, Lifecycle: req.Lifecycle, Revision: 1, Generation: 1, DesiredState: "stopped", ObservedState: "unavailable", ApprovalState: "pending", CleanupScope: "owned_runtime", CleanupState: "not_allocated", CreatedAt: now, UpdatedAt: now}
	// Registration of an existing target never proves ownership of the machine.
	// A future provisioning adapter must establish exact resource ownership before
	// any compute-delete capability can be added.
	d.ApprovalDigest, err = hashWorkerPayload(d)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	if err = candidate.put(workerControlKey(account, worker, "deployment", d.ID), d); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if err = candidate.put(workerTargetPolicyKey(account, req.Target), req.Target.Capacity); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if err = candidate.put(ik, workerControlReceipt{Hash: hash, ID: d.ID}); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	m = candidate
	return d, nil
}
func (ws *WorkerStore) ApproveWorkerDeployment(account, user, worker, id string, revision uint64, digest string) (WorkerDeploymentRecord, error) {
	ws.store.workersMu.Lock()
	var m *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if m != nil {
			ws.store.publishWorkerRealtime(m)
		}
	}()
	d, err := ws.GetWorkerDeployment(account, worker, id)
	if err != nil {
		return d, err
	}
	w, err := ws.controlWorker(account, worker)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	c, err := ws.GetWorkerContext(account, worker, 0)
	if err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if user == "" || revision != d.Revision || digest != d.ApprovalDigest || d.ApprovalState != "pending" || w.Revision != d.WorkerRevision || w.PendingReview != nil || c.Revision != d.ContextRevision {
		return WorkerDeploymentRecord{}, ErrWorkerConflict
	}
	d.ApprovalState = "approved"
	d.DesiredState = "running"
	d.ApprovedBy = user
	d.Revision++
	d.UpdatedAt = time.Now().UnixMilli()
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	if err = candidate.put(workerControlKey(account, worker, "deployment", id), d); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerDeploymentRecord{}, err
	}
	m = candidate
	return d, nil
}

// pinWorkerPlacement runs inside the canonical admission mutex and adds all
// reservations to its existing atomic run/idempotency/outbox transaction.
func (ws *WorkerStore) pinWorkerPlacement(account string, req WorkerRunAdmission, w WorkerRecord, r *WorkerRunRecord, m *workerRealtimeMutation) error {
	p := req.Placement
	var currentWriter string
	if _, err := ws.store.GetJSON(workerControlKey(account, w.ID, "writer", "current"), &currentWriter); err != nil {
		return err
	}
	if currentWriter != "" {
		return fmt.Errorf("%w: worker context writer reserved", ErrWorkerConflict)
	}
	if p == nil {
		return nil
	}
	active, err := ws.unfinishedWorkerRuns(account, w.ID, "")
	if err != nil {
		return err
	}
	if len(active) != 0 {
		return fmt.Errorf("%w: another job owns worker context", ErrWorkerConflict)
	}
	d, err := ws.GetWorkerDeployment(account, w.ID, p.DeploymentID)
	if err != nil {
		return err
	}
	c, err := ws.GetWorkerContext(account, w.ID, 0)
	if err != nil {
		return err
	}
	if d.Revision != p.DeploymentRevision || d.WorkerRevision != w.Revision || d.ApprovalState != "approved" || d.DesiredState != "running" || d.ContextRevision != p.ContextRevision || c.Revision != p.ContextRevision || d.ActiveJobID != "" || w.PendingReview != nil || req.ResolvedModelProfile == nil {
		return ErrWorkerConflict
	}
	if err := ws.validateWorkerTargetCapacity(account, d.Target); err != nil {
		return err
	}
	var used int
	if _, err := ws.store.GetJSON(workerTargetUsageKey(account, d.Target), &used); err != nil {
		return err
	}
	if used >= d.Target.Capacity {
		return fmt.Errorf("%w: target capacity exhausted", ErrWorkerConflict)
	}
	if err := m.put(workerTargetUsageKey(account, d.Target), used+1); err != nil {
		return err
	}
	r.Placement = &WorkerRunPlacement{DeploymentID: d.ID, DeploymentRevision: d.Revision, Target: d.Target, ContextRevision: c.Revision, Generation: d.Generation, AttemptID: r.ID + "-attempt-1", AttemptNumber: 1, State: "pending_adapter"}
	d.ActiveJobID = r.ID
	d.UpdatedAt = time.Now().UnixMilli()
	if err = m.put(workerControlKey(account, w.ID, "deployment", d.ID), d); err != nil {
		return err
	}
	return m.put(workerControlKey(account, w.ID, "writer", "current"), r.ID)
}
func (ws *WorkerStore) QueueWorkerCommand(account, user, worker, id string, req WorkerCommandRequest) (WorkerCommandRecord, error) {
	if !validWorkerControlKey(req.IdempotencyKey) || user == "" {
		return WorkerCommandRecord{}, ErrWorkerInvalid
	}
	// Start is deliberately unavailable, not a queued claim of remote execution.
	if req.Kind == "start" {
		return WorkerCommandRecord{}, ErrWorkerRemoteUnavailable
	}
	if req.Kind != "stop" {
		return WorkerCommandRecord{}, ErrWorkerInvalid
	}
	hash, err := hashWorkerPayload(struct {
		User    string
		Request WorkerCommandRequest
	}{user, req})
	if err != nil {
		return WorkerCommandRecord{}, err
	}
	ws.store.workersMu.Lock()
	var m *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if m != nil {
			ws.store.publishWorkerRealtime(m)
		}
	}()
	d, err := ws.GetWorkerDeployment(account, worker, id)
	if err != nil {
		return WorkerCommandRecord{}, err
	}
	ik := workerControlKey(account, worker, "command_idempotency", id+"-"+req.IdempotencyKey)
	var prior workerControlReceipt
	ok, err := ws.store.GetJSON(ik, &prior)
	if err != nil {
		return WorkerCommandRecord{}, err
	}
	if ok {
		if prior.Hash != hash {
			return WorkerCommandRecord{}, ErrWorkerConflict
		}
		var c WorkerCommandRecord
		ok, err := ws.store.GetJSON(workerControlKey(account, worker, "command-"+id, prior.ID), &c)
		if err != nil {
			return c, err
		}
		if !ok {
			return c, ErrWorkerConflict
		}
		return c, nil
	}
	if d.Revision != req.ExpectedRevision || d.Generation != req.Generation || d.ApprovalState != "approved" {
		return WorkerCommandRecord{}, ErrWorkerConflict
	}
	commands, err := ws.ListWorkerCommands(account, worker, id)
	if err != nil {
		return WorkerCommandRecord{}, err
	}
	if len(commands) >= 100 {
		return WorkerCommandRecord{}, errors.New("command limit reached")
	}
	c := WorkerCommandRecord{ID: "wcmd_" + strings.TrimPrefix(GenerateWorkerID(), "worker_"), DeploymentID: id, Generation: d.Generation, Kind: req.Kind, Status: "pending", RequestedBy: user, CreatedAt: time.Now().UnixMilli()}
	d.DesiredState = "stopped"
	d.Revision++
	d.UpdatedAt = c.CreatedAt
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	for key, value := range map[string]any{workerControlKey(account, worker, "command-"+id, c.ID): c, workerControlKey(account, worker, "deployment", id): d, ik: workerControlReceipt{Hash: hash, ID: c.ID}} {
		if err = candidate.put(key, value); err != nil {
			return WorkerCommandRecord{}, err
		}
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerCommandRecord{}, err
	}
	m = candidate
	return c, nil
}
func (ws *WorkerStore) ListWorkerCommands(account, worker, id string) ([]WorkerCommandRecord, error) {
	if _, err := ws.GetWorkerDeployment(account, worker, id); err != nil {
		return nil, err
	}
	out := []WorkerCommandRecord{}
	err := ws.store.IteratePrefix(workerControlKey(account, worker, "command-"+id, ""), 101, func(_ string, b []byte) error {
		var c WorkerCommandRecord
		if err := json.Unmarshal(b, &c); err != nil {
			return err
		}
		if c.DeploymentID != id {
			return ErrWorkerInvalid
		}
		out = append(out, c)
		return nil
	})
	if len(out) > 100 {
		return nil, errors.New("command scan budget exceeded")
	}
	return out, err
}

// Capacity policy references the canonical target identity and is shared across
// worker deployments. A later proposal cannot silently increase an existing
// target's capacity. No credential/topology data is duplicated here.
func workerTargetPolicyKey(account string, t WorkerTargetReference) string {
	hash, _ := hashWorkerPayload([]string{t.Kind, t.WorkspaceID, t.ReferenceID})
	return workerControlKey(account, "targets", "capacity", hash)
}
func workerTargetUsageKey(account string, t WorkerTargetReference) string {
	hash, _ := hashWorkerPayload([]string{t.Kind, t.WorkspaceID, t.ReferenceID})
	return workerControlKey(account, "targets", "usage", hash)
}
func (ws *WorkerStore) validateWorkerTargetCapacity(account string, t WorkerTargetReference) error {
	var capacity int
	ok, err := ws.store.GetJSON(workerTargetPolicyKey(account, t), &capacity)
	if err != nil {
		return err
	}
	if ok && capacity != t.Capacity {
		return fmt.Errorf("%w: target capacity policy differs", ErrWorkerConflict)
	}
	return nil
}

// CancelPendingWorkerJob is a hub-only acknowledgement that an unstarted intent
// was cancelled. It never reports remote cleanup or execution completion. Once
// an adapter changes attempt state this operation fails closed.
func (ws *WorkerStore) CancelPendingWorkerJob(account, user, worker, runID string, generation uint64) (WorkerRunRecord, error) {
	ws.store.workersMu.Lock()
	var m *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if m != nil {
			ws.store.publishWorkerRealtime(m)
		}
	}()
	r, ok, err := ws.GetWorkerRun(account, worker, runID)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, ErrWorkerNotFound
	}
	if user == "" || r.Placement == nil || r.Placement.Generation != generation {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	if r.Status == "cancelled" && r.Placement.State == "cancelled_before_dispatch" {
		return r, nil
	}
	if r.Status != "admitted" || r.Placement.State != "pending_adapter" {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	d, err := ws.GetWorkerDeployment(account, worker, r.Placement.DeploymentID)
	if err != nil {
		return WorkerRunRecord{}, err
	}
	var writer string
	if _, err = ws.store.GetJSON(workerControlKey(account, worker, "writer", "current"), &writer); err != nil {
		return WorkerRunRecord{}, err
	}
	if d.Generation != generation || d.ActiveJobID != r.ID || writer != r.ID {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	var used int
	if _, err = ws.store.GetJSON(workerTargetUsageKey(account, d.Target), &used); err != nil {
		return WorkerRunRecord{}, err
	}
	if used < 1 {
		return WorkerRunRecord{}, ErrWorkerConflict
	}
	r.Status = "cancelled"
	r.CancelRequested = true
	r.CompletedAt = time.Now().UnixMilli()
	r.Placement.State = "cancelled_before_dispatch"
	d.ActiveJobID = ""
	d.UpdatedAt = r.CompletedAt
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: worker}
	for key, value := range map[string]any{KeyWorkerRun(account, worker, r.ID): r, workerControlKey(account, worker, "deployment", d.ID): d, workerControlKey(account, worker, "writer", "current"): "", workerTargetUsageKey(account, d.Target): used - 1} {
		if err = candidate.put(key, value); err != nil {
			return WorkerRunRecord{}, err
		}
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerRunRecord{}, err
	}
	m = candidate
	return r, nil
}
