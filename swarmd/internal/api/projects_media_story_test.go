package api

import (
	"context"
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// Requirement: multipart validation bounds cost and keeps unsupported operations
// from reaching dispatch. Boundary: validateVideoScenes, shared by creation and
// approval/execution. Narrow unit checks cover malformed input and exact bounds.
func TestValidateVideoScenes(t *testing.T) {
	scenes := []pebblestore.ProjectTaskScene{{SceneNumber: 1, Prompt: "first", DurationSec: 8}, {SceneNumber: 2, Prompt: "second", DurationSec: 8}}
	if err := validateVideoScenes(scenes, "create", 1, ""); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		scenes     []pebblestore.ProjectTaskScene
		op         string
		variants   int
		soundtrack string
	}{
		{"empty", nil, "create", 1, ""}, {"one", scenes[:1], "create", 1, ""},
		{"edit", scenes, "edit", 1, ""}, {"variants", scenes, "create", 2, ""},
		{"soundtrack", scenes, "create", 1, "music"},
		{"empty prompt", []pebblestore.ProjectTaskScene{{Prompt: "first"}, {Prompt: " "}}, "create", 1, ""},
		{"negative duration", []pebblestore.ProjectTaskScene{{Prompt: "first"}, {Prompt: "second", DurationSec: -1}}, "create", 1, ""},
		{"misordered", []pebblestore.ProjectTaskScene{{Prompt: "first", SceneNumber: 2}, {Prompt: "second"}}, "create", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if validateVideoScenes(tc.scenes, tc.op, tc.variants, tc.soundtrack) == nil {
				t.Fatal("invalid story accepted")
			}
		})
	}
}

type storyFailGenerator struct{ prompts []string }

func (g *storyFailGenerator) GenerateManagedVideo(_ context.Context, req videogen.ManagedVideoRequest) (videogen.ManagedVideoResult, error) {
	g.prompts = append(g.prompts, req.Prompt)
	if len(g.prompts) == 2 {
		return videogen.ManagedVideoResult{}, errors.New("provider failure")
	}
	return videogen.ManagedVideoResult{Bytes: []byte("fixture"), MediaType: "video/mp4"}, nil
}

// Requirement: failures/cancellation must not return a publishable partial cut
// or spend on later scenes. Boundary: sequential project story execution.
func TestProjectVideoStoryFailureAndCancellation(t *testing.T) {
	scenes := []pebblestore.ProjectTaskScene{{Prompt: "first"}, {Prompt: "second"}, {Prompt: "third"}}
	generator := &storyFailGenerator{}
	result, err := generateProjectVideoStory(context.Background(), generator, videogen.ManagedVideoRequest{}, scenes)
	if err == nil || len(result.Bytes) != 0 || len(generator.prompts) != 2 || generator.prompts[1] != "second" {
		t.Fatalf("failure contract: err=%v calls=%v bytes=%d", err, generator.prompts, len(result.Bytes))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	generator = &storyFailGenerator{}
	result, err = generateProjectVideoStory(ctx, generator, videogen.ManagedVideoRequest{}, scenes)
	if !errors.Is(err, context.Canceled) || len(generator.prompts) != 0 || len(result.Bytes) != 0 {
		t.Fatal("cancelled story dispatched or published")
	}
}
