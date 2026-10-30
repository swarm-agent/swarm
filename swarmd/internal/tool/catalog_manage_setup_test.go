package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// recordingSetup records each setup service call and answers with a fixed
// record.
type recordingSetup struct {
	calls []string
	who   []string
}

func (r *recordingSetup) ManageWorkflows(context.Context, identity.Principal, string, map[string]any) (any, error) {
	return nil, context.Canceled
}

func (r *recordingSetup) ManageSetup(_ context.Context, _ identity.Principal, who string, args map[string]any) (any, error) {
	r.calls = append(r.calls, asString(args["machine"])+"/"+asString(args["action"]))
	r.who = append(r.who, who)
	return map[string]any{"ok": true}, nil
}

// Purpose:
//   - Requirement: manage_setup is declared once in the catalog with every
//     setup action the swarm_* setup tools had; every change action is above
//     read level and says it waits for the person's decision; model and media
//     model changes say they are made only when the person asks; the
//     local-only actions (remote's localOnlyTools) are home-only and
//     local_only; keys, secrets and permissions never reach a linked machine;
//     every action declares its route scopes and is in the Orchestrator's
//     definition, which is generated from the declarations; reads are in
//     Swarm Control's default listing.
//   - Threat/regression: a setup action the Orchestrator cannot reach, a
//     change advertised as decision-free, key or secret management over a
//     link, or a relay reaching sign-in or key creation.
//   - Boundary/authority: manageSetupCatalog, OrchestratorDefinition,
//     FleetWorkActionAllowed, ManageSetupChange.
//   - Narrowest layer: the declaration itself.
func TestManageSetupCatalogDeclaresEverySetupAction(t *testing.T) {
	catalog, ok := CatalogToolNamed(ManageSetupToolName)
	if !ok {
		t.Fatal("manage_setup is not a catalog tool")
	}
	want := []string{"models", "providers", "usage", "plan_usage", "limits", "permissions", "list_keys", "list_fleet_keys", "list_secrets", "media_models",
		"set_agent_model", "set_media_models", "set_limits", "set_usage_limits", "set_permissions", "create_key", "create_client_key", "request_fleet_key",
		"revoke_key", "revoke_fleet_key", "request_provider_key", "connect_chatgpt", "connect_claude_code", "request_update", "cancel_update"}
	if got := catalogActionNames(catalog); len(got) != len(want) {
		t.Fatalf("manage_setup declares %v", got)
	}
	reads := map[string]bool{"models": true, "providers": true, "usage": true, "plan_usage": true, "limits": true, "permissions": true, "list_keys": true, "list_fleet_keys": true, "list_secrets": true, "media_models": true}
	localOnly := map[string]bool{"create_key": true, "create_client_key": true, "connect_chatgpt": true, "connect_claude_code": true, "request_provider_key": true, "request_fleet_key": true}
	neverRemote := map[string]bool{"permissions": true, "set_permissions": true, "list_keys": true, "revoke_key": true, "list_fleet_keys": true, "revoke_fleet_key": true, "list_secrets": true}
	definition := catalog.OrchestratorDefinition()
	enum := definition.Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
	if len(enum) != len(want) {
		t.Fatalf("the Orchestrator's definition lists %v", enum)
	}
	for _, name := range want {
		action, ok := catalog.Action(name)
		if !ok {
			t.Fatalf("manage_setup lacks %s", name)
		}
		if len(action.APIScopes) == 0 || !action.Listed(CatalogProfileOrchestrator) || !action.ControlCallable() {
			t.Fatalf("%s: scopes %v profiles %v", name, action.APIScopes, action.Profiles)
		}
		if reads[name] != (action.Level == CatalogLevelRead) {
			t.Fatalf("%s: level %s", name, action.Level)
		}
		if reads[name] && !action.Listed(CatalogProfileEssentials) {
			t.Fatalf("read %s is not in the default listing", name)
		}
		raw, _ := json.Marshal(map[string]any{"action": name})
		if ManageSetupChange(string(raw)) == reads[name] {
			t.Fatalf("ManageSetupChange(%s) = %v", name, !reads[name])
		}
		if !reads[name] && name != "create_key" && name != "create_client_key" && !strings.Contains(action.Description, "person's decision") {
			t.Fatalf("change %s does not say it waits for the person: %q", name, action.Description)
		}
		if action.LocalOnly != localOnly[name] {
			t.Fatalf("%s local_only %v", name, action.LocalOnly)
		}
		home := localOnly[name] || neverRemote[name]
		if home != (action.Reach == CatalogReachHome) || FleetWorkActionAllowed(ManageSetupToolName, name) == home {
			t.Fatalf("%s reach %s, linked allowed %v", name, action.Reach, FleetWorkActionAllowed(ManageSetupToolName, name))
		}
	}
	for _, name := range []string{"set_agent_model", "set_media_models"} {
		if action, _ := catalog.Action(name); !strings.Contains(action.Description, "Only when the person asks") {
			t.Fatalf("%s does not say it is only made when the person asks: %q", name, action.Description)
		}
	}
	if !strings.Contains(catalog.Description, "make one only when the person asks") || !strings.Contains(catalog.Description, "never change a model or media model on your own") {
		t.Fatalf("manage_setup's description: %q", catalog.Description)
	}
	// Unreadable or unknown calls are changes: nothing runs undecided.
	for _, raw := range []string{`not json`, `{"action":"no-such"}`, `{}`} {
		if !ManageSetupChange(raw) {
			t.Fatalf("ManageSetupChange(%s) is false", raw)
		}
	}
	found := false
	for _, d := range NewRuntime(1).Definitions() {
		found = found || d.Name == ManageSetupToolName
	}
	if !found {
		t.Fatal("the runtime offers no manage_setup definition")
	}
}

// Purpose:
//   - Requirement: Swarm Control and the Orchestrator run manage_setup
//     through the one handler with the same envelope; a conversation's
//     change runs only with the person's decision for that call
//     (WithPersonDecision, which only the run's gate sets) and only for the
//     Orchestrator; a Swarm Control key below an action's level is refused
//     before anything runs; machine= on a home-only action is refused
//     (keys, secrets, permissions and sign-in never cross a link).
//   - Threat/regression: an agent changing machine setup without the person,
//     a non-Orchestrator agent or worker reaching setup, a read key changing
//     setup, or key management on a linked machine.
//   - Boundary/authority: Runtime.ExecuteControlCatalog, executeManageSetup,
//     PersonDecisionFromContext, catalogEnvelope, with a recording service.
//   - Narrowest layer: the shared handler, called as both surfaces call it.
func TestManageSetupHandlerHoldsChangesToThePersonsDecision(t *testing.T) {
	_, store := openTaskViewStore(t)
	account, user := "account-setup", "user-setup"
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: user, AccountScopeID: account}
	if err := store.CreateSessionForAccount(pebblestore.SessionSnapshot{ID: "orch", AccountScopeID: account, UserID: user}, user, account); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(1)
	defer r.Close()
	r.SetManageSessionService(session.NewService(store, nil))
	service := &recordingSetup{}
	r.SetWorkflowLifecycleService(service)
	control := func(levels []string, args map[string]any) (string, error) {
		return r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: p, Levels: levels, KeyID: "k1", Tool: ManageSetupToolName, Arguments: args})
	}
	orchestrator := func(agent string, decided bool, args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		ctx := WithCatalogAgent(context.Background(), agent)
		if decided {
			ctx = WithPersonDecision(ctx)
		}
		return r.ExecuteForWorkspaceScopeWithRuntime(ctx, WorkspaceScope{SessionID: "orch", Principal: p}, Call{Name: ManageSetupToolName, Arguments: string(raw)})
	}
	catalog, _ := CatalogToolNamed(ManageSetupToolName)
	for _, action := range catalog.Actions {
		if action.Level == CatalogLevelRead {
			continue
		}
		args := map[string]any{"action": action.Action}
		if _, err := orchestrator("system-orchestrator", false, args); err == nil || !strings.Contains(err.Error(), "needs the person's decision") {
			t.Fatalf("undecided %s: %v", action.Action, err)
		}
	}
	for _, agent := range []string{"system-coder", "worker-agent", ""} {
		if _, err := orchestrator(agent, true, map[string]any{"action": "models"}); err == nil || !strings.Contains(err.Error(), "Orchestrator's") {
			t.Fatalf("agent %q read setup: %v", agent, err)
		}
	}
	for _, refused := range []struct {
		levels []string
		action string
		need   string
	}{
		{[]string{"swarm:read"}, "set_agent_model", "needs swarm:write"},
		{[]string{"swarm:read", "swarm:write"}, "set_permissions", "needs swarm:approve"},
		{[]string{"swarm:read", "swarm:write", "swarm:approve"}, "set_usage_limits", "needs swarm:manage"},
		{[]string{"swarm:read", "swarm:write", "swarm:approve"}, "request_update", "needs swarm:manage"},
	} {
		if _, err := control(refused.levels, map[string]any{"action": refused.action}); err == nil || !strings.Contains(err.Error(), refused.need) {
			t.Fatalf("%s with %v: %v", refused.action, refused.levels, err)
		}
	}
	if _, err := r.ExecuteControlCatalog(context.Background(), ControlCatalogCall{Principal: p, Levels: []string{"swarm:read", "swarm:write", "swarm:manage"}, LevelsBound: true, KeyID: "relay", Tool: ManageSetupToolName, Arguments: map[string]any{"action": "connect_chatgpt"}}); err == nil {
		t.Fatal("a relayed caller reached connect_chatgpt")
	}
	for _, name := range []string{"list_keys", "list_secrets", "permissions"} {
		if _, err := control([]string{"swarm:read"}, map[string]any{"action": name, "machine": "box"}); err == nil || !strings.Contains(err.Error(), "this machine only") {
			t.Fatalf("%s on a linked machine: %v", name, err)
		}
	}
	if len(service.calls) != 0 {
		t.Fatalf("a refused call reached the service: %v", service.calls)
	}
	// The same call through both surfaces: the same envelope.
	read := map[string]any{"action": "usage", "period": "today"}
	a, err := control([]string{"swarm:read"}, read)
	if err != nil {
		t.Fatal(err)
	}
	b, err := orchestrator("system-orchestrator", false, read)
	if err != nil || a != b || CheckEnvelope(a) != nil {
		t.Fatalf("envelopes differ or break the contract:\n%s\n%s %v", a, b, err)
	}
	// A decided change runs; a Swarm Control change runs at its level.
	if _, err := orchestrator("system-orchestrator", true, map[string]any{"action": "cancel_update"}); err != nil {
		t.Fatal(err)
	}
	if _, err := control([]string{"swarm:manage"}, map[string]any{"action": "cancel_update"}); err != nil {
		t.Fatal(err)
	}
	linked, err := control([]string{"swarm:read"}, map[string]any{"action": "limits", "machine": "box"})
	if err != nil || CheckEnvelope(linked) != nil {
		t.Fatalf("linked: %s %v", linked, err)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(linked), &e); err != nil || e.Machine != "box" || !e.UntrustedContent {
		t.Fatalf("linked envelope: %s", linked)
	}
	if strings.Join(service.calls, ",") != "/usage,/usage,/cancel_update,/cancel_update,box/limits" || service.who[0] != "key:k1" || service.who[1] != "orch" {
		t.Fatalf("calls %v by %v", service.calls, service.who)
	}
}
