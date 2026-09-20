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

// Requirement: import must be classified as a write action (denied in read-only execution setting,
// allowed in auto mode), while discovery and reading are classified as read actions (allowed in read-only).
func TestArtifactImportAndDiscoveryPolicyClassification(t *testing.T) {
	writeActions := []string{"import", "create", "create_package", "revise_v3", "begin_v3", "author_v3", "publish_workspace", "materialize", "promote", "delete"}
	for _, action := range writeActions {
		args := `{"action":"` + action + `"}`
		autoGot := ExplainPolicy("auto", "manage_artifact", args, Policy{})
		if autoGot.Decision != PolicyDecisionAllow {
			t.Fatalf("write action %s in auto mode = %q, want allow", action, autoGot.Decision)
		}
		readGot := ExplainPolicy("read", "manage_artifact", args, Policy{})
		if readGot.Decision != PolicyDecisionDeny {
			t.Fatalf("write action %s in read mode = %q, want deny", action, readGot.Decision)
		}
	}

	readActions := []string{"list_v3", "source_v3", "read_v3", "draft_status_v3", "list", "search", "get", "read", "help"}
	for _, action := range readActions {
		args := `{"action":"` + action + `"}`
		autoGot := ExplainPolicy("auto", "manage_artifact", args, Policy{})
		if autoGot.Decision != PolicyDecisionAllow {
			t.Fatalf("read action %s in auto mode = %q, want allow", action, autoGot.Decision)
		}
		readGot := ExplainPolicy("read", "manage_artifact", args, Policy{})
		if readGot.Decision != PolicyDecisionAllow {
			t.Fatalf("read action %s in read mode = %q, want allow", action, readGot.Decision)
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
