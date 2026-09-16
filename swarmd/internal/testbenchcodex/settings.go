package testbenchcodex

import (
	"errors"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	db "swarm/packages/swarmd/internal/store/pebble"
	"time"
)

// Configure is invoked only in a candidate overlay. Identity and model writes
// use the ordinary canonical stores; no host setting or credential is imported.
func Configure(ids *identity.Service, settings *db.AgentModelSettingsStore, models *model.Service) error {
	if err := models.EnsureBootDefaults(); err != nil {
		return err
	}
	state, err := ids.StateSummary()
	if err != nil {
		return err
	}
	if state.CurrentUser == nil && state.AccountScope == nil {
		if _, err := ids.BootstrapFirstIdentity("testbench"); err != nil {
			return err
		}
		state, err = ids.StateSummary()
		if err != nil {
			return err
		}
	}
	if state.CurrentUser == nil || state.AccountScope == nil {
		return errors.New("incomplete testbench identity")
	}
	lookup, err := models.GetCatalog("codex", Model)
	if err != nil {
		return err
	}
	if !lookup.Found {
		return errors.New("required Luna catalog entry unavailable")
	}
	resolved, _, err := models.SetPreferenceForAccount(state.AccountScope.ID, state.CurrentUser.ID, "codex", Model, Thinking)
	if err != nil {
		return err
	}
	if !resolved.CatalogPresent || resolved.Preference.Provider != "codex" || resolved.Preference.Model != Model || resolved.Preference.Thinking != Thinking {
		return errors.New("required Luna medium settings did not resolve exactly")
	}
	a := db.AgentModelAssignment{Provider: "codex", Model: Model, Thinking: Thinking}
	_, err = settings.PutForAccount(db.AgentModelSettingsRecord{AccountScopeID: state.AccountScope.ID, Swarm: db.SwarmAgentModelAssignments{Action: a, Plan: a}, SystemAgents: db.SystemAgentModelAssignments{Compact: a, Finder: a, Coder: a, Designer: a, Router: a}, UpdatedAt: time.Now().UnixMilli()})
	return err
}
