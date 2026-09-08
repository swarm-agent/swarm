package run

import (
	"fmt"
	"testing"
)

// Requirement: native target_part_ids are authoritative for regular and swarm
// Designers; optional display hints must agree but need no duplicate locator.
// Threat: legacy locator parsing blocks native swarms or widens their intent.
// Authority: parseTaskCallArguments/parseTaskSwarmArguments, before any launch.
func TestArtifactV3SwarmTargetHints(t *testing.T) {
	const base = `"mode":"swarm","agent_type":"designer","count":2,"prompt":"revise","artifact_v3_source":{"session_id":"parent","artifact_id":"artifact","commit_oid":"commit","projection_seq":1,"target_part_ids":["pricing"]}`
	for _, hint := range []string{"", `,"section_target":{"id":"pricing","label":"Pricing","kind":"selector"}`, `,"section_targets":[{"id":"pricing","label":"Pricing","kind":"selector"}]`} {
		parsed, err := parseTaskCallArguments("{" + base + hint + "}")
		if err != nil || len(parsed.Launches) != 2 {
			t.Fatalf("hint %s: %+v %v", hint, parsed, err)
		}
		for _, launch := range parsed.Launches {
			if launch.ArtifactV3Source == nil || launch.ArtifactV3Source.TargetPartIDs[0] != "pricing" {
				t.Fatalf("lost target: %+v", launch)
			}
		}
	}
	for _, ids := range []string{`{"id":"footer","label":"Footer","kind":"selector"}`, `{"id":"pricing","label":"Pricing","kind":"selector"},{"id":"footer","label":"Footer","kind":"selector"}`} {
		if parsed, err := parseTaskCallArguments(fmt.Sprintf(`{%s,"section_targets":[%s]}`, base, ids)); err == nil || len(parsed.Launches) != 0 {
			t.Fatalf("mismatched intent accepted: %+v %v", parsed, err)
		}
	}
}
