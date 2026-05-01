package permission

import (
	"encoding/json"
	"testing"
)

func bashArguments(t *testing.T, command string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal bash arguments: %v", err)
	}
	return string(payload)
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
