package htmlcapture

import "fmt"

// AnimationFrameBudget is shared by authoring, conversion and rendering. The
// viewport is fixed at 1920x1080; 36,000 frames therefore also bound pixel work.
// Check duration before multiplication so hostile integers cannot wrap admission.
func AnimationFrameBudget(durationMS int64, fps int) (int, error) {
	if durationMS < MinAuthoredAnimationDurationMS || fps < 1 || fps > MaxAnimationFPS {
		return 0, NewError("animation_source_limit_exceeded", "animation requires duration >= 100 ms and integral FPS between 1 and 60")
	}
	if durationMS > int64(MaxAnimationFrames)*1000/int64(fps) {
		return 0, NewError("animation_source_limit_exceeded", fmt.Sprintf("animation exceeds %d frames at 1920x1080: %d ms at %d FPS; lower FPS or split into separate jobs (maximum %d ms at this FPS)", MaxAnimationFrames, durationMS, fps, int64(MaxAnimationFrames)*1000/int64(fps)))
	}
	return int((durationMS*int64(fps) + 999) / 1000), nil
}
