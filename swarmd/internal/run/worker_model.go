package run

import (
	"errors"
	"fmt"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

func workerExecutionPreference(w store.WorkerRecord) store.ModelPreference {
	a := w.ModelProfile.Action
	if w.ExecutionMode == "plan" {
		a = *w.ModelProfile.Plan
	}
	return store.ModelPreference{Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, ServiceTier: a.ServiceTier, ContextMode: a.ContextMode}
}

// ResolveWorkerModelProfile captures account-owned Swarm plan/action settings or
// validates an explicit review selection without writing any account settings.
func (s *Service) ResolveWorkerModelProfile(account string, selected *store.SessionModelProfileSnapshot) (*store.SessionModelProfileSnapshot, error) {
	if s == nil || s.model == nil {
		return nil, errors.New("worker model catalog unavailable")
	}
	p := store.CloneSessionModelProfileSnapshot(selected)
	if p == nil {
		if s.agentModelSettings == nil {
			return nil, errors.New("worker default model settings unavailable")
		}
		settings, err := s.agentModelSettings.GetForAccount(account)
		if err != nil {
			return s.workerDefaultFallback(account, err)
		}
		selection := func(a store.AgentModelAssignment) store.ModelProfileSelection {
			return store.ModelProfileSelection{Provider: a.Provider, Model: a.Model, Thinking: a.Thinking, ServiceTier: a.ServiceTier, ContextMode: a.ContextMode}
		}
		plan := selection(settings.Swarm.Plan)
		p = &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, Action: selection(settings.Swarm.Action), Plan: &plan}
	} else if err := store.ValidateWorkerModelProfile(p); err != nil {
		return nil, err
	}
	for _, sel := range []*store.ModelProfileSelection{&p.Action, p.Plan} {
		if sel == nil {
			continue
		}
		resolved, err := s.model.ResolvePreference(store.ModelPreference{Provider: sel.Provider, Model: sel.Model, Thinking: sel.Thinking, ServiceTier: sel.ServiceTier, ContextMode: sel.ContextMode})
		if err != nil {
			if selected == nil {
				return s.workerDefaultFallback(account, err)
			}
			return nil, fmt.Errorf("choose an available worker model: %w", err)
		}
		if !resolved.CatalogPresent {
			if selected == nil {
				return s.workerDefaultFallback(account, errors.New("configured Swarm model is unavailable in the catalog"))
			}
			return nil, fmt.Errorf("worker model %s/%s is not in the authorized model catalog", sel.Provider, sel.Model)
		}
		pref := resolved.Preference
		if selected != nil && (pref.Provider != sel.Provider || pref.Model != sel.Model || pref.Thinking != sel.Thinking || pref.ServiceTier != sel.ServiceTier || pref.ContextMode != sel.ContextMode) {
			return nil, errors.New("worker model options are invalid; select supported thinking, service tier and context options")
		}
		*sel = store.ModelProfileSelection{Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking, ServiceTier: pref.ServiceTier, ContextMode: pref.ContextMode}
	}
	p.UseAccountDefault = false
	if p.AppliedAt == 0 {
		p.AppliedAt = time.Now().UnixMilli()
	}
	return p, store.ValidateWorkerModelProfile(p)
}

func (s *WorkerExecutionService) ResolveModelProfile(account string, selected *store.SessionModelProfileSnapshot) (*store.SessionModelProfileSnapshot, error) {
	return s.host.runs.ResolveWorkerModelProfile(account, selected)
}

func (s *WorkerExecutionService) initializeWorkerModel(w store.WorkerRecord, user string) (store.WorkerRecord, error) {
	if w.ModelProfile != nil {
		return w, store.ValidateWorkerModelProfile(w.ModelProfile)
	}
	p, err := s.ResolveModelProfile(w.AccountScopeID, nil)
	if err != nil {
		return store.WorkerRecord{}, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return store.WorkerRecord{}, err
	}
	return ws.InitializeWorkerModelProfile(w.AccountScopeID, user, w.ID, w.Revision, p)
}

// Fallback is only for initialization, never an explicit review override. The
// warning travels with the pinned profile into review and generated task alerts.
func (s *Service) workerDefaultFallback(account string, cause error) (*store.SessionModelProfileSnapshot, error) {
	resolved, err := s.model.GetResolvedPreferenceForAccount(account)
	if err != nil {
		return nil, fmt.Errorf("worker default resolution failed (%v); account default unavailable: %w", cause, err)
	}
	if !resolved.CatalogPresent {
		return nil, fmt.Errorf("worker default resolution failed (%v); configure a valid account default model", cause)
	}
	pref := resolved.Preference
	selection := store.ModelProfileSelection{Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking, ServiceTier: pref.ServiceTier, ContextMode: pref.ContextMode}
	p := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, Action: selection, Plan: &selection, AppliedAt: time.Now().UnixMilli(), ResolutionWarning: fmt.Sprintf("Worker model settings resolution failed (%v); captured account default %s/%s for planning and action.", cause, pref.Provider, pref.Model)}
	return p, store.ValidateWorkerModelProfile(p)
}
