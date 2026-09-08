package run

import (
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: explicit chapter policy must survive regular and swarm parsing
// into allocation inputs, rather than being lost in Router specializations.
// The parser is the narrowest no-provider layer proving the original contract.
func TestArtifactV3SceneTaskPropagation(t *testing.T) {
	for _, prefix := range []string{`"subagent_type":"designer","meta_prompt":"Animate chapters","output_mode":"managed",`, `"mode":"swarm","agent_type":"designer","count":2,`} {
		parsed, err := parseTaskCallArguments(`{` + prefix + `"prompt":"Animate chapters","animation_profile":{"profile":"motion_ui"},"scene_contract":{"duration_ms":8000,"scenes":[{"scene_id":"opening","start_ms":0,"end_ms":4000},{"scene_id":"resolve","start_ms":4000,"end_ms":8000}]}}`)
		if err != nil {
			t.Fatal(err)
		}
		for _, launch := range parsed.Launches {
			c, err := tool.ParseArtifactV3SceneContract(launch.SourceArguments["scene_contract"])
			if err != nil || len(c.Scenes) != 2 {
				t.Fatal("scene contract dropped", err)
			}
		}
	}
}
