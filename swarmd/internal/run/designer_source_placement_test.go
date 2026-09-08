package run

import (
	"strings"
	"testing"
)

// Requirement: misplaced source authority must fail before allocation, never
// silently become initial creation. parseTaskCallArguments is the earliest
// shared admission boundary; successful parsing is required to launch workers.
func TestDesignerRejectsNestedSourceBinding(t *testing.T) {
	for _, key := range []string{"artifact_v3_source", "artifact_v2_source", "source_artifact"} {
		_, err := parseTaskCallArguments(`{"prompt":"Remix exact source","launches":[{"subagent_type":"designer","meta_prompt":"Remix source","` + key + `":{}}]}`)
		if err == nil || !strings.Contains(err.Error(), key+" must be supplied at task top level") {
			t.Fatalf("%s: %v", key, err)
		}
	}
}
