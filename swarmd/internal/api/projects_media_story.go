package api

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
)

// validateVideoScenes bounds provider spend before persistence or dispatch.
func validateVideoScenes(scenes []pebblestore.ProjectTaskScene, operation string, variants int, soundtrack string) error {
	if len(scenes) < 2 || len(scenes) > 8 {
		return errors.New("multipart video stories require 2..8 explicit scenes")
	}
	if operation != "" && operation != "create" {
		return errors.New("multipart video stories require create operation")
	}
	if variants > 1 {
		return errors.New("multipart video stories support one assembled variant")
	}
	if strings.TrimSpace(soundtrack) != "" {
		return errors.New("continuous soundtrack must be added in Video Studio; multipart scenes retain their generated audio")
	}
	for i, scene := range scenes {
		if strings.TrimSpace(scene.Prompt) == "" {
			return fmt.Errorf("scene %d requires a prompt", i+1)
		}
		if len(scene.Prompt) > 16000 || scene.DurationSec < 0 || scene.DurationSec > 30 {
			return fmt.Errorf("scene %d exceeds prompt or duration bounds", i+1)
		}
		if scene.SceneNumber != 0 && scene.SceneNumber != i+1 {
			return errors.New("scene numbers must follow array order")
		}
	}
	return nil
}

// generateProjectVideoStory never publishes a successful partial story. Scene
// results remain private until every generation and final measured assembly
// succeeds; the ordinary task acceptance gate owns the single returned cut.
func generateProjectVideoStory(ctx context.Context, vg managedVideoService, req videogen.ManagedVideoRequest, scenes []pebblestore.ProjectTaskScene) (videogen.ManagedVideoResult, error) {
	// Fail before paid generation when the local assembly runtime is absent.
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			return videogen.ManagedVideoResult{}, fmt.Errorf("%s is required for multipart video", binary)
		}
	}
	clips := make([]videogen.ManagedVideoResult, 0, len(scenes))
	totalBytes := 0
	for i, scene := range scenes {
		if err := ctx.Err(); err != nil {
			return videogen.ManagedVideoResult{}, err
		}
		sceneReq := req
		sceneReq.Prompt = scene.Prompt
		if strings.TrimSpace(scene.VisualNotes) != "" {
			sceneReq.Prompt += "\n" + scene.VisualNotes
		}
		if scene.DurationSec > 0 {
			sceneReq.DurationSeconds = scene.DurationSec
		}
		if i > 0 {
			sceneReq.Image = nil
		}
		clip, err := vg.GenerateManagedVideo(ctx, sceneReq)
		if err != nil {
			return videogen.ManagedVideoResult{}, fmt.Errorf("scene %d: %w", i+1, err)
		}
		if len(clip.Bytes) == 0 || clip.MediaType != "video/mp4" {
			return videogen.ManagedVideoResult{}, fmt.Errorf("scene %d returned no MP4", i+1)
		}
		totalBytes += len(clip.Bytes)
		if totalBytes > 100<<20 {
			return videogen.ManagedVideoResult{}, errors.New("story scene bytes exceed 100 MiB assembly budget")
		}
		clips = append(clips, clip)
	}
	return videogen.AssembleClips(ctx, clips)
}
