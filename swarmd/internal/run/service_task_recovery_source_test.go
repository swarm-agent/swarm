package run

import (
	"encoding/json"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: parsing and durable program conversion must retain the exact recovery
// reference; unsupported agents and whole-worktree scopes must fail before
// allocation. This unit boundary catches dropped/implicitly widened contracts.
func TestRecoverySourceLaunchContract(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, agent := range []string{"coder", "finder", "designer"} {
		args := map[string]any{"prompt": "Recover unfinished source", "subagent_type": agent, "meta_prompt": "Complete source.go only", "owned_scope": []string{"source.go"}, "recovery_source_digest": digest}
		parsed, err := parseTaskCallArguments(mustJSON(t, args))
		if agent != "coder" {
			if err == nil {
				t.Fatal("non-Coder accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(parsed.Launches) != 1 || parsed.Launches[0].RecoverySourceDigest != digest {
			t.Fatal("digest lost")
		}
	}
	for _, scopes := range [][]string{nil, {"**"}, {"../escape"}} {
		if validateRecoveryLaunch(digest, "coder", scopes) == nil {
			t.Fatal("wide scope accepted")
		}
	}
	if validateRecoveryLaunch("bad", "coder", []string{"source.go"}) == nil {
		t.Fatal("invalid digest accepted")
	}
	args := map[string]any{"action": "start", "prompt": "Complete only unfinished job", "program": map[string]any{"id": "replacement", "stages": []any{map[string]any{"id": "build", "dependency_evidence": "ready"}}, "jobs": []any{map[string]any{"id": "unfinished", "stage_id": "build", "agent_type": "coder", "title": "Recover source", "meta_prompt": "Complete source.go only", "deliverable": "committed source", "owned_scope": []string{"source.go"}, "recovery_source_digest": digest, "acceptance_criteria": []string{"complete"}, "dependency_evidence": "retained source"}}}}
	parsed, err := parseTaskCallArguments(mustJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Program.Jobs) != 1 || parsed.Program.Jobs[0].RecoverySourceDigest != digest || parsed.Launches[0].RecoverySourceDigest != digest {
		t.Fatal("program lost exact unfinished job reference")
	}
	definition, _, err := taskProgramDefinitionFromSpec(parsed.Program)
	if err != nil {
		t.Fatal(err)
	}
	var restored pebblestore.TaskProgramDefinition
	if err := json.Unmarshal([]byte(mustJSON(t, definition)), &restored); err != nil || len(restored.Jobs) != 1 || restored.Jobs[0].RecoverySourceDigest != digest {
		t.Fatalf("durable definition lost recovery reference: %+v %v", restored, err)
	}
}

// Purpose: parseApprovedTaskLaunchManifest must reject digest substitution or
// removal after approval, before any allocation. A valid signed envelope and
// changed requested spec exercise the narrowest permission replay boundary.
func TestRecoverySourceApprovalBinding(t *testing.T) {
	digest := strings.Repeat("a", 64)
	spec := taskLaunchSpec{RequestedSubagentType: "coder", RecoverySourceDigest: digest, OwnedScope: []string{"source.go"}}
	manifest := taskLaunchManifest{Launches: []taskLaunchManifestRow{{RequestedSubagentType: "coder", RecoverySourceDigest: digest, OwnedScope: []string{"source.go"}, ProfileSnapshot: &pebblestore.AgentProfile{}}}}
	hash, err := taskLaunchManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	body := mustJSON(t, map[string]any{"manifest_hash": hash, "manifest": manifest})
	if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{spec}); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{"", strings.Repeat("b", 64)} {
		spec.RecoverySourceDigest = changed
		if _, err := parseApprovedTaskLaunchManifest(body, []taskLaunchSpec{spec}); err == nil || !strings.Contains(err.Error(), "approved recovery source differs") {
			t.Fatalf("digest substitution accepted: %v", err)
		}
	}
}
