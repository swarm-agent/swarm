package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestVoiceModalEnterOnDeviceQueuesSetDeviceAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	p.SetVoiceModalData(
		VoiceModalStatus{},
		[]VoiceModalDevice{
			{ID: "mic-default", Name: "Built-in Mic"},
			{ID: "usb-mic", Name: "USB Mic"},
		},
	)

	p.voiceModal.Selected = voiceModalItemIndex(p.voiceModal.Items, voiceModalItemKindDevice, "usb-mic", "", "", "")
	if p.voiceModal.Selected < 0 {
		t.Fatalf("expected usb-mic device row")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected voice modal action")
	}
	if action.Kind != VoiceModalActionSetDevice {
		t.Fatalf("action kind = %q, want %q", action.Kind, VoiceModalActionSetDevice)
	}
	if action.DeviceID != "usb-mic" {
		t.Fatalf("action device = %q, want usb-mic", action.DeviceID)
	}
}

func TestVoiceModalEnterOnProfileRowQueuesSetProfileAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	p.SetVoiceModalData(
		VoiceModalStatus{
			Profiles: []VoiceModalProfile{{ID: "whisper-local", Adapter: "whisper-local", ActiveSTT: true}},
		},
		nil,
	)

	p.voiceModal.Selected = voiceModalItemIndex(p.voiceModal.Items, voiceModalItemKindProfile, "", "", "", "whisper-local")
	if p.voiceModal.Selected < 0 {
		t.Fatalf("expected profile row")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected voice modal action")
	}
	if action.Kind != VoiceModalActionSetSTTProfile {
		t.Fatalf("action kind = %q, want %q", action.Kind, VoiceModalActionSetSTTProfile)
	}
	if action.STTProfile != "whisper-local" {
		t.Fatalf("action profile = %q, want whisper-local", action.STTProfile)
	}
}

func TestVoiceModalEnterOnSTTRowQueuesSetSTTAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	p.SetVoiceModalData(
		VoiceModalStatus{
			STT: VoiceModalSTTStatus{
				Providers: []VoiceModalSTTProviderRef{
					{ID: "whisper-local", Configured: true, Models: []string{"ggml-small.en-q5_1.bin"}},
				},
			},
		},
		nil,
	)

	p.voiceModal.Selected = voiceModalItemIndex(p.voiceModal.Items, voiceModalItemKindSTT, "", "whisper-local", "ggml-small.en-q5_1.bin", "")
	if p.voiceModal.Selected < 0 {
		t.Fatalf("expected stt row")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected voice modal action")
	}
	if action.Kind != VoiceModalActionSetSTT {
		t.Fatalf("action kind = %q, want %q", action.Kind, VoiceModalActionSetSTT)
	}
	if action.STTProvider != "whisper-local" || action.STTModel != "ggml-small.en-q5_1.bin" {
		t.Fatalf("action stt = %s/%s, want whisper-local/ggml-small.en-q5_1.bin", action.STTProvider, action.STTModel)
	}
	if action.STTProfile != "" {
		t.Fatalf("expected profile cleared on provider fallback selection")
	}
}

func TestVoiceModalCreateWhisperAction(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	idx := voiceModalItemIndexByAction(p.voiceModal.Items, "create-whisper")
	if idx < 0 {
		t.Fatalf("expected create-whisper action row")
	}
	p.voiceModal.Selected = idx

	p.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	action, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected voice modal action")
	}
	if action.Kind != VoiceModalActionCreateProfile {
		t.Fatalf("action kind = %q, want %q", action.Kind, VoiceModalActionCreateProfile)
	}
	if action.ProfileAdapter != "whisper-local" {
		t.Fatalf("profile adapter = %q, want whisper-local", action.ProfileAdapter)
	}
}

func TestVoiceModalRefreshAndTestShortcutsQueueActions(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	p.SetVoiceModalData(
		VoiceModalStatus{
			Profiles: []VoiceModalProfile{{ID: "whisper-local", Adapter: "whisper-local"}},
		},
		nil,
	)
	p.voiceModal.Selected = voiceModalItemIndex(p.voiceModal.Items, voiceModalItemKindProfile, "", "", "", "whisper-local")
	if p.voiceModal.Selected < 0 {
		t.Fatalf("expected profile row")
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'r', tcell.ModNone))
	refresh, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected refresh action")
	}
	if refresh.Kind != VoiceModalActionRefresh {
		t.Fatalf("action kind = %q, want %q", refresh.Kind, VoiceModalActionRefresh)
	}

	p.voiceModal.Loading = false
	p.HandleKey(tcell.NewEventKey(tcell.KeyRune, 't', tcell.ModNone))
	testAction, ok := p.PopVoiceModalAction()
	if !ok {
		t.Fatalf("expected test action")
	}
	if testAction.Kind != VoiceModalActionTestSTT {
		t.Fatalf("action kind = %q, want %q", testAction.Kind, VoiceModalActionTestSTT)
	}
	if testAction.STTProfile != "whisper-local" {
		t.Fatalf("test profile = %s, want whisper-local", testAction.STTProfile)
	}
	if testAction.Seconds != 4 {
		t.Fatalf("test seconds = %d, want 4", testAction.Seconds)
	}
}

func voiceModalItemIndex(items []voiceModalItem, kind voiceModalItemKind, deviceID, provider, model, profileID string) int {
	for i, item := range items {
		if item.Kind != kind {
			continue
		}
		if kind == voiceModalItemKindDevice && item.DeviceID != deviceID {
			continue
		}
		if kind == voiceModalItemKindProfile && item.ProfileID != profileID {
			continue
		}
		if (kind == voiceModalItemKindSTT || kind == voiceModalItemKindTTS) && item.Provider != provider {
			continue
		}
		if kind == voiceModalItemKindSTT && item.Model != model {
			continue
		}
		return i
	}
	return -1
}

func voiceModalItemIndexByAction(items []voiceModalItem, action string) int {
	for i, item := range items {
		if item.Kind != voiceModalItemKindAction {
			continue
		}
		if item.Action == action {
			return i
		}
	}
	return -1
}

func TestVoiceModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.ShowVoiceModal()
	p.SetVoiceModalData(VoiceModalStatus{}, []VoiceModalDevice{{ID: "mic-default", Name: "Built-in Mic"}})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 50, 14
	screen.SetSize(w, h)
	p.drawVoiceModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Voice Controls") {
		t.Fatalf("expected voice modal on narrow screen, got:\n%s", text)
	}
}
