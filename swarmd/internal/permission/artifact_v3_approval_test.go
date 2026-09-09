package permission

import "testing"

// Requirement: native head selection and retained producer rebinding need an
// explicit approval, distinct from safe discovery. Threat: generic artifact
// allow rules or bypass silently authorize selection/recovery. ExplainPolicy and
// authorizationRequirement are the narrow policy/identity boundaries.
func TestArtifactV3ExactActionApproval(t *testing.T) {
	for action, identity := range map[string]string{"select_v3": "artifact_v3_select", "resume_v3": "artifact_v3_resume"} {
		args := `{"action":"` + action + `"}`
		for _, mode := range []string{"auto", "auto:bypass"} {
			got := ExplainPolicy(mode, "manage_artifact", args, Policy{})
			if got.Decision != PolicyDecisionAsk || got.ToolName != identity {
				t.Fatalf("%s %s: %+v", action, mode, got)
			}
			if got := authorizationRequirement(mode, "manage_artifact", args); got != identity {
				t.Fatalf("identity=%s", got)
			}
		}
	}
	for _, action := range []string{"list_v3", "source_v3", "draft_status_v3", "read_v3"} {
		got := ExplainPolicy("auto", "manage_artifact", `{"action":"`+action+`"}`, Policy{})
		if got.Decision != PolicyDecisionAllow {
			t.Fatalf("read %s: %+v", action, got)
		}
	}
}

// Requirement: an operator's exact native capability rule must resolve automation
// without granting another artifact action. Threat: wildcard/generic rules leak
// consent, or an allow masks denial. The policy evaluator is the narrow boundary.
func TestArtifactV3ExactActionRules(t *testing.T) {
	for action, identity := range map[string]string{"select_v3": "artifact_v3_select", "resume_v3": "artifact_v3_resume"} {
		args := `{"action":"` + action + `"}`
		for _, mode := range []string{"auto", "auto:bypass"} {
			for _, tool := range []string{"manage_artifact", "*", "artifact_v3_other"} {
				policy := Policy{Rules: []PolicyRule{{ID: "generic", Kind: PolicyRuleKindTool, Tool: tool, Decision: PolicyDecisionAllow}}}
				if got := ExplainPolicy(mode, "manage_artifact", args, policy); got.Decision != PolicyDecisionAsk {
					t.Fatalf("generic %s authorized %s: %+v", tool, action, got)
				}
			}
			policy := Policy{Rules: []PolicyRule{{ID: "exact", Kind: PolicyRuleKindTool, Tool: identity, Decision: PolicyDecisionAllow}}}
			if got := ExplainPolicy(mode, "manage_artifact", args, policy); got.Decision != PolicyDecisionAllow || got.ToolName != identity {
				t.Fatalf("exact rule ignored: %+v", got)
			}
			policy.Rules = append(policy.Rules, PolicyRule{ID: "deny", Kind: PolicyRuleKindTool, Tool: identity, Decision: PolicyDecisionDeny})
			if got := ExplainPolicy(mode, "manage_artifact", args, policy); got.Decision != PolicyDecisionDeny {
				t.Fatalf("exact denial ignored: %+v", got)
			}
		}
	}
}
