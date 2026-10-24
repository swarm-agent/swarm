package run

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/agentmodel"
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
	return s.ResolveWorkerRoleModelProfile(account, "", selected)
}

// workerRoleAssignment returns the account-scoped model setting of a worker
// role's system agent; ok is false for the Swarm role, which uses its plan and
// action settings.
func workerRoleAssignment(settings store.AgentModelSettingsRecord, role string) (store.AgentModelAssignment, bool) {
	switch strings.TrimSpace(role) {
	case store.WorkerRoleCoder:
		return settings.SystemAgents.Coder, true
	case store.WorkerRoleReviewer:
		if settings.SystemAgents.Reviewer.Model != "" {
			return settings.SystemAgents.Reviewer, true
		}
		return settings.SystemAgents.Coder, true
	case store.WorkerRoleDesigner:
		return settings.SystemAgents.Designer, true
	case store.WorkerRoleFinder:
		return settings.SystemAgents.Finder, true
	default:
		return store.AgentModelAssignment{}, false
	}
}

// ResolveWorkerRoleModelProfile is ResolveWorkerModelProfile for a worker role:
// inherited slots capture the role's account-scoped agent model setting (the
// Swarm role keeps its plan/action settings). An unconfigured or unavailable
// role setting falls back to the account default with a ResolutionWarning,
// never to a hardcoded model.
func (s *Service) ResolveWorkerRoleModelProfile(account, role string, selected *store.SessionModelProfileSnapshot) (*store.SessionModelProfileSnapshot, error) {
	if s == nil || s.model == nil {
		return nil, errors.New("worker model catalog unavailable")
	}
	p := store.CloneSessionModelProfileSnapshot(selected)
	if p != nil && (p.UseAccountDefault || p.ActionUseAccountDefault || p.PlanUseAccountDefault) {
		if err := store.ValidateWorkerModelProfile(p); err != nil {
			return nil, err
		}
		defaults, err := s.ResolveWorkerRoleModelProfile(account, role, nil)
		if err != nil {
			return nil, err
		}
		if p.UseAccountDefault || p.ActionUseAccountDefault {
			p.Action = defaults.Action
		}
		if p.UseAccountDefault || p.PlanUseAccountDefault {
			p.Plan = store.CloneModelProfileSelection(defaults.Plan)
		}
		p.ResolutionWarning = defaults.ResolutionWarning
	}
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
		if assignment, isRole := workerRoleAssignment(settings, role); isRole {
			if strings.TrimSpace(role) == store.WorkerRoleReviewer {
				assignment, _, err = agentmodel.EffectiveReviewerAssignment(s.model, settings)
				if err != nil {
					return s.workerDefaultFallback(account, err)
				}
			}
			if strings.TrimSpace(assignment.Provider) == "" || strings.TrimSpace(assignment.Model) == "" {
				return s.workerDefaultFallback(account, fmt.Errorf("%s model setting is not configured", role))
			}
			plan := selection(assignment)
			p = &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, Action: selection(assignment), Plan: &plan}
		} else {
			plan := selection(settings.Swarm.Plan)
			p = &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, Action: selection(settings.Swarm.Action), Plan: &plan}
		}
	} else if err := store.ValidateWorkerModelProfile(p); err != nil {
		return nil, err
	}
	for slot, sel := range []*store.ModelProfileSelection{&p.Action, p.Plan} {
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
		requested := *sel
		// The picker names the standard tier explicitly; model resolution stores
		// it as empty. Accept only this known equivalent, not arbitrary options
		// that resolution may silently discard as unsupported.
		if requested.ServiceTier == "standard" {
			requested.ServiceTier = ""
		}
		if selected != nil && !selected.UseAccountDefault && !(slot == 0 && selected.ActionUseAccountDefault) && !(slot == 1 && selected.PlanUseAccountDefault) && (pref.Provider != requested.Provider || pref.Model != requested.Model || pref.Thinking != requested.Thinking || pref.ServiceTier != requested.ServiceTier || pref.ContextMode != requested.ContextMode) {
			return nil, errors.New("worker model options are invalid; select supported thinking, service tier and context options")
		}
		*sel = store.ModelProfileSelection{Provider: pref.Provider, Model: pref.Model, Thinking: pref.Thinking, ServiceTier: pref.ServiceTier, ContextMode: pref.ContextMode}
	}
	p.UseAccountDefault = selected == nil || selected.UseAccountDefault
	if p.AppliedAt == 0 {
		p.AppliedAt = time.Now().UnixMilli()
	}
	return p, store.ValidateWorkerModelProfile(p)
}

func (s *WorkerExecutionService) ResolveModelProfile(account string, selected *store.SessionModelProfileSnapshot) (*store.SessionModelProfileSnapshot, error) {
	return s.host.runs.ResolveWorkerModelProfile(account, selected)
}

// ResolveRoleModelProfile resolves a worker's model policy against its role's
// account-scoped model setting.
func (s *WorkerExecutionService) ResolveRoleModelProfile(account, role string, selected *store.SessionModelProfileSnapshot) (*store.SessionModelProfileSnapshot, error) {
	return s.host.runs.ResolveWorkerRoleModelProfile(account, role, selected)
}

func (s *WorkerExecutionService) initializeWorkerModel(w store.WorkerRecord, user string) (store.WorkerRecord, error) {
	if w.ModelProfile != nil {
		return w, store.ValidateWorkerModelProfile(w.ModelProfile)
	}
	p, err := s.ResolveRoleModelProfile(w.AccountScopeID, w.Role, nil)
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
// warning travels with the resolved profile into review and generated task alerts.
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
	p := &store.SessionModelProfileSnapshot{Source: store.SessionModelProfileSourceSwarmSettings, UseAccountDefault: true, Action: selection, Plan: &selection, AppliedAt: time.Now().UnixMilli(), ResolutionWarning: fmt.Sprintf("Worker model settings resolution failed (%v); captured account default %s/%s for planning and action.", cause, pref.Provider, pref.Model)}
	return p, store.ValidateWorkerModelProfile(p)
}
