package htmlcapture

import (
	"errors"
	"testing"
)

// Requirement: native temporal authoring acknowledges both time_ms and scene_id.
// The animation bootstrap and captureAnimationFrame must accept that shape without
// weakening exact integer-millisecond echo, rejection, or unknown-field checks.
// Browser preflight is the narrowest layer exercising both trusted JS validators;
// synthetic scene transitions and the last frame avoid private artifact fixtures.
func TestAnimationSeekSceneContract(t *testing.T) {
	for _, tc := range []struct{ name, ack, code string }{
		{"scene-boundary-and-final-frame", `{time_ms:t,scene_id:t<500?"opening":"resolve"}`, ""},
		{"time-only", `{time_ms:t}`, ""},
		{"wrong-time", `{time_ms:t+1,scene_id:"opening"}`, "animation_seek_ack_mismatch"},
		{"rounded-frame-time", `{time_ms:Math.round(t/1000*30)*1000/30,scene_id:"resolve"}`, "animation_seek_ack_mismatch"},
		{"empty-scene", `{time_ms:t,scene_id:""}`, "animation_seek_ack_mismatch"},
		{"non-string-scene", `{time_ms:t,scene_id:1}`, "animation_seek_ack_mismatch"},
		{"unknown-field", `{time_ms:t,scene_id:"opening",extra:true}`, "animation_seek_ack_mismatch"},
		{"missing-time", `{scene_id:"opening"}`, "animation_seek_ack_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `<!doctype html><html><head><script>globalThis.__SWARM_ANIMATION_V1__={version:"swarm.animation/v1",ready:()=>({duration_ms:1000,fps:30}),seek:t=>{return ` + tc.ack + `}};</script></head><body></body></html>`
			result, err := preflightHTML(t, body, 1000, 30)
			if tc.code != "" {
				var captureErr *Error
				if !errors.As(err, &captureErr) || captureErr.Code != tc.code {
					t.Fatalf("want %s, got %v", tc.code, err)
				}
				if len(result.PreviewPNG) != 0 || len(result.MP4) != 0 {
					t.Fatal("failed seek returned deliverable media")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := map[int]bool{}
			for _, frame := range result.InspectionFrames {
				seen[frame.TimestampMS] = true
			}
			for _, timestamp := range []int{0, 500, 966} {
				if !seen[timestamp] {
					t.Fatalf("missing boundary/frame %d: %v", timestamp, seen)
				}
			}
		})
	}
}
