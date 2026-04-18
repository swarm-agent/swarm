package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

const voiceCaptureMaxDuration = 2 * time.Minute

func (a *App) toggleVoiceCapture() {
	if a.api == nil {
		a.home.SetStatus("voice api is unavailable")
		return
	}

	switch a.voiceCapture.Phase {
	case voiceCapturePhaseIdle:
		a.startVoiceCapture()
	case voiceCapturePhaseRecording:
		a.stopVoiceCapture()
	case voiceCapturePhaseProcessing:
		a.setVoiceCaptureStatus("voice capture: processing transcription...")
	default:
		a.resetVoiceCaptureState()
		a.startVoiceCapture()
	}
}

func (a *App) startVoiceCapture() {
	route := "home"
	sessionID := ""
	if a.route == "chat" && a.chat != nil {
		route = "chat"
		sessionID = strings.TrimSpace(a.chat.SessionID())
	}

	profile := ""
	provider := ""
	model := ""
	language := ""
	deviceID := ""

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	status, err := a.api.GetVoiceStatus(ctx)
	cancel()
	if err == nil {
		profile = strings.TrimSpace(status.Config.STTProfile)
		provider = strings.TrimSpace(status.Config.STTProvider)
		model = strings.TrimSpace(status.Config.STTModel)
		language = strings.TrimSpace(status.Config.STTLanguage)
		deviceID = strings.TrimSpace(status.Config.DeviceID)
	}

	a.voiceCaptureSeq++
	captureID := a.voiceCaptureSeq
	recordCtx, recordCancel := context.WithTimeout(context.Background(), voiceCaptureMaxDuration)

	a.voiceCapture = activeVoiceCapture{
		ID:        captureID,
		Phase:     voiceCapturePhaseRecording,
		Since:     time.Now(),
		Route:     route,
		SessionID: sessionID,
		DeviceID:  deviceID,
		Profile:   profile,
		Provider:  provider,
		Model:     model,
		Language:  language,
		cancel:    recordCancel,
	}
	a.syncVoiceInputState()

	statusMsg := "voice capture: recording (F9 to stop)"
	if err != nil {
		statusMsg = "voice capture: recording (F9 to stop, using default voice config)"
	}
	a.setVoiceCaptureStatus(statusMsg)

	go func(id int64, device string, ctx context.Context) {
		audio, backend, recordErr := recordLocalVoiceAudio(ctx, device)
		a.postVoiceCaptureEvent(voiceCaptureEvent{
			CaptureID: id,
			Kind:      voiceCaptureEventKindRecorded,
			Audio:     audio,
			Backend:   backend,
			Err:       recordErr,
		})
	}(captureID, deviceID, recordCtx)
}

func (a *App) stopVoiceCapture() {
	if a.voiceCapture.Phase != voiceCapturePhaseRecording {
		return
	}
	if a.voiceCapture.cancel != nil {
		a.voiceCapture.cancel()
		a.voiceCapture.cancel = nil
	}
	a.voiceCapture.Phase = voiceCapturePhaseProcessing
	a.voiceCapture.Since = time.Now()
	a.syncVoiceInputState()
	a.setVoiceCaptureStatus("voice capture: processing transcription...")
}

func (a *App) consumeVoiceCaptureEvents() {
	for {
		select {
		case event := <-a.voiceCaptureCh:
			a.handleVoiceCaptureEvent(event)
		default:
			return
		}
	}
}

func (a *App) handleVoiceCaptureEvent(event voiceCaptureEvent) {
	if event.CaptureID == 0 || event.CaptureID != a.voiceCapture.ID {
		return
	}
	if a.voiceCapture.Phase == voiceCapturePhaseIdle {
		return
	}

	switch event.Kind {
	case voiceCaptureEventKindRecorded:
		if event.Err != nil {
			a.failVoiceCapture(fmt.Sprintf("voice capture failed: %v", event.Err))
			return
		}
		if len(event.Audio) == 0 {
			a.failVoiceCapture("voice capture failed: no audio recorded")
			return
		}
		if a.voiceCapture.Phase == voiceCapturePhaseRecording {
			a.voiceCapture.Phase = voiceCapturePhaseProcessing
			a.voiceCapture.Since = time.Now()
			a.syncVoiceInputState()
			a.setVoiceCaptureStatus("voice capture: processing transcription...")
		}
		a.startVoiceTranscription(event.CaptureID, event.Audio)
	case voiceCaptureEventKindTranscribed:
		if event.Err != nil {
			a.failVoiceCapture(fmt.Sprintf("voice capture failed: %v", event.Err))
			return
		}
		text := strings.TrimSpace(event.Result.Text)
		if text == "" {
			a.failVoiceCapture("voice capture returned no transcript")
			return
		}
		capture := a.voiceCapture
		a.resetVoiceCaptureState()
		a.insertVoiceTranscript(capture, text, event.Result)
	}
}

func (a *App) startVoiceTranscription(captureID int64, audio []byte) {
	profile := strings.TrimSpace(a.voiceCapture.Profile)
	provider := strings.TrimSpace(a.voiceCapture.Provider)
	model := strings.TrimSpace(a.voiceCapture.Model)
	language := strings.TrimSpace(a.voiceCapture.Language)

	go func(id int64, payload []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		result, err := a.api.TranscribeSTT(ctx, client.STTTranscribeRequest{
			Profile:  profile,
			Provider: provider,
			Model:    model,
			Language: language,
			Audio:    payload,
		})
		a.postVoiceCaptureEvent(voiceCaptureEvent{
			CaptureID: id,
			Kind:      voiceCaptureEventKindTranscribed,
			Result:    result,
			Err:       err,
		})
	}(captureID, append([]byte(nil), audio...))
}

func (a *App) insertVoiceTranscript(capture activeVoiceCapture, text string, result client.STTTranscribeResult) {
	if capture.Route == "chat" && capture.SessionID != "" && a.chat != nil && strings.EqualFold(strings.TrimSpace(a.chat.SessionID()), capture.SessionID) {
		existing := strings.TrimSpace(a.chat.InputValue())
		next := text
		if existing != "" {
			next = existing + " " + text
		}
		a.chat.SetInput(next)
		a.chat.SetStatus(fmt.Sprintf("voice captured (%s/%s)", emptyFallback(result.Provider, "stt"), emptyFallback(result.Model, "auto")))
		a.showToast(ui.ToastSuccess, "voice input inserted")
		return
	}

	existing := strings.TrimSpace(a.home.PromptValue())
	next := text
	if existing != "" {
		next = existing + " " + text
	}
	a.home.SetPrompt(next)
	a.home.SetStatus(fmt.Sprintf("voice captured (%s/%s)", emptyFallback(result.Provider, "stt"), emptyFallback(result.Model, "auto")))
	a.showToast(ui.ToastSuccess, "voice input inserted")
}

func (a *App) failVoiceCapture(message string) {
	a.resetVoiceCaptureState()
	a.setVoiceCaptureStatus(message)
	a.showToast(ui.ToastError, message)
}

func (a *App) setVoiceCaptureStatus(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if a.voiceCapture.Route == "chat" && a.chat != nil {
		if a.voiceCapture.SessionID == "" || strings.EqualFold(strings.TrimSpace(a.chat.SessionID()), a.voiceCapture.SessionID) {
			a.chat.SetStatus(message)
			return
		}
	}
	a.home.SetStatus(message)
}

func (a *App) resetVoiceCaptureState() {
	if a.voiceCapture.cancel != nil {
		a.voiceCapture.cancel()
		a.voiceCapture.cancel = nil
	}
	a.voiceCapture = activeVoiceCapture{Phase: voiceCapturePhaseIdle}
	a.syncVoiceInputState()
}

func (a *App) voiceInputLocked() bool {
	switch a.voiceCapture.Phase {
	case voiceCapturePhaseRecording, voiceCapturePhaseProcessing:
		return true
	default:
		return false
	}
}

func (a *App) syncVoiceInputState() {
	state := ui.VoiceInputState{}
	switch a.voiceCapture.Phase {
	case voiceCapturePhaseRecording:
		state = ui.VoiceInputState{Phase: ui.VoiceInputPhaseRecording, Since: a.voiceCapture.Since}
	case voiceCapturePhaseProcessing:
		state = ui.VoiceInputState{Phase: ui.VoiceInputPhaseProcessing, Since: a.voiceCapture.Since}
	}
	if a.home != nil {
		a.home.SetVoiceInputState(state)
	}
	if a.chat != nil {
		a.chat.SetVoiceInputState(state)
	}
}

func (a *App) postVoiceCaptureEvent(event voiceCaptureEvent) {
	select {
	case a.voiceCaptureCh <- event:
	default:
	}
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptVoiceReady))
	}
}
