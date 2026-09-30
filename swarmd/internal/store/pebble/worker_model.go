package pebblestore

import (
	"fmt"
	"strings"
	"time"
)

// ValidateWorkerModelProfile rejects unresolved profiles: workers never follow a
// mutable account default after the initial selection has been captured.
func ValidateWorkerModelProfile(p *SessionModelProfileSnapshot) error {
	if p == nil || p.UseAccountDefault || p.Plan == nil {
		return fmt.Errorf("worker requires a pinned model profile")
	}
	if p.Source != SessionModelProfileSourceTemporary && p.Source != SessionModelProfileSourceSwarmSettings && p.Source != SessionModelProfileSourceSaved {
		return fmt.Errorf("worker model profile source is invalid")
	}
	for _, selection := range []*ModelProfileSelection{&p.Action, p.Plan} {
		if selection != nil && (strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "") {
			return fmt.Errorf("worker model provider and model are required")
		}
	}
	return nil
}

// InitializeWorkerModelProfile is a one-time migration, never a live model edit.
// The service must resolve the canonical default before crossing this CAS.
func (ws *WorkerStore) InitializeWorkerModelProfile(account, user, id string, revision uint64, profile *SessionModelProfileSnapshot) (WorkerRecord, error) {
	if ws == nil || ws.store == nil || ws.store.db == nil {
		return WorkerRecord{}, fmt.Errorf("worker store is not open")
	}
	if err := ValidateWorkerModelProfile(profile); err != nil {
		return WorkerRecord{}, err
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
		return WorkerRecord{}, err
	}
	if !found || w.AccountScopeID != account {
		return WorkerRecord{}, ErrWorkerNotFound
	}
	if revision == 0 || w.Revision != revision || w.ModelProfile != nil || w.LifecycleState == WorkerLifecycleStateDeleted || w.LifecycleState == WorkerLifecycleStateStopping {
		return WorkerRecord{}, ErrWorkerConflict
	}
	w.ModelProfile = CloneSessionModelProfileSnapshot(profile)
	w.Revision++
	w.UpdatedAt = time.Now().UnixMilli()
	m := &workerRealtimeMutation{accountScopeID: account, userID: user, workerID: id}
	history := WorkerRevisionRecord{WorkerID: id, AccountScopeID: account, Revision: w.Revision, Worker: w, CommittedAt: w.UpdatedAt, CommittedBy: user, ChangeSummary: "initialized worker model"}
	if err = m.put(KeyWorker(account, id), w); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.put(KeyWorkerHistory(account, id, w.Revision), history); err != nil {
		return WorkerRecord{}, err
	}
	if err = m.setPayload(WorkerRealtimePayload{WorkerID: id, Revision: w.Revision, LifecycleState: w.LifecycleState, ChangeSummary: history.ChangeSummary}); err != nil {
		return WorkerRecord{}, err
	}
	if err = ws.store.commitWorkerRealtime(m); err != nil {
		return WorkerRecord{}, err
	}
	published = m
	return w, nil
}
