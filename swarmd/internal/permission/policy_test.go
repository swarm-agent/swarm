package permission

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func bashArguments(t *testing.T, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"command": command, "explanation": []string{"Focused policy test."}, "category": "read", "critical": false})
	if err != nil {
		t.Fatalf("marshal bash arguments: %v", err)
	}
	return string(payload)
}

func bashEffectArguments(t *testing.T, command, category string, critical bool) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"command": command, "explanation": []string{"Focused policy test."}, "category": category, "critical": critical,
	})
	if err != nil {
		t.Fatalf("marshal bash effect arguments: %v", err)
	}
	return string(payload)
}

func TestBashApprovalProfileMatrix(t *testing.T) {
	cases := []struct {
		name     string
		profile  BashApprovalProfile
		category string
		critical bool
		want     PolicyDecision
	}{
		{"current rules noncritical read", BashApprovalProfileCurrentRules, "read", false, PolicyDecisionAsk},
		{"current rules critical read", BashApprovalProfileCurrentRules, "read", true, PolicyDecisionAsk},
		{"current rules noncritical write", BashApprovalProfileCurrentRules, "write", false, PolicyDecisionAsk},
		{"current rules noncritical update", BashApprovalProfileCurrentRules, "update", false, PolicyDecisionAsk},
		{"current rules critical write", BashApprovalProfileCurrentRules, "write", true, PolicyDecisionAsk},
		{"current rules critical update", BashApprovalProfileCurrentRules, "update", true, PolicyDecisionAsk},
		{"current rules delete", BashApprovalProfileCurrentRules, "delete", true, PolicyDecisionAsk},
		{"allow every read safe read", BashApprovalProfileAllowEveryRead, "read", false, PolicyDecisionAllow},
		{"allow every read critical read", BashApprovalProfileAllowEveryRead, "read", true, PolicyDecisionAllow},
		{"allow every read write fallback", BashApprovalProfileAllowEveryRead, "write", false, PolicyDecisionAsk},
		{"allow every read update fallback", BashApprovalProfileAllowEveryRead, "update", false, PolicyDecisionAsk},
		{"allow every read critical write", BashApprovalProfileAllowEveryRead, "write", true, PolicyDecisionAsk},
		{"allow every read critical update", BashApprovalProfileAllowEveryRead, "update", true, PolicyDecisionAsk},
		{"allow every read delete", BashApprovalProfileAllowEveryRead, "delete", true, PolicyDecisionAsk},
		{"only critical safe read", BashApprovalProfileOnlyCriticalPrompts, "read", false, PolicyDecisionAllow},
		{"only critical critical read", BashApprovalProfileOnlyCriticalPrompts, "read", true, PolicyDecisionAsk},
		{"only critical safe write", BashApprovalProfileOnlyCriticalPrompts, "write", false, PolicyDecisionAllow},
		{"only critical safe update", BashApprovalProfileOnlyCriticalPrompts, "update", false, PolicyDecisionAllow},
		{"only critical critical write", BashApprovalProfileOnlyCriticalPrompts, "write", true, PolicyDecisionAsk},
		{"only critical critical update", BashApprovalProfileOnlyCriticalPrompts, "update", true, PolicyDecisionAsk},
		{"only critical delete", BashApprovalProfileOnlyCriticalPrompts, "delete", true, PolicyDecisionAsk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultPolicy()
			policy.BashProfile = tc.profile
			got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "printf hello", tc.category, tc.critical), policy)
			if got.Decision != tc.want {
				t.Fatalf("decision = %s (%s), want %s", got.Decision, got.Reason, tc.want)
			}
			if tc.profile != BashApprovalProfileCurrentRules && got.RulePreview != "allow bash prefix: printf" {
				t.Fatalf("rule preview = %q, want the always-allow prefix", got.RulePreview)
			}
		})
	}
}

func TestBashApprovalProfilePrecedenceAndMetadataValidation(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts
	policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindBashPrefix, Decision: PolicyDecisionAllow, Pattern: "printf"})

	critical := ExplainPolicy("auto", "bash", bashEffectArguments(t, "printf hello", "write", true), policy)
	if critical.Decision != PolicyDecisionAsk || critical.Source != "bash_profile" {
		t.Fatalf("critical allow-rule result = %+v, want profile prompt", critical)
	}
	policy.Rules = append(policy.Rules, PolicyRule{Kind: PolicyRuleKindBashPrefix, Decision: PolicyDecisionDeny, Pattern: "printf"})
	denied := ExplainPolicy("auto", "bash", bashEffectArguments(t, "printf hello", "read", false), policy)
	if denied.Decision != PolicyDecisionDeny || denied.Source != "rule" {
		t.Fatalf("explicit deny result = %+v, want deny", denied)
	}

	policy.Rules = nil
	for name, arguments := range map[string]string{
		"missing metadata":            `{"command":"git status"}`,
		"delete not critical":         bashEffectArguments(t, "rm old.log", "delete", false),
		"read mutation contradiction": bashEffectArguments(t, "touch generated.txt", "read", false),
	} {
		t.Run(name, func(t *testing.T) {
			got := ExplainPolicy("auto", "bash", arguments, policy)
			if got.Decision != PolicyDecisionAsk {
				t.Fatalf("decision = %s (%s), want ask", got.Decision, got.Reason)
			}
		})
	}
}

func TestBashApprovalProfilesKeepDangerousDeniesAndDedicatedCapabilitiesIsolated(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts
	if got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "rm -rf /", "delete", true), policy); got.Decision != PolicyDecisionDeny || got.Source != "builtin" {
		t.Fatalf("dangerous delete result = %+v, want builtin deny", got)
	}
	deployArgs := `{"action":"deploy","proposals":[{"prompt":"work"}]}`
	if got := ExplainPolicy("auto", "manage_sessions", deployArgs, policy); got.Decision != PolicyDecisionAsk || got.Source != "session_deploy_policy" {
		t.Fatalf("dedicated capability result = %+v, want capability ask", got)
	}
}

func TestBashMisdeclaredOutputRedirectUsesEffectiveWritePolicy(t *testing.T) {
	arguments := bashEffectArguments(t, `printf '%s\n' report >"$TMPDIR/report.txt"`, "read", false)

	allowWrites := DefaultPolicy()
	allowWrites.BashProfile = BashApprovalProfileOnlyCriticalPrompts
	got := ExplainPolicy("auto", "bash", arguments, allowWrites)
	if got.Decision != PolicyDecisionAllow || got.Source != "bash_profile" {
		t.Fatalf("noncritical redirect result = %+v, want profile allow", got)
	}
	if got.BashEffect == nil || !got.BashEffect.Valid || got.BashEffect.DeclaredCategory != BashEffectRead || got.BashEffect.Category != BashEffectWrite || got.BashEffect.Critical || !got.BashEffect.Promoted {
		t.Fatalf("noncritical redirect effect = %+v, want declared read corrected to effective noncritical write", got.BashEffect)
	}
	if !strings.Contains(got.BashEffect.Reason, `output redirect >"$tmpdir/report.txt"`) {
		t.Fatalf("noncritical redirect reason = %q, want actionable redirect evidence", got.BashEffect.Reason)
	}

	readOnly := DefaultPolicy()
	readOnly.BashProfile = BashApprovalProfileAllowEveryRead
	got = ExplainPolicy("auto", "bash", arguments, readOnly)
	if got.Decision != PolicyDecisionAsk {
		t.Fatalf("redirect under %s = %+v, want prompt", readOnly.BashProfile, got)
	}
	if got.BashEffect == nil || !got.BashEffect.Valid || got.BashEffect.Category != BashEffectWrite {
		t.Fatalf("redirect effect under %s = %+v, want effective write", readOnly.BashProfile, got.BashEffect)
	}
}

func TestBashOutputRedirectDetectionIgnoresNonFileRedirection(t *testing.T) {
	for _, command := range []string{`printf "a > b"`, `check 2>/dev/null`, `check 2>&1`} {
		t.Run(command, func(t *testing.T) {
			assessment := assessBashEffect(bashEffectArguments(t, command, "read", false), command)
			if !assessment.Valid || assessment.Category != BashEffectRead || assessment.Promoted {
				t.Fatalf("assessment = %+v, want unchanged read", assessment)
			}
		})
	}
}

func TestBashEffectBackendPromotion(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts

	deleted := ExplainPolicy("auto", "bash", bashEffectArguments(t, "rm old.log", "update", false), policy)
	if deleted.Decision != PolicyDecisionAsk || deleted.Source != "bash_profile" {
		t.Fatalf("promoted delete = %+v, want profile prompt", deleted)
	}
	criticalRead := ExplainPolicy("auto", "bash", bashEffectArguments(t, "cat .env", "read", false), policy)
	if criticalRead.Decision != PolicyDecisionAsk || criticalRead.Source != "bash_profile" {
		t.Fatalf("promoted critical read = %+v, want profile prompt", criticalRead)
	}
	allowEvery := policy
	allowEvery.BashProfile = BashApprovalProfileAllowEveryRead
	criticalRead = ExplainPolicy("auto", "bash", bashEffectArguments(t, "cat .env", "read", false), allowEvery)
	if criticalRead.Decision != PolicyDecisionAllow || criticalRead.Source != "bash_profile" {
		t.Fatalf("allow-every promoted critical read = %+v, want profile allow", criticalRead)
	}
}

func TestAllowEveryReadApprovesRoutineBashReads(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileAllowEveryRead
	for _, command := range []string{"cat README.md", "ls -la", "grep -R TODO src", "git status --short", "tail -n 50 app.log"} {
		t.Run(command, func(t *testing.T) {
			got := ExplainPolicy("auto", "bash", bashEffectArguments(t, command, "read", false), policy)
			if got.Decision != PolicyDecisionAllow || got.Source != "bash_profile" || got.BashEffect == nil || got.BashEffect.Critical {
				t.Fatalf("routine read result = %+v, want noncritical profile allow", got)
			}
		})
	}
}

func TestAllowEveryReadAcceptsReadOnlySystemctlInspection(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileAllowEveryRead
	command := `systemctl is-active prometheus-node-exporter.service 2>/dev/null || true; systemctl is-enabled prometheus-node-exporter.service 2>/dev/null || true; dpkg-query -W -f='${Status} ${Version}\n' prometheus-node-exporter 2>/dev/null || true; ss -ltn '( sport = :9100 )'`

	got := ExplainPolicy("auto", "bash", bashEffectArguments(t, command, "read", false), policy)
	if got.Decision != PolicyDecisionAllow || got.Source != "bash_profile" {
		t.Fatalf("read-only service inspection = %+v, want profile allow", got)
	}
	if got.BashEffect == nil || !got.BashEffect.Valid || got.BashEffect.Category != BashEffectRead || got.BashEffect.Critical {
		t.Fatalf("read-only service inspection effect = %+v, want valid noncritical read", got.BashEffect)
	}
}

func TestMutatingSystemctlCommandsContradictReadMetadata(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileAllowEveryRead
	for _, command := range []string{
		"systemctl start prometheus-node-exporter.service",
		"sudo systemctl restart prometheus-node-exporter.service",
		"systemctl --system enable --now prometheus-node-exporter.service",
		"systemctl daemon-reload",
		"systemctl status prometheus-node-exporter.service; systemctl stop prometheus-node-exporter.service",
	} {
		t.Run(command, func(t *testing.T) {
			got := ExplainPolicy("auto", "bash", bashEffectArguments(t, command, "read", false), policy)
			if got.Decision != PolicyDecisionAsk || got.Source != "bash_profile" {
				t.Fatalf("mutating systemctl result = %+v, want profile prompt", got)
			}
			if got.BashEffect == nil || got.BashEffect.Valid || got.BashEffect.Reason != "read category contradicts a mutating command" {
				t.Fatalf("mutating systemctl effect = %+v, want read contradiction", got.BashEffect)
			}
		})
	}
}

func TestEnumeratedCriticalBashReadsArePromoted(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts
	for _, command := range []string{"cat .env", "cat /etc/shadow", "pg_dump production", "curl https://example.test/private"} {
		t.Run(command, func(t *testing.T) {
			got := ExplainPolicy("auto", "bash", bashEffectArguments(t, command, "read", false), policy)
			if got.Decision != PolicyDecisionAsk || got.BashEffect == nil || !got.BashEffect.Critical || !got.BashEffect.Promoted {
				t.Fatalf("critical read result = %+v, want promoted prompt", got)
			}
		})
	}
}

func TestNormalizePolicyDefaultsAndValidatesBashProfile(t *testing.T) {
	if got := NormalizePolicy(Policy{}).BashProfile; got != BashApprovalProfileCurrentRules {
		t.Fatalf("missing bash profile = %q, want current rules", got)
	}
	if got := NormalizePolicy(Policy{BashProfile: "bogus"}).BashProfile; got != BashApprovalProfileCurrentRules {
		t.Fatalf("invalid bash profile = %q, want current rules", got)
	}
	if got := NormalizePolicy(Policy{BashProfile: "allow_safe_reads"}).BashProfile; got != BashApprovalProfileCurrentRules {
		t.Fatalf("legacy safe-reads profile = %q, want current rules", got)
	}
}

func TestBashProfilePreservesHardRuntimeRestrictionsAndBypassIsolation(t *testing.T) {
	policy := DefaultPolicy()
	policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts
	arguments := bashEffectArguments(t, "printf hello", "read", false)
	if got := ExplainPolicy("read", "bash", arguments, policy); got.Decision != PolicyDecisionDeny || got.Source != "builtin" {
		t.Fatalf("read execution restriction = %+v, want builtin deny", got)
	}
	if got := ExplainPolicy("auto+bypass_permissions", "bash", arguments, policy); got.Decision != PolicyDecisionAllow {
		t.Fatalf("permissions bypass result = %+v, want allow", got)
	}
}

func TestBashProfileGranularRulePrecedence(t *testing.T) {
	t.Run("deny overrides profile auto approval", func(t *testing.T) {
		policy := DefaultPolicy()
		policy.BashProfile = BashApprovalProfileAllowEveryRead
		policy.Rules = []PolicyRule{{Kind: PolicyRuleKindBashPrefix, Decision: PolicyDecisionDeny, Pattern: "cat"}}
		got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "cat README.md", "read", false), policy)
		if got.Decision != PolicyDecisionDeny || got.Source != "rule" {
			t.Fatalf("deny rule result = %+v, want rule deny", got)
		}
	})

	t.Run("profile safety prompt overrides allow rule", func(t *testing.T) {
		policy := DefaultPolicy()
		policy.BashProfile = BashApprovalProfileOnlyCriticalPrompts
		policy.Rules = []PolicyRule{{Kind: PolicyRuleKindBashPrefix, Decision: PolicyDecisionAllow, Pattern: "printf"}}
		got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "printf hello", "write", true), policy)
		if got.Decision != PolicyDecisionAsk || got.Source != "bash_profile" {
			t.Fatalf("critical allow-rule result = %+v, want profile prompt", got)
		}
	})

	t.Run("profile auto approval precedes ask rule", func(t *testing.T) {
		policy := DefaultPolicy()
		policy.BashProfile = BashApprovalProfileAllowEveryRead
		policy.Rules = []PolicyRule{{Kind: PolicyRuleKindBashPrefix, Decision: PolicyDecisionAsk, Pattern: "cat"}}
		got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "cat README.md", "read", false), policy)
		if got.Decision != PolicyDecisionAllow || got.Source != "bash_profile" {
			t.Fatalf("safe-read ask-rule result = %+v, want profile allow", got)
		}
	})

	for _, decision := range []PolicyDecision{PolicyDecisionAllow, PolicyDecisionAsk} {
		t.Run("fallback "+string(decision)+" rule applies", func(t *testing.T) {
			policy := DefaultPolicy()
			policy.BashProfile = BashApprovalProfileAllowEveryRead
			policy.Rules = []PolicyRule{{Kind: PolicyRuleKindBashPrefix, Decision: decision, Pattern: "touch"}}
			got := ExplainPolicy("auto", "bash", bashEffectArguments(t, "touch generated.txt", "write", false), policy)
			if got.Decision != decision || got.Source != "rule" {
				t.Fatalf("fallback rule result = %+v, want rule %s", got, decision)
			}
		})
	}
}

func TestManageWorktreeIntegrateFlowsWithoutPermissionRoundTrip(t *testing.T) {
	policy := NormalizePolicy(Policy{})
	integrate := ExplainPolicy("auto", "manage_worktree", `{"action":"integrate","session_ids":["child-a","child-b"]}`, policy)
	if integrate.Decision != PolicyDecisionAllow {
		t.Fatalf("integrate decision = %s, want allow", integrate.Decision)
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

func TestUpdateBashApprovalProfilePersistsPerAccountAndPreservesCapabilities(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "permission-bash-profile.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	svc := NewService(pebblestore.NewPermissionStore(store), events, nil)
	before, err := svc.CurrentPolicyForAccount("account-profile")
	if err != nil {
		t.Fatalf("current policy: %v", err)
	}
	updated, err := svc.UpdateBashApprovalProfileForAccount("account-profile", BashApprovalProfileAllowEveryRead)
	if err != nil {
		t.Fatalf("update bash profile: %v", err)
	}
	if updated.BashProfile != BashApprovalProfileAllowEveryRead {
		t.Fatalf("updated profile = %q", updated.BashProfile)
	}
	if !reflect.DeepEqual(updated.Subagents, before.Subagents) || !reflect.DeepEqual(updated.SessionDeploy, before.SessionDeploy) || !reflect.DeepEqual(updated.PlanAcceptance, before.PlanAcceptance) {
		t.Fatalf("unrelated capability policy changed: before=%+v after=%+v", before, updated)
	}
	reloaded, err := svc.CurrentPolicyForAccount("account-profile")
	if err != nil {
		t.Fatalf("reload policy: %v", err)
	}
	if reloaded.BashProfile != BashApprovalProfileAllowEveryRead {
		t.Fatalf("reloaded profile = %q", reloaded.BashProfile)
	}
	other, err := svc.CurrentPolicyForAccount("other-account")
	if err != nil {
		t.Fatalf("other account policy: %v", err)
	}
	if other.BashProfile != BashApprovalProfileCurrentRules {
		t.Fatalf("other account profile = %q, want current rules", other.BashProfile)
	}
	if _, err := svc.UpdateBashApprovalProfileForAccount("account-profile", "invalid"); err == nil {
		t.Fatal("invalid profile update succeeded")
	}
	if _, err := svc.UpdateBashApprovalProfileForAccount("account-profile", "allow_safe_reads"); err == nil {
		t.Fatal("legacy safe-reads profile update succeeded")
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
