package run

import (
	"context"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestParseManageSessionsDeployArgumentsRejectsTrustFields(t *testing.T) {
	for _, field := range []string{"agent_profile", "tool_contract", "runtime_mode", "worktree_path", "manifest_digest", "idempotency_key"} {
		_, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","` + field + `":"untrusted"}]}`)
		if err == nil || !strings.Contains(err.Error(), "rejects untrusted field") {
			t.Fatalf("field %s: err = %v", field, err)
		}
	}
}

func TestParseManageSessionsDeployArgumentsBoundsAndModes(t *testing.T) {
	if _, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[]}`); err == nil {
		t.Fatal("empty proposals accepted")
	}
	if _, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","mode":"read"}]}`); err == nil {
		t.Fatal("unsupported mode accepted")
	}
	parsed, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].Mode != "auto" || !parsed[0].Worktree {
		t.Fatalf("parsed = %#v", parsed)
	}
	disabled, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","worktree":false}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].Worktree {
		t.Fatalf("explicit worktree override = %#v", disabled)
	}
	named, err := parseManageSessionsDeployArguments(`{"action":"deploy","proposals":[{"prompt":"work","worktree_name":"subagent live cards"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(named) != 1 || named[0].WorktreeName != "subagent live cards" {
		t.Fatalf("worktree name suggestion = %#v", named)
	}
}

func TestResolveManageSessionsDeploySwarmSeparatesCompiledIdentityFromModePreferences(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	split := pebblestore.AgentProfile{
		Name: agentruntime.SwarmAgentID, Mode: agentruntime.ModePrimary, Enabled: true, ModelMode: "split",
		PlanProvider: "codex", PlanModel: "plan-model", PlanThinking: "high",
		AutoProvider: "openai", AutoModel: "auto-model", AutoThinking: "medium",
	}
	resolution, found, err := svc.resolveManageSessionsDeployAgent(map[string]pebblestore.AgentProfile{agentruntime.SwarmAgentID: split}, agentruntime.SwarmAgentID)
	if err != nil || !found {
		t.Fatalf("resolve Swarm: found=%t err=%v", found, err)
	}
	identity := resolution.ExecutionProfile
	if identity.Name != agentruntime.SwarmAgentID || identity.Mode != agentruntime.ModePrimary || identity.RuntimeMode != pebblestore.AgentRuntimeModePlanAuto || identity.Prompt != agentruntime.SwarmAgentPrompt() || identity.ToolContract == nil {
		t.Fatalf("compiled Swarm identity = %#v", identity)
	}
	if identity.Provider != "" || identity.Model != "" || identity.Thinking != "" || identity.ModelMode != "" {
		t.Fatalf("compiled Swarm identity became model-bearing: %#v", identity)
	}
	for _, test := range []struct {
		name, mode, provider, model, thinking string
	}{
		{name: "split auto", mode: sessionruntime.ModeAuto, provider: "openai", model: "auto-model", thinking: "medium"},
		{name: "split plan", mode: sessionruntime.ModePlan, provider: "codex", model: "plan-model", thinking: "high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolution.preferenceForMode(pebblestore.ModelPreference{}, test.mode)
			if got.Provider != test.provider || got.Model != test.model || got.Thinking != test.thinking {
				t.Fatalf("preference = %#v", got)
			}
		})
	}

	single := split
	single.ModelMode = "single"
	single.Provider, single.Model, single.Thinking = "anthropic", "single-model", "low"
	resolution, found, err = svc.resolveManageSessionsDeployAgent(map[string]pebblestore.AgentProfile{agentruntime.SwarmAgentID: single}, agentruntime.SwarmAgentID)
	if err != nil || !found {
		t.Fatalf("resolve single-model Swarm: found=%t err=%v", found, err)
	}
	for _, mode := range []string{sessionruntime.ModeAuto, sessionruntime.ModePlan} {
		got := resolution.preferenceForMode(pebblestore.ModelPreference{}, mode)
		if got.Provider != "anthropic" || got.Model != "single-model" || got.Thinking != "low" {
			t.Fatalf("single %s preference = %#v", mode, got)
		}
	}

	otherPrimary := pebblestore.AgentProfile{Name: "other-primary", Mode: agentruntime.ModePrimary, Enabled: true}
	if err := validateManageSessionsDeployAgent(otherPrimary, identity, true); err != nil {
		t.Fatalf("authorized explicit Swarm target rejected: %v", err)
	}
	if err := validateManageSessionsDeployAgent(otherPrimary, identity, false); err == nil || !strings.Contains(err.Error(), "requires calling primary") {
		t.Fatalf("ordinary alternate-agent gating changed: %v", err)
	}
}

func TestResolveQueuedAITaskDeployAgentPreservesActiveSplitPrimary(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	active := pebblestore.AgentProfile{
		Name: "split-primary", Mode: agentruntime.ModePrimary, Enabled: true,
		RuntimeMode: pebblestore.AgentRuntimeModePlanAuto, ExitPlanModeEnabled: pebblestore.BoolPtr(true),
		ModelMode: "split", PlanProvider: "codex", PlanModel: "plan-model", PlanThinking: "high",
		AutoProvider: "openai", AutoModel: "auto-model", AutoThinking: "medium",
	}
	swarm := pebblestore.AgentProfile{Name: agentruntime.SwarmAgentID, Mode: agentruntime.ModePrimary, Enabled: true}
	profiles := map[string]pebblestore.AgentProfile{active.Name: active, agentruntime.SwarmAgentID: swarm}

	resolution, found, err := svc.resolveQueuedAITaskDeployAgent(profiles, active.Name)
	if err != nil || !found {
		t.Fatalf("resolve active split primary: found=%t err=%v", found, err)
	}
	if resolution.ExecutionProfile.Name != active.Name || resolution.PreferenceProfile.Name != active.Name {
		t.Fatalf("queued task resolution = %#v, want active split primary", resolution)
	}
	plan := resolution.preferenceForMode(pebblestore.ModelPreference{}, sessionruntime.ModePlan)
	auto := resolution.preferenceForMode(pebblestore.ModelPreference{}, sessionruntime.ModeAuto)
	if plan.Provider != "codex" || plan.Model != "plan-model" || plan.Thinking != "high" {
		t.Fatalf("plan preference = %#v", plan)
	}
	if auto.Provider != "openai" || auto.Model != "auto-model" || auto.Thinking != "medium" {
		t.Fatalf("auto preference = %#v", auto)
	}

	profiles[agentruntime.SwarmAgentID] = func() pebblestore.AgentProfile {
		swarmSplit := active
		swarmSplit.Name = agentruntime.SwarmAgentID
		return swarmSplit
	}()
	compiledSwarm, found, err := svc.resolveQueuedAITaskDeployAgent(profiles, agentruntime.SwarmAgentID)
	if err != nil || !found || compiledSwarm.ExecutionProfile.Name != agentruntime.SwarmAgentID || compiledSwarm.ExecutionProfile.ModelMode != "" || compiledSwarm.PreferenceProfile.ModelMode != "split" {
		t.Fatalf("active Swarm split resolution = %#v found=%t err=%v", compiledSwarm, found, err)
	}

	active.ModelMode = "single"
	profiles[active.Name] = active
	fallback, found, err := svc.resolveQueuedAITaskDeployAgent(profiles, active.Name)
	if err != nil || !found || fallback.ExecutionProfile.Name != agentruntime.SwarmAgentID {
		t.Fatalf("non-split active fallback = %#v found=%t err=%v", fallback, found, err)
	}
}

func TestValidateManageSessionsDeployAgentDefaultsAndGating(t *testing.T) {
	active := pebblestore.AgentProfile{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, active, false); err != nil {
		t.Fatalf("active primary rejected without delegation: %v", err)
	}
	alternate := pebblestore.AgentProfile{Name: "finder", Mode: agentruntime.ModeSubagent, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, alternate, false); err == nil || !strings.Contains(err.Error(), "requires calling primary") {
		t.Fatalf("alternate agent gating err = %v", err)
	}
	if err := validateManageSessionsDeployAgent(active, alternate, true); err != nil {
		t.Fatalf("delegated subagent rejected: %v", err)
	}
	background := pebblestore.AgentProfile{Name: "background", Mode: agentruntime.ModeBackground, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, background, true); err == nil {
		t.Fatal("background agent accepted")
	}
}

func TestManageSessionsDeployDigestStableAndBound(t *testing.T) {
	manifest := manageSessionsDeployManifest{ManifestVersion: 1, Action: "deploy", ParentSessionID: "parent", AccountScopeID: "account", UserID: "user", Proposals: []manageSessionsDeployProposal{{ID: "proposal-1", Prompt: "work", Mode: "auto", AgentName: "swarm", WorkspaceID: "workspace", Selected: true}}}
	first, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestDigest = "ignored"
	manifest.ApprovedArguments = map[string]any{"ignored": true}
	manifest.AllowedWorkspaces = []manageSessionsDeployWorkspace{{ID: "workspace", Generation: 2, Path: "/workspace", Name: "Workspace"}}
	second, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digest changed across envelope fields: %s != %s", first, second)
	}
	manifest.Proposals[0].Prompt = "different"
	third, err := manageSessionsDeployDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("digest did not bind proposal")
	}
}

func TestParseApprovedManageSessionsDeployRejectsEmptySelection(t *testing.T) {
	_, err := parseApprovedManageSessionsDeploy(`{"action":"deploy","manifest_version":1,"manifest_digest":"digest","selected_proposal_ids":[],"proposals":[{"id":"proposal-1","prompt":"work","mode":"auto","agent_name":"swarm"}]}`)
	if err == nil || !strings.Contains(err.Error(), "at least one selected") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveManageSessionsDeployBindingPathUsesSourceWorkspaceFromManagedParent(t *testing.T) {
	parent := pebblestore.SessionSnapshot{WorkspacePath: "/managed/worktree", WorktreeEnabled: true, Metadata: map[string]any{"swarm_v3_source_workspace_path": "/bound/workspace"}}
	path, err := resolveManageSessionsDeployBindingPath(parent, manageSessionsDeployInput{Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bound/workspace" {
		t.Fatalf("path = %q, want source workspace", path)
	}
	if explicit, err := resolveManageSessionsDeployBindingPath(parent, manageSessionsDeployInput{WorkspacePath: "/explicit", Worktree: false}); err != nil || explicit != "/explicit" {
		t.Fatalf("explicit path = %q, err = %v", explicit, err)
	}
}

func TestResolveManageSessionsDeployBindingPathNamesMissingSourceField(t *testing.T) {
	_, err := resolveManageSessionsDeployBindingPath(pebblestore.SessionSnapshot{WorkspacePath: "/managed/worktree", WorktreeEnabled: true}, manageSessionsDeployInput{Worktree: false})
	if err == nil || !strings.Contains(err.Error(), "swarm_v3_source_workspace_path") {
		t.Fatalf("err = %v", err)
	}
}

func TestCanonicalDeployWorktreeBranchUsesDesktopStylePrefixAndTitle(t *testing.T) {
	if got := canonicalDeployWorktreeBranch("agent/<id>", canonicalDeployWorktreeBranchSuffix("AI suggested cards worktree", "session-1")); got != "agent/ai-suggested-cards-worktree" {
		t.Fatalf("suggested branch = %q", got)
	}
	if got := canonicalDeployWorktreeBranch("agent/<id>", canonicalDeployWorktreeBranchSuffix("Test: 2-checkpoint plan exit", "session-1")); got != "agent/test-2-checkpoint-plan-exit" {
		t.Fatalf("branch = %q", got)
	}
	if got := canonicalDeployWorktreeBranch("", canonicalDeployWorktreeBranchSuffix("", "session-2")); got != "agent/session-2" {
		t.Fatalf("fallback branch = %q", got)
	}
}

func TestSessionDeployCanonicalServicesAreRequired(t *testing.T) {
	_, err := (&Service{}).executeManageSessionsDeploy(context.Background(), "parent", tool.Call{}, `{}`, nil)
	if err == nil || !strings.Contains(err.Error(), "canonical V3 services") {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionDeployCreationMetadataMatchesDesktopV3AndPreservesLineage(t *testing.T) {
	metadata := sessionDeployCreationMetadata(" parent-session ", " /workspace/source ", " digest ", " proposal-1 ")
	want := map[string]string{
		"source":                     "desktop-v3",
		"workspace_path":             "/workspace/source",
		"parent_session_id":          "parent-session",
		"lineage_kind":               "session_deploy",
		"deployment_manifest_digest": "digest",
		"deployment_proposal_id":     "proposal-1",
	}
	for key, expected := range want {
		if got := mapString(metadata, key); got != expected {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, expected)
		}
	}
}

func TestSessionDeployCreationMetadataWithoutOriginIsRootSession(t *testing.T) {
	metadata := sessionDeployCreationMetadata("", "/workspace/source", "digest", "proposal-1")
	if mapString(metadata, "parent_session_id") != "" || mapString(metadata, "lineage_kind") != "" {
		t.Fatalf("sessionless deployment gained parent lineage: %#v", metadata)
	}
	if mapString(metadata, "workspace_path") != "/workspace/source" || mapString(metadata, "deployment_manifest_digest") != "digest" {
		t.Fatalf("sessionless deployment metadata = %#v", metadata)
	}
}

func TestSessionDeployRunIntentCarriesRunOwnership(t *testing.T) {
	intent := sessionDeployRunIntent(" session-1 ", " run-1 ", " parent-session ", " user ", " account ")
	if intent.SessionID != "session-1" || intent.RunID != "run-1" || intent.RunSessionID != "session-1" || intent.ParentSessionID != "parent-session" {
		t.Fatalf("run intent ownership = %#v", intent)
	}
	if intent.UserID != "user" || intent.AccountScopeID != "account" {
		t.Fatalf("run intent principal = %#v", intent)
	}
}

func TestSessionDeployEnqueuerReceivesCanonicalPendingRun(t *testing.T) {
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account"}
	var gotSessionID, gotRunID, gotParentSessionID string
	svc := &Service{sessionDeployEnqueue: func(got identity.Principal, sessionID, runID, parentSessionID string) bool {
		if got.UserID != principal.UserID || got.AccountScopeID != principal.AccountScopeID {
			t.Fatalf("principal = %#v", got)
		}
		gotSessionID, gotRunID, gotParentSessionID = sessionID, runID, parentSessionID
		return true
	}}
	if !svc.sessionDeployEnqueue(principal, "session-1", "run-1", "parent-session") {
		t.Fatal("canonical enqueue rejected")
	}
	if gotSessionID != "session-1" || gotRunID != "run-1" || gotParentSessionID != "parent-session" {
		t.Fatalf("enqueue = %q/%q parent=%q", gotSessionID, gotRunID, gotParentSessionID)
	}
}

func TestDeploySessionNavigationUsesActualSourceWorkspace(t *testing.T) {
	session := pebblestore.SessionSnapshot{ID: "session-1", WorkspacePath: "/data/worktrees/ws_fake", WorkspaceName: "ws_fake", Metadata: map[string]any{"swarm_v3_source_workspace_path": "/actual/workspace", "swarm_v3_source_workspace_name": "actual"}}
	navigation := deploySessionNavigation(session)
	if navigation["workspace_path"] != "/actual/workspace" || navigation["workspace_name"] != "actual" || navigation["href"] != "/actual/session-1" {
		t.Fatalf("navigation = %#v", navigation)
	}
}

func TestAITaskDeploymentDigestBindsModelProfileSnapshot(t *testing.T) {
	profile := &pebblestore.SessionModelProfileSnapshot{Source: pebblestore.SessionModelProfileSourceSaved, SavedProfileID: "profile-1", ModelMode: pebblestore.ModelProfileModeSplit, Plan: &pebblestore.ModelProfileSelection{Provider: "codex", Model: "plan"}, Auto: &pebblestore.ModelProfileSelection{Provider: "openai", Model: "auto"}}
	first, err := aiTaskDeploymentDigest("account", "/workspace", "task", profile)
	if err != nil {
		t.Fatal(err)
	}
	profile.Auto.Model = "different"
	second, err := aiTaskDeploymentDigest("account", "/workspace", "task", profile)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("AI task deployment digest did not bind model profile snapshot")
	}
}

func TestDeterministicDeployIDStableAndProposalBound(t *testing.T) {
	first := deterministicDeployID("digest", "proposal-1", "session")
	if first != deterministicDeployID("digest", "proposal-1", "session") {
		t.Fatal("deterministic deploy id changed")
	}
	if first == deterministicDeployID("digest", "proposal-2", "session") {
		t.Fatal("deterministic deploy id did not bind proposal")
	}
}

func TestPermissionRequirementAlwaysAsksForSessionDeploy(t *testing.T) {
	args := `{"action":"deploy","proposals":[{"prompt":"work"}]}`
	for _, mode := range []string{"plan", "auto", "auto+bypass_permissions"} {
		requirement, ask := permissionRequirement(mode, "manage-sessions", args)
		if requirement != "session_deploy" || !ask {
			t.Fatalf("mode %s = %q/%v", mode, requirement, ask)
		}
	}
}
