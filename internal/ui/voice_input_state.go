package ui

import (
	"fmt"
	"time"
)

type VoiceInputPhase string

const (
	VoiceInputPhaseIdle       VoiceInputPhase = ""
	VoiceInputPhaseRecording  VoiceInputPhase = "recording"
	VoiceInputPhaseProcessing VoiceInputPhase = "processing"
)

type VoiceInputState struct {
	Phase VoiceInputPhase
	Since time.Time
}

func (s VoiceInputState) Active() bool {
	return s.Phase != VoiceInputPhaseIdle
}

func voiceInputOverlayText(state VoiceInputState, now time.Time) (string, bool) {
	switch state.Phase {
	case VoiceInputPhaseRecording:
		return fmt.Sprintf("[REC %s] Press F9 to stop", formatVoiceElapsed(now.Sub(state.Since))), true
	case VoiceInputPhaseProcessing:
		return fmt.Sprintf("[%s] Processing voice...", voiceInputSpinnerFrame(now)), true
	default:
		return "", false
	}
}

func formatVoiceElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d / time.Second)
	minutes := seconds / 60
	seconds = seconds % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func voiceInputSpinnerFrame(now time.Time) string {
	frames := []string{"|", "/", "-", "\\"}
	if len(frames) == 0 {
		return "|"
	}
	index := int((now.UnixMilli() / 150) % int64(len(frames)))
	if index < 0 {
		index = 0
	}
	return frames[index]
}
