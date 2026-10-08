package pebblestore

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// WorkerRuntimeRecord journals an adapter's launch identity before external
// effects. An uncertain launch must reconcile this identity, never create a
// replacement merely because a network request timed out. This is a subordinate
// deployment record, not another worker/run authority. No credentials are stored.
type WorkerRuntimeRecord struct {
	AccountScopeID        string `json:"account_scope_id"`
	WorkerID              string `json:"worker_id"`
	DeploymentID          string `json:"deployment_id"`
	Generation            uint64 `json:"generation"`
	RuntimeID             string `json:"runtime_id"`
	LaunchKey             string `json:"launch_key"`
	ClaimDigest           string `json:"claim_digest"`
	BindingDigest         string `json:"binding_digest"`
	State                 string `json:"state"`
	LastSequence          uint64 `json:"last_sequence"`
	LastObservationDigest string `json:"last_observation_digest,omitempty"`
	EvidenceDigest        string `json:"evidence_digest,omitempty"`
	CreatedAt             int64  `json:"created_at"`
	ObservedAt            int64  `json:"observed_at,omitempty"`
}

// BindingDigest pins the complete adapter launch specification, including the
// executable, effective model, capability, context and transport identities.
// Only the canonical service may construct claims after authorization and target
// revalidation. A claim is not evidence of readiness or resource existence.
type WorkerRuntimeClaim struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	Generation       uint64 `json:"generation"`
	LaunchKey        string `json:"launch_key"`
	BindingDigest    string `json:"binding_digest"`
}

// WorkerRuntimeObservation is an internal authenticated-adapter fact, not a
// user-facing mutation DTO. Transport must bind identity before this boundary.
// EvidenceDigest identifies retained protocol evidence; it is not a substitute
// for checking that evidence in the adapter. Sequence is local to one generation.
type WorkerRuntimeObservation struct {
	RuntimeID      string `json:"runtime_id"`
	Generation     uint64 `json:"generation"`
	Sequence       uint64 `json:"sequence"`
	BindingDigest  string `json:"binding_digest"`
	State          string `json:"state"`
	EvidenceDigest string `json:"evidence_digest"`
}

func workerRuntimeKey(account, worker, deployment string, generation uint64) string {
	return workerControlKey(account, worker, "runtime-"+deployment, fmt.Sprintf("%020d", generation))
}

// Runtime reservations are independent of job slots: idle persistent runtimes
// still consume target capacity, and disconnection must not release ownership.
func workerRuntimeUsageKey(account string, target WorkerTargetReference) string {
	hash, _ := hashWorkerPayload([]string{target.Kind, target.WorkspaceID, target.ReferenceID})
	return workerControlKey(account, "targets", "runtime_usage", hash)
}

func validWorkerRuntimeDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (ws *WorkerStore) GetWorkerRuntime(account, worker, deployment string, generation uint64) (WorkerRuntimeRecord, error) {
	if _, err := ws.GetWorkerDeployment(account, worker, deployment); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	var r WorkerRuntimeRecord
	ok, err := ws.store.GetJSON(workerRuntimeKey(account, worker, deployment, generation), &r)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if !ok || r.AccountScopeID != account || r.WorkerID != worker || r.DeploymentID != deployment || r.Generation != generation {
		return WorkerRuntimeRecord{}, ErrWorkerNotFound
	}
	return r, nil
}

// ClaimWorkerRuntime serializes launch ownership with deployment/job admission.
// Replay returns the same journal even after stop intent; callers must re-read
// desired state before contacting a provider. This method performs no I/O beyond
// the existing atomic worker/outbox transaction.
func (ws *WorkerStore) ClaimWorkerRuntime(account, worker, deployment string, req WorkerRuntimeClaim) (WorkerRuntimeRecord, error) {
	if !validWorkerControlKey(req.LaunchKey) || !validWorkerRuntimeDigest(req.BindingDigest) || req.Generation == 0 || req.ExpectedRevision == 0 {
		return WorkerRuntimeRecord{}, ErrWorkerInvalid
	}
	digest, err := hashWorkerPayload(req)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	ws.store.workersMu.Lock()
	var mutation *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if mutation != nil {
			ws.store.publishWorkerRealtime(mutation)
		}
	}()
	d, err := ws.GetWorkerDeployment(account, worker, deployment)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	key := workerRuntimeKey(account, worker, deployment, req.Generation)
	var prior WorkerRuntimeRecord
	exists, err := ws.store.GetJSON(key, &prior)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if exists {
		if prior.AccountScopeID != account || prior.WorkerID != worker || prior.DeploymentID != deployment || prior.Generation != d.Generation || prior.ClaimDigest != digest {
			return WorkerRuntimeRecord{}, ErrWorkerConflict
		}
		return prior, nil
	}
	if d.Generation != req.Generation || d.Revision != req.ExpectedRevision || d.ApprovalState != "approved" || d.DesiredState != "running" || d.CleanupScope != "owned_runtime" || d.CleanupState != "not_allocated" || (d.Lifecycle == "on_demand" && d.ActiveJobID == "") {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	w, err := ws.controlWorker(account, worker)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	c, err := ws.GetWorkerContext(account, worker, 0)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if w.Revision != d.WorkerRevision || w.PendingReview != nil || w.LifecycleState != WorkerLifecycleStateActive || c.Revision != d.ContextRevision {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	if err = ws.validateWorkerTargetCapacity(account, d.Target); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	var runtimeCount int
	if _, err = ws.store.GetJSON(workerRuntimeUsageKey(account, d.Target), &runtimeCount); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if runtimeCount < 0 || runtimeCount >= d.Target.Capacity {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	now := time.Now().UnixMilli()
	r := WorkerRuntimeRecord{AccountScopeID: account, WorkerID: worker, DeploymentID: deployment, Generation: d.Generation, RuntimeID: "wrt_" + strings.TrimPrefix(GenerateWorkerID(), "worker_"), LaunchKey: req.LaunchKey, ClaimDigest: digest, BindingDigest: req.BindingDigest, State: "launching", CreatedAt: now}
	d.ObservedState = "launching"
	d.CleanupState = "pending"
	d.UpdatedAt = now
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: d.ApprovedBy, workerID: worker}
	if d.ActiveJobID != "" {
		job, found, e := ws.GetWorkerRun(account, worker, d.ActiveJobID)
		if e != nil {
			return WorkerRuntimeRecord{}, e
		}
		if !found || job.Placement == nil || job.Placement.DeploymentID != d.ID || job.Placement.Generation != d.Generation || job.Placement.State != "pending_adapter" || job.Status != "admitted" {
			return WorkerRuntimeRecord{}, ErrWorkerConflict
		}
		job.Placement.State = "launch_reserved"
		if err = candidate.put(KeyWorkerRun(account, worker, job.ID), job); err != nil {
			return WorkerRuntimeRecord{}, err
		}
	}
	if err = candidate.put(workerRuntimeUsageKey(account, d.Target), runtimeCount+1); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if err = candidate.put(key, r); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if err = candidate.put(workerControlKey(account, worker, "deployment", deployment), d); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	mutation = candidate
	return r, nil
}

// ObserveWorkerRuntime commits ordered facts and the deployment projection in
// one transaction. Disconnect is not termination. Stop evidence is accepted only
// after the canonical job has finalized; it never implicitly acknowledges a
// command, changes context, releases a job reservation or grants host ownership.
func (ws *WorkerStore) ObserveWorkerRuntime(account, worker, deployment string, obs WorkerRuntimeObservation) (WorkerRuntimeRecord, error) {
	if obs.Sequence == 0 || obs.Generation == 0 || !validWorkerRuntimeDigest(obs.BindingDigest) || !validWorkerRuntimeDigest(obs.EvidenceDigest) {
		return WorkerRuntimeRecord{}, ErrWorkerInvalid
	}
	switch obs.State {
	case "ready", "disconnected", "unknown", "failed", "stopped":
	default:
		return WorkerRuntimeRecord{}, ErrWorkerInvalid
	}
	digest, err := hashWorkerPayload(obs)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	ws.store.workersMu.Lock()
	var mutation *workerRealtimeMutation
	defer func() {
		ws.store.workersMu.Unlock()
		if mutation != nil {
			ws.store.publishWorkerRealtime(mutation)
		}
	}()
	d, err := ws.GetWorkerDeployment(account, worker, deployment)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	r, err := ws.GetWorkerRuntime(account, worker, deployment, obs.Generation)
	if err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if r.RuntimeID != obs.RuntimeID || r.Generation != d.Generation || r.BindingDigest != obs.BindingDigest {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	if obs.Sequence == r.LastSequence && digest == r.LastObservationDigest {
		return r, nil
	}
	if obs.Sequence != r.LastSequence+1 || r.State == "stopped" {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	if obs.State == "ready" && d.DesiredState != "running" {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	if obs.State == "stopped" && (d.ActiveJobID != "" || (d.DesiredState != "stopped" && d.Lifecycle != "on_demand")) {
		return WorkerRuntimeRecord{}, ErrWorkerConflict
	}
	r.State = obs.State
	r.LastSequence = obs.Sequence
	r.LastObservationDigest = digest
	r.EvidenceDigest = obs.EvidenceDigest
	r.ObservedAt = time.Now().UnixMilli()
	d.ObservedState = r.State
	d.UpdatedAt = r.ObservedAt
	if r.State == "stopped" {
		d.CleanupState = "verified"
	}
	candidate := &workerRealtimeMutation{accountScopeID: account, userID: d.ApprovedBy, workerID: worker}
	if r.State == "stopped" {
		var runtimeCount int
		if _, err = ws.store.GetJSON(workerRuntimeUsageKey(account, d.Target), &runtimeCount); err != nil {
			return WorkerRuntimeRecord{}, err
		}
		if runtimeCount < 1 {
			return WorkerRuntimeRecord{}, ErrWorkerConflict
		}
		if err = candidate.put(workerRuntimeUsageKey(account, d.Target), runtimeCount-1); err != nil {
			return WorkerRuntimeRecord{}, err
		}
	}
	if err = candidate.put(workerRuntimeKey(account, worker, deployment, r.Generation), r); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if err = candidate.put(workerControlKey(account, worker, "deployment", deployment), d); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(candidate); err != nil {
		return WorkerRuntimeRecord{}, err
	}
	mutation = candidate
	return r, nil
}
