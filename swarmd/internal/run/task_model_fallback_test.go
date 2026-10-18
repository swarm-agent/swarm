package run

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/agentmodelsettings"
	"swarm/packages/swarmd/internal/model"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func taskFallbackFixture(t *testing.T) (*Service, *pebblestore.AgentModelSettingsStore, *pebblestore.Store) {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "fixture.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	catalogStore := pebblestore.NewModelCatalogStore(db)
	var records []pebblestore.ModelCatalogRecord
	for _, name := range []string{"specialist", "account-a-action", "account-b-action"} {
		records = append(records, pebblestore.ModelCatalogRecord{Provider: "fixture-provider", Model: name, Reasoning: true, ThinkingOptions: []string{"low", "high"}, DefaultThinking: "low", ServiceTiers: []string{"priority"}, ContextModes: []pebblestore.ModelCatalogContextMode{{Mode: "long", ContextWindow: 10000}}, ContextWindow: 1000})
	}
	if err := catalogStore.ReplaceSnapshot(records, pebblestore.ModelCatalogMeta{Source: "fixture"}); err != nil {
		t.Fatal(err)
	}
	settingsStore := pebblestore.NewAgentModelSettingsStore(db)
	s := &Service{agents: agentruntime.NewService(pebblestore.NewAgentStore(db), events), model: model.NewService(pebblestore.NewModelStore(db), events, model.NewCatalogService(catalogStore)), agentModelSettings: agentmodelsettings.NewService(settingsStore)}
	return s, settingsStore, db
}

func taskFallbackSettings(t *testing.T, store *pebblestore.AgentModelSettingsStore, account, specialist, action string) pebblestore.AgentModelSettingsRecord {
	t.Helper()
	assigned := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: specialist, Thinking: "low"}
	fallback := pebblestore.AgentModelAssignment{Provider: "fixture-provider", Model: action, Thinking: "high", ServiceTier: "priority", ContextMode: "long"}
	record, err := store.PutForAccount(pebblestore.AgentModelSettingsRecord{AccountScopeID: account, Swarm: pebblestore.SwarmAgentModelAssignments{Action: fallback, Plan: assigned}, SystemAgents: pebblestore.SystemAgentModelAssignments{Compact: assigned, Finder: assigned, Coder: assigned, Designer: assigned, Router: assigned}})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestTaskModelFallbackPreservesConfiguredSpecialist(t *testing.T) {
	// Requirement: configured specialist models retain their canonical assignment.
	// Threat: fallback silently replaces valid account choices or compiled tools.
	// Owners: strict task resolver and resolveTaskLaunchProfileWithFallback; real
	// local catalog/settings stores prove selection without making provider calls.
	s, settings, _ := taskFallbackFixture(t)
	before := taskFallbackSettings(t, settings, "account-a", "specialist", "account-a-action")
	parent := pebblestore.SessionSnapshot{AccountScopeID: "account-a", Mode: "auto", Metadata: map[string]any{"agent_name": "fixture-parent"}}
	for _, role := range []string{"coder", "finder", "designer"} {
		t.Run(role, func(t *testing.T) {
			want, wantVirtual, wantSource, err := s.resolveTaskLaunchProfileForMode(parent, role, "auto")
			if err != nil {
				t.Fatal(err)
			}
			got, virtual, source, alert, err := s.resolveTaskLaunchProfileWithFallback(parent, role, "auto")
			if err != nil || alert != "" || virtual != wantVirtual || source != wantSource || !reflect.DeepEqual(got, want) {
				t.Fatalf("configured specialist changed: alert=%q error=%v", alert, err)
			}
		})
	}
	after, found, err := settings.GetForAccount("account-a")
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatal("selection mutated canonical settings")
	}
}

func TestTaskModelFallbackUsesOwningAccountAndCompiledRole(t *testing.T) {
	// Requirement: unavailable specialists use only their account's Swarm Action
	// assignment and disclose the replacement without changing role permissions.
	// Threat: cross-account/global fallback or inheritance of parent tool authority.
	// Owners: fallback resolver, model catalog and compiled ResolveSystemAgent;
	// real isolated stores are the narrowest authority/selection test layer.
	s, settings, db := taskFallbackFixture(t)
	if err := db.PutBytes(pebblestore.KeyModelCatalog("fixture-provider", "unavailable-specialist"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	before := taskFallbackSettings(t, settings, "account-a", "unavailable-specialist", "account-a-action")
	taskFallbackSettings(t, settings, "account-b", "unavailable-specialist", "account-b-action")
	for _, account := range []string{"account-a", "account-b"} {
		for _, role := range []string{"coder", "finder", "designer"} {
			t.Run(account+"/"+role, func(t *testing.T) {
				parent := pebblestore.SessionSnapshot{AccountScopeID: account, Mode: "auto", Metadata: map[string]any{"agent_name": "fixture-parent"}}
				got, virtual, source, alert, err := s.resolveTaskLaunchProfileWithFallback(parent, role, "auto")
				if err != nil {
					t.Fatal(err)
				}
				if got.Provider != "fixture-provider" || got.Model != account+"-action" || got.Thinking != "high" || got.AutoServiceTier != "priority" || got.ContextMode != "long" {
					t.Fatalf("wrong resolved account assignment: %#v", got)
				}
				if !strings.Contains(alert, "unavailable-specialist") || !strings.Contains(alert, "fixture-provider/"+account+"-action") {
					t.Fatalf("fallback cause/selection missing: %q", alert)
				}
				if virtual != (role == "coder") || role == "coder" && source != "fixture-parent" {
					t.Fatal("Coder virtual/source identity changed")
				}
				want, err := s.agents.ResolveSystemAgent(got.Name, pebblestore.AgentProfile{Provider: got.Provider, Model: got.Model, Thinking: got.Thinking, AutoServiceTier: got.AutoServiceTier, ContextMode: got.ContextMode})
				if err != nil {
					t.Fatal(err)
				}
				want.ContextMode = got.ContextMode
				if !reflect.DeepEqual(got, want) {
					t.Fatal("fallback changed compiled role contract")
				}
			})
		}
	}
	after, _, _ := settings.GetForAccount("account-a")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("fallback rewrote canonical assignment")
	}
}

func TestTaskModelFallbackUnavailableFailsClosed(t *testing.T) {
	// Requirement: no readable configured account fallback means no profile.
	// Threat: silently selecting another account or a process/global default.
	// Owner: resolveTaskLaunchProfileWithFallback; isolated authoritative settings
	// exercise missing account and corrupt catalog without allocating a child.
	s, settings, db := taskFallbackFixture(t)
	if err := db.PutBytes(pebblestore.KeyModelCatalog("fixture-provider", "unavailable-specialist"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	taskFallbackSettings(t, settings, "account-b", "specialist", "account-b-action")
	taskFallbackSettings(t, settings, "account-a", "unavailable-specialist", "unavailable-fallback")
	if err := db.PutBytes(pebblestore.KeyModelCatalog("fixture-provider", "unavailable-fallback"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"", "missing-account", "account-a"} {
		t.Run(account, func(t *testing.T) {
			profile, _, _, alert, err := s.resolveTaskLaunchProfileWithFallback(pebblestore.SessionSnapshot{AccountScopeID: account}, "coder", "auto")
			if err == nil || !strings.Contains(err.Error(), "fallback unavailable") || profile.Name != "" || profile.Model != "" || alert != "" {
				t.Fatalf("unconfigured fallback selected profile: %#v, %q, %v", profile, alert, err)
			}
		})
	}
}

func TestTaskModelFallbackPreservesWorkerHelperRestriction(t *testing.T) {
	// Requirement: a worker Coder may obtain only its read-only Finder helper.
	// Threat: treating an authorization denial as a recoverable model failure.
	// Owners: workerHelperParent, strict task resolver and helper materializer;
	// direct real-service resolution proves no profile authority is widened.
	s, settings, db := taskFallbackFixture(t)
	if err := db.PutBytes(pebblestore.KeyModelCatalog("fixture-provider", "unavailable-specialist"), []byte("{")); err != nil {
		t.Fatal(err)
	}
	taskFallbackSettings(t, settings, "account-a", "unavailable-specialist", "account-a-action")
	parent := pebblestore.SessionSnapshot{AccountScopeID: "account-a", Metadata: map[string]any{"agent_profile": pebblestore.AgentProfile{Name: agentruntime.WorkerCoderAgentID}}}
	profile, virtual, source, alert, err := s.resolveTaskLaunchProfileWithFallback(parent, "finder", "auto")
	if err != nil || profile.Name != agentruntime.HelperAgentID || virtual || source != "" || alert == "" {
		t.Fatalf("helper fallback failed: %#v, %q, %v", profile, alert, err)
	}
	want := agentruntime.HelperAgentProfileForParent(profile)
	if !reflect.DeepEqual(profile.ToolContract, want.ToolContract) || profile.Name != want.Name {
		t.Fatal("helper authority changed")
	}
	for _, role := range []string{"coder", "designer"} {
		profile, _, _, alert, err := s.resolveTaskLaunchProfileWithFallback(parent, role, "auto")
		if err == nil || !strings.Contains(err.Error(), "Finder helpers only") || profile.Name != "" || alert != "" {
			t.Fatal("model fallback bypassed worker helper restriction")
		}
	}
}

func TestTaskModelFallbackPreservesCanonicalUnlistedModelSuccess(t *testing.T) {
	// Requirement: canonical support for custom/unlisted models remains intact.
	// Threat: a fallback-only wrapper introduces a new catalog admission policy.
	// Owners: strict resolver and fallback wrapper; real store selection proves
	// canonical success is returned unchanged, without a provider request.
	s, settings, _ := taskFallbackFixture(t)
	taskFallbackSettings(t, settings, "account-a", "unlisted-custom-model", "account-a-action")
	parent := pebblestore.SessionSnapshot{AccountScopeID: "account-a"}
	for _, role := range []string{"coder", "finder", "designer"} {
		want, _, _, err := s.resolveTaskLaunchProfileForMode(parent, role, "auto")
		if err != nil {
			t.Fatal(err)
		}
		got, _, _, alert, err := s.resolveTaskLaunchProfileWithFallback(parent, role, "auto")
		if err != nil || alert != "" || !reflect.DeepEqual(got, want) || got.Model != "unlisted-custom-model" {
			t.Fatalf("custom canonical success replaced: %q, %q, %v", got.Model, alert, err)
		}
	}
}
