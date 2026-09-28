package api

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Requirement: independent video variants remain clips; only explicit scenes
// imply a story. Regression: task validation must not route a multi-output clip
// into story assembly. Authority: ProjectTaskRecord.Validate; pure validation
// is the narrowest layer proving classification without provider execution.
func TestProjectVideoOutcomeUsesScenesNotVariantCount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		scenes []pebblestore.ProjectTaskScene
		want   string
	}{
		{"single", 1, nil, "video_clip"},
		{"independent variants", 3, nil, "video_clip"},
		{"explicit story", 1, []pebblestore.ProjectTaskScene{{SceneNumber: 1, Prompt: "First"}, {SceneNumber: 2, Prompt: "Second"}}, "video_story"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := pebblestore.ProjectTaskRecord{Title: "Video", ProjectID: "project", Agent: "video", VariantCount: tc.count, Scenes: tc.scenes}
			if err := task.Validate(); err != nil {
				t.Fatal(err)
			}
			if task.OutcomeType != tc.want {
				t.Fatalf("outcome = %q, want %q", task.OutcomeType, tc.want)
			}
		})
	}
}
