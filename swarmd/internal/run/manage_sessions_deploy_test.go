package run

import (
	"errors"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
	if len(parsed) != 1 || parsed[0].Mode != "auto" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestValidateManageSessionsDeployAgentDefaultsAndGating(t *testing.T) {
	active := pebblestore.AgentProfile{Name: "swarm", Mode: agentruntime.ModePrimary, Enabled: true}
	if err := validateManageSessionsDeployAgent(active, active, false); err != nil {
		t.Fatalf("active primary rejected without delegation: %v", err)
	}
	alternate := pebblestore.AgentProfile{Name: "explorer", Mode: agentruntime.ModeSubagent, Enabled: true}
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

func TestResolveManageSessionsDeployBindingPathUsesSourceWorkspaceForNonWorktreeDeploy(t *testing.T) {
	parent := pebblestore.SessionSnapshot{WorkspacePath: "/managed/worktree", WorktreeEnabled: true, Metadata: map[string]any{"swarm_v3_source_workspace_path": "/bound/workspace"}}
	path, err := resolveManageSessionsDeployBindingPath(parent, manageSessionsDeployInput{Worktree: false})
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
	if got := canonicalDeployWorktreeBranch("agent/<id>", canonicalDeployWorktreeBranchSuffix("Test: 2-checkpoint plan exit", "session-1")); got != "agent/test-2-checkpoint-plan-exit" {
		t.Fatalf("branch = %q", got)
	}
	if got := canonicalDeployWorktreeBranch("", canonicalDeployWorktreeBranchSuffix("", "session-2")); got != "agent/session-2" {
		t.Fatalf("fallback branch = %q", got)
	}
}

func TestDeploySessionNavigationUsesActualSourceWorkspace(t *testing.T) {
	session := pebblestore.SessionSnapshot{ID: "session-1", WorkspacePath: "/data/worktrees/ws_fake", WorkspaceName: "ws_fake", Metadata: map[string]any{"swarm_v3_source_workspace_path": "/actual/workspace", "swarm_v3_source_workspace_name": "actual"}}
	navigation := deploySessionNavigation(session)
	if navigation["workspace_path"] != "/actual/workspace" || navigation["workspace_name"] != "actual" || navigation["href"] != "/actual/session-1" {
		t.Fatalf("navigation = %#v", navigation)
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

func TestRecordManageSessionsDeployRunFailurePersistsMessageAndFailedIntent(t *testing.T) {
	var mutations []sessionruntime.SessionMutationInput
	apply := func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
		mutations = append(mutations, input)
		return sessionruntime.SessionMutationResult{}, nil
	}
	principal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: "user", AccountScopeID: "account", SessionID: "session"}
	(&Service{}).recordManageSessionsDeployRunFailure(pebblestore.SessionSnapshot{ID: "session"}, "run-1", principal, errors.New("startup exploded"), apply)
	if len(mutations) != 2 {
		t.Fatalf("mutations = %d, want failure message and run status", len(mutations))
	}
	if mutations[0].Kind != sessionruntime.SessionMutationAppendMessage || mutations[0].Message == nil || !strings.Contains(mutations[0].Message.Content, "startup exploded") {
		t.Fatalf("failure message mutation = %#v", mutations[0])
	}
	if mutations[1].Kind != sessionruntime.SessionMutationRecordRunIntent || mutations[1].RunIntent == nil || mutations[1].RunIntent.Status != sessionruntime.RunIntentFailed || mutations[1].RunIntent.BlockedReason != "startup exploded" {
		t.Fatalf("failure intent mutation = %#v", mutations[1])
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
