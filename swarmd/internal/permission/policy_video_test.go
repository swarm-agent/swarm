package permission

import (
	"fmt"
	"testing"

	"swarm/packages/swarmd/internal/tool"
)

// Requirement: manage_video must not fall through to the unknown-tool approval
// default before chat is upgraded to Studio. ExplainPolicy is the shared gate;
// enumerate the production action registry rather than a drifting discovery list.
// This proves approval decisions, not source access or human render authority.
func TestManageVideoDefaultsToAutomaticSessionAuthority(t *testing.T) {
	for _, mode := range []string{"auto", "plan", "read", "readwrite"} {
		for _, name := range []string{"manage_video", "manage-video"} {
			for _, action := range tool.ManageVideoActionNames(false) {
				t.Run(mode+"/"+name+"/"+action, func(t *testing.T) {
					got := ExplainPolicy(mode, name, fmt.Sprintf(`{"action":%q}`, action), DefaultPolicy())
					if got.Decision != PolicyDecisionAllow {
						t.Fatalf("decision=%s source=%s reason=%s", got.Decision, got.Source, got.Reason)
					}
				})
			}
		}
	}
}

// Removing a default prompt must not bypass a deliberate account restriction or
// relax unrelated billed-image approval. Runtime rejects invalid video actions.
func TestManageVideoDefaultPreservesExplicitRestrictions(t *testing.T) {
	for _, decision := range []PolicyDecision{PolicyDecisionDeny, PolicyDecisionAsk} {
		policy := NormalizePolicy(Policy{Version: 1, Rules: []PolicyRule{{Kind: PolicyRuleKindTool, Decision: decision, Tool: "manage_video"}}})
		got := ExplainPolicy("auto", "manage_video", `{"action":"list_source_roots"}`, policy)
		if got.Decision != decision {
			t.Fatalf("explicit %s became %s", decision, got.Decision)
		}
	}
	if got := ExplainPolicy("auto", "manage_artifact", `{"action":"generate_image"}`, DefaultPolicy()); got.Decision != PolicyDecisionAsk {
		t.Fatalf("image approval changed: %s", got.Decision)
	}
}
