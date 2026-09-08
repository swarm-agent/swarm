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
