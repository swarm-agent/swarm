package permission

import (
	"encoding/json"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func bashArguments(t *testing.T, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal bash arguments: %v", err)
	}
	return string(payload)
}

func TestManageWorktreeIntegrateRequiresOnePermissionDecision(t *testing.T) {
	policy := NormalizePolicy(Policy{})
	integrate := ExplainPolicy("auto", "manage_worktree", `{"action":"integrate","session_ids":["child-a","child-b"],"expected_parent_head":"abc"}`, policy)
	if integrate.Decision != PolicyDecisionAsk {
		t.Fatalf("integrate decision = %s, want ask", integrate.Decision)
	}
	recall := ExplainPolicy("auto", "manage_worktree", `{"action":"recall"}`, policy)
	if recall.Decision != PolicyDecisionAllow {
		t.Fatalf("recall decision = %s, want allow", recall.Decision)
	}
}

func TestBashPrefixMatchesRelativeScriptPaths(t *testing.T) {
	policy := NormalizePolicy(Policy{Rules: []PolicyRule{{
		Kind:     PolicyRuleKindBashPrefix,
		Decision: PolicyDecisionAllow,
		Pattern:  "run-tests.sh",
	}}})

	cases := []string{
		"run-tests.sh",
		"./run-tests.sh",
		"scripts/run-tests.sh --fast",
		"./scripts/run-tests.sh --fast",
		"../scripts/run-tests.sh --fast",
	}

	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			explain := ExplainPolicy("auto", "bash", bashArguments(t, command), policy)
			if explain.Decision != PolicyDecisionAllow {
				t.Fatalf("expected allow for %q, got %s (%s)", command, explain.Decision, explain.Reason)
			}
			if explain.Source != "rule" {
				t.Fatalf("expected rule source for %q, got %q", command, explain.Source)
			}
		})
	}
}

func TestBashPrefixMatchesShellWrappedScripts(t *testing.T) {
	policy := NormalizePolicy(Policy{Rules: []PolicyRule{{
		Kind:     PolicyRuleKindBashPrefix,
		Decision: PolicyDecisionAllow,
		Pattern:  "run-tests.sh",
	}}})

	cases := []string{
		"bash run-tests.sh",
		"bash ./run-tests.sh",
		"bash scripts/run-tests.sh --fast",
		"sh ./scripts/run-tests.sh --fast",
		"zsh ../scripts/run-tests.sh --fast",
		"dash scripts/run-tests.sh --fast",
		"ksh scripts/run-tests.sh --fast",
		"/bin/bash ./scripts/run-tests.sh --fast",
		"/usr/bin/env bash ./scripts/run-tests.sh --fast",
		"sudo bash ./scripts/run-tests.sh --fast",
		"command sh ./scripts/run-tests.sh --fast",
		"VAR=value bash ./scripts/run-tests.sh --fast",
		"bash -e ./scripts/run-tests.sh --fast",
		"bash --noprofile --norc ./scripts/run-tests.sh --fast",
	}

	for _, command := range cases {
		t.Run(command, func(t *testing.T) {
			explain := ExplainPolicy("auto", "bash", bashArguments(t, command), policy)
			if explain.Decision != PolicyDecisionAllow {
				t.Fatalf("expected allow for %q, got %s (%s)", command, explain.Decision, explain.Reason)
			}
			if explain.Source != "rule" {
				t.Fatalf("expected rule source for %q, got %q", command, explain.Source)
			}
		})
	}
}

func TestBashPrefixDoesNotTreatShellInterpreterAsScript(t *testing.T) {
	policy := NormalizePolicy(Policy{Rules: []PolicyRule{{
		Kind:     PolicyRuleKindBashPrefix,
		Decision: PolicyDecisionAllow,
		Pattern:  "bash",
	}}})

	explain := ExplainPolicy("auto", "bash", bashArguments(t, "bash ./scripts/run-tests.sh"), policy)
	if explain.Decision == PolicyDecisionAllow && explain.Source == "rule" {
		t.Fatalf("expected shell wrapper not to match bash prefix directly")
	}
}

func TestResolveWithPolicyAndArgumentsPersistsRuleForSessionAccount(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-policy-account.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	sessionSvc := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	session, _, err := sessionSvc.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:      "session-account-rule",
		Title:          "Account Rule",
		WorkspacePath:  "/workspace",
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		UserID:         "user-a",
		AccountScopeID: "account-a",
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc.SetSessionResolver(sessionSvc)
	record, err := svc.CreatePending(CreateInput{
		SessionID:     session.ID,
		RunID:         "run-1",
		CallID:        "call-1",
		ToolName:      "bash",
		ToolArguments: bashArguments(t, "git status"),
		Requirement:   "tool",
		Mode:          sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	resolved, savedRule, err := svc.ResolveWithPolicyAndArguments(session.ID, record.ID, ActionAllowAlways, "ok", "")
	if err != nil {
		t.Fatalf("resolve with policy: %v", err)
	}
	if resolved.Status != pebblestore.PermissionStatusApproved {
		t.Fatalf("resolved status = %q, want approved", resolved.Status)
	}
	if savedRule == nil {
		t.Fatal("saved rule is nil")
	}

	accountPolicy, err := svc.CurrentPolicyForAccount("account-a")
	if err != nil {
		t.Fatalf("current account policy: %v", err)
	}
	foundSavedRule := false
	for _, rule := range accountPolicy.Rules {
		if rule.ID == savedRule.ID {
			foundSavedRule = true
			break
		}
	}
	if !foundSavedRule {
		t.Fatalf("account policy rules = %+v, want saved rule %s", accountPolicy.Rules, savedRule.ID)
	}
	globalPolicy, err := svc.CurrentPolicy()
	if err != nil {
		t.Fatalf("current global policy: %v", err)
	}
	for _, rule := range globalPolicy.Rules {
		if rule.ID == savedRule.ID {
			t.Fatalf("global policy rules = %+v, want no leaked account rule %s", globalPolicy.Rules, savedRule.ID)
		}
	}

	auth, err := svc.AuthorizeToolCall(AuthorizationInput{
		SessionID:      session.ID,
		AccountScopeID: "account-a",
		RunID:          "run-2",
		CallID:         "call-2",
		ToolName:       "bash",
		ToolArguments:  bashArguments(t, "git status --short"),
		Mode:           sessionruntime.ModeAuto,
	})
	if err != nil {
		t.Fatalf("authorize tool call: %v", err)
	}
	if auth.Decision != AuthorizationApprove || auth.Source != "rule" {
		t.Fatalf("authorization = %+v, want account-scoped rule approval", auth)
	}
}
