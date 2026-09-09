package run

import (
	"swarm/packages/swarmd/internal/tool"
	"testing"
)

// Requirement: accepted native edits allow recovery from earlier mistakes, but
// reads cannot mask a stuck loop and total failures remain bounded. Observe is
// the narrow production stop-policy boundary; legacy behavior is unchanged.
func TestDesignerNativeFailureProgress(t *testing.T) {
	observe := func(s *designerToolFailureState, action, failure string) bool {
		_, stop := s.Observe([]tool.Call{{Name: "artifact_v3_author", Arguments: `{"action":"` + action + `"}`}}, []tool.Result{{Error: failure}})
		return stop
	}
	t.Run("corrected edits reset consecutive failures", func(t *testing.T) {
		s := &designerToolFailureState{}
		for i := 0; i < 4; i++ {
			if observe(s, "edit_file", "literal mismatch") {
				t.Fatal("stopped despite corrections")
			}
			if observe(s, "edit_file", "") {
				t.Fatal("successful edit stopped")
			}
		}
	})
	t.Run("reads do not reset failure budget", func(t *testing.T) {
		s := &designerToolFailureState{}
		for i := 0; i < 3; i++ {
			stop := observe(s, "edit_file", "literal mismatch")
			if stop != (i == 2) {
				t.Fatalf("failure %d stop=%v", i, stop)
			}
			observe(s, "read_file", "")
		}
	})
	t.Run("total cap survives progress", func(t *testing.T) {
		s := &designerToolFailureState{}
		for i := 0; i < 12; i++ {
			stop := observe(s, "edit_file", "literal mismatch")
			if stop != (i == 11) {
				t.Fatalf("failure %d stop=%v", i, stop)
			}
			observe(s, "edit_file", "")
		}
	})
}
