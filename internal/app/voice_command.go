package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
)

func (a *App) handleVoiceCommand(args []string) {
	if a.api == nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("voice api is unavailable")
		return
	}
	if len(args) == 0 {
		a.openVoiceModal()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "open", "manage", "status", "devices", "list":
		a.openVoiceModal()
	case "device":
		a.handleVoiceDeviceCommand(args[1:])
	case "stt":
		a.handleVoiceSTTCommand(args[1:])
	case "tts":
		a.handleVoiceTTSCommand(args[1:])
	case "test":
		a.handleVoiceTestCommand(args[1:])
	case "profile", "profiles":
		a.handleVoiceProfileCommand(args[1:])
	default:
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /voice [open|device <id>|stt [provider] [model]|profile [list|use <id>|upsert <id> <adapter> [model]|whisper [id] [model]|delete <id>]|tts [provider] [voice]|test [seconds]]")
	}
}

func (a *App) openVoiceModal() {
	a.home.ClearCommandOverlay()
	a.home.HideSessionsModal()
	a.home.HideAuthModal()
	a.home.HideWorkspaceModal()
	a.home.HideSandboxModal()
	a.home.HideWorktreesModal()
	a.home.HideMCPModal()
	a.home.HideModelsModal()
	a.home.HideAgentsModal()
	a.home.HideThemeModal()
	a.home.HideKeybindsModal()
	a.home.ShowVoiceModal()
	a.refreshVoiceModalData("Loading voice controls...")
}

func (a *App) refreshVoiceModalData(statusHint string) {
	if !a.home.VoiceModalVisible() {
		return
	}
	if strings.TrimSpace(statusHint) != "" {
		a.home.SetVoiceModalStatus(statusHint)
	}
	a.home.SetVoiceModalLoading(true)

	if a.api == nil {
		a.home.SetVoiceModalLoading(false)
		a.home.SetVoiceModalError("voice api is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	status, statusErr := a.api.GetVoiceStatus(ctx)
	devices, devicesErr := a.api.ListVoiceDevices(ctx)
	if statusErr != nil && devicesErr != nil {
		a.home.SetVoiceModalLoading(false)
		a.home.SetVoiceModalError(fmt.Sprintf("voice load failed: status=%v, devices=%v", statusErr, devicesErr))
		return
	}

	if statusErr == nil {
		a.home.SetVoiceModalData(mapVoiceModalStatus(status), mapVoiceModalDevices(devices))
	} else {
		emptyStatus := client.VoiceStatus{}
		a.home.SetVoiceModalData(mapVoiceModalStatus(emptyStatus), mapVoiceModalDevices(devices))
	}
	a.home.SetVoiceModalLoading(false)

	switch {
	case statusErr != nil:
		a.home.SetVoiceModalStatus(fmt.Sprintf("devices loaded, status failed: %v", statusErr))
	case devicesErr != nil:
		a.home.SetVoiceModalStatus(fmt.Sprintf("status loaded, device scan failed: %v", devicesErr))
	default:
		a.home.SetVoiceModalStatus(fmt.Sprintf("voice devices loaded: %d", len(devices)))
	}
}

func (a *App) handleVoiceModalAction(action ui.VoiceModalAction) {
	if !a.home.VoiceModalVisible() {
		return
	}
	switch action.Kind {
	case ui.VoiceModalActionRefresh:
		a.refreshVoiceModalData(action.StatusHint)
	case ui.VoiceModalActionSetDevice:
		target := strings.TrimSpace(action.DeviceID)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
			DeviceID: stringPtr(target),
		}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice device update failed: %v", err))
			return
		}
		if target == "" {
			a.home.SetVoiceModalStatus("voice device set to system default")
		} else {
			a.home.SetVoiceModalStatus(fmt.Sprintf("voice device set: %s", target))
		}
		a.refreshVoiceModalData("")
	case ui.VoiceModalActionSetSTTProfile:
		profileID := strings.TrimSpace(action.STTProfile)
		empty := ""
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
			STTProfile:  stringPtr(profileID),
			STTProvider: &empty,
			STTModel:    &empty,
		}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice stt profile update failed: %v", err))
			return
		}
		if profileID == "" {
			a.home.SetVoiceModalStatus("voice stt profile cleared")
		} else {
			a.home.SetVoiceModalStatus(fmt.Sprintf("voice stt profile: %s", profileID))
		}
		a.refreshVoiceModalData("")
	case ui.VoiceModalActionSetSTT:
		profileClear := ""
		provider := strings.TrimSpace(action.STTProvider)
		model := strings.TrimSpace(action.STTModel)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
			STTProfile:  &profileClear,
			STTProvider: stringPtr(provider),
			STTModel:    stringPtr(model),
		}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice stt update failed: %v", err))
			return
		}
		if provider == "" {
			a.home.SetVoiceModalStatus("voice stt provider fallback reset to auto")
		} else if model == "" {
			a.home.SetVoiceModalStatus(fmt.Sprintf("voice stt provider fallback: %s", provider))
		} else {
			a.home.SetVoiceModalStatus(fmt.Sprintf("voice stt fallback: %s / %s", provider, model))
		}
		a.refreshVoiceModalData("")
	case ui.VoiceModalActionCreateProfile:
		profileID := strings.TrimSpace(action.ProfileID)
		if profileID == "" {
			profileID = "whisper-local"
		}
		adapterID := strings.TrimSpace(action.ProfileAdapter)
		if adapterID == "" {
			adapterID = "whisper-local"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := a.api.UpsertVoiceProfile(ctx, client.VoiceProfileUpsertRequest{
			ID:          profileID,
			Label:       strings.TrimSpace(action.ProfileLabel),
			Adapter:     adapterID,
			STTModel:    strings.TrimSpace(action.ProfileModel),
			STTLanguage: strings.TrimSpace(action.ProfileLang),
		}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice profile upsert failed: %v", err))
			return
		}
		if _, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{STTProfile: stringPtr(profileID)}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice profile select failed: %v", err))
			return
		}
		a.home.SetVoiceModalStatus(fmt.Sprintf("voice profile ready: %s", profileID))
		a.refreshVoiceModalData("")
	case ui.VoiceModalActionSetTTS:
		provider := strings.TrimSpace(action.TTSProvider)
		voiceID := strings.TrimSpace(action.TTSVoice)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if _, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
			TTSProvider: stringPtr(provider),
			TTSVoice:    stringPtr(voiceID),
		}); err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice tts update failed: %v", err))
			return
		}
		if provider == "" {
			a.home.SetVoiceModalStatus("voice tts disabled (placeholder)")
		} else {
			a.home.SetVoiceModalStatus(fmt.Sprintf("voice tts provider: %s (placeholder)", provider))
		}
		a.refreshVoiceModalData("")
	case ui.VoiceModalActionTestSTT:
		req := client.VoiceTestSTTRequest{
			Profile:  strings.TrimSpace(action.STTProfile),
			Provider: strings.TrimSpace(action.STTProvider),
			Model:    strings.TrimSpace(action.STTModel),
			Language: strings.TrimSpace(action.STTLanguage),
			Seconds:  action.Seconds,
		}
		if req.Seconds <= 0 {
			req.Seconds = 4
		}
		if req.Seconds > 15 {
			req.Seconds = 15
		}
		a.home.SetVoiceModalStatus(fmt.Sprintf("recording voice for %ds...", req.Seconds))
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Seconds+12)*time.Second)
		defer cancel()
		result, err := a.api.TestVoiceSTT(ctx, req)
		if err != nil {
			a.home.SetVoiceModalLoading(false)
			a.home.SetVoiceModalError(fmt.Sprintf("voice test failed: %v", err))
			return
		}
		a.home.SetVoiceModalLoading(false)
		a.home.SetVoiceModalTestResult(mapVoiceModalTestResult(result))
		a.home.SetVoiceModalStatus(fmt.Sprintf("voice test complete (%s/%s)", result.Provider, result.Model))
		a.showToast(ui.ToastSuccess, "voice test completed")
	default:
		a.home.SetVoiceModalLoading(false)
	}
}

func (a *App) handleVoiceDeviceCommand(args []string) {
	if len(args) == 0 {
		a.openVoiceModal()
		return
	}
	target := strings.TrimSpace(strings.Join(args, " "))
	if target == "" {
		a.openVoiceModal()
		return
	}
	switch strings.ToLower(target) {
	case "default", "auto", "clear", "none", "off":
		target = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
		DeviceID: stringPtr(target),
	})
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice device update failed: %v", err))
		if a.home.VoiceModalVisible() {
			a.home.SetVoiceModalError(fmt.Sprintf("voice device update failed: %v", err))
		}
		return
	}
	a.home.SetCommandOverlay(voiceStatusLines(status))
	if strings.TrimSpace(target) == "" {
		a.home.SetStatus("voice device cleared (system default)")
	} else {
		a.home.SetStatus(fmt.Sprintf("voice device set: %s", target))
	}
	if a.home.VoiceModalVisible() {
		a.refreshVoiceModalData("")
	}
}

func (a *App) handleVoiceSTTCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "status") {
		a.openVoiceModal()
		return
	}
	provider := strings.TrimSpace(args[0])
	model := ""
	if len(args) > 1 {
		model = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	switch strings.ToLower(provider) {
	case "default", "auto", "clear", "none", "off":
		provider = ""
		model = ""
	}
	empty := ""
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
		STTProfile:  &empty,
		STTProvider: stringPtr(provider),
		STTModel:    stringPtr(model),
	})
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice stt update failed: %v", err))
		if a.home.VoiceModalVisible() {
			a.home.SetVoiceModalError(fmt.Sprintf("voice stt update failed: %v", err))
		}
		return
	}
	a.home.SetCommandOverlay(voiceStatusLines(status))
	if provider == "" {
		a.home.SetStatus("voice stt provider reset to auto")
	} else if model == "" {
		a.home.SetStatus(fmt.Sprintf("voice stt provider: %s", provider))
	} else {
		a.home.SetStatus(fmt.Sprintf("voice stt: %s / %s", provider, model))
	}
	if a.home.VoiceModalVisible() {
		a.refreshVoiceModalData("")
	}
}

func (a *App) handleVoiceProfileCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		profiles, err := a.api.ListVoiceProfiles(ctx)
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("voice profile list failed: %v", err))
			return
		}
		lines := []string{"voice profiles:"}
		if len(profiles) == 0 {
			lines = append(lines, "- (none)")
		} else {
			for _, profile := range profiles {
				line := fmt.Sprintf("- %s adapter=%s", emptyFallback(profile.ID, "-"), emptyFallback(profile.Adapter, "-"))
				if profile.STTModel != "" {
					line += " model=" + profile.STTModel
				}
				if profile.ActiveSTT {
					line += " [active-stt]"
				}
				if !profile.AdapterConfigured && strings.TrimSpace(profile.AdapterReason) != "" {
					line += " " + strings.TrimSpace(profile.AdapterReason)
				}
				lines = append(lines, line)
			}
		}
		lines = append(lines, "usage: /voice profile use <id>")
		a.home.SetCommandOverlay(lines)
		a.home.SetStatus(fmt.Sprintf("voice profiles loaded: %d", len(profiles)))
		if a.home.VoiceModalVisible() {
			a.refreshVoiceModalData("")
		}
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "use":
		if len(args) < 2 {
			a.home.SetStatus("usage: /voice profile use <id|auto>")
			return
		}
		profileID := strings.TrimSpace(args[1])
		switch strings.ToLower(profileID) {
		case "auto", "default", "none", "off", "clear":
			profileID = ""
		}
		empty := ""
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		status, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
			STTProfile:  stringPtr(profileID),
			STTProvider: &empty,
			STTModel:    &empty,
		})
		if err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("voice profile select failed: %v", err))
			return
		}
		a.home.SetCommandOverlay(voiceStatusLines(status))
		if profileID == "" {
			a.home.SetStatus("voice stt profile cleared")
		} else {
			a.home.SetStatus(fmt.Sprintf("voice stt profile set: %s", profileID))
		}
		if a.home.VoiceModalVisible() {
			a.refreshVoiceModalData("")
		}
	case "delete", "rm", "remove":
		if len(args) < 2 {
			a.home.SetStatus("usage: /voice profile delete <id>")
			return
		}
		profileID := strings.TrimSpace(args[1])
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := a.api.DeleteVoiceProfile(ctx, profileID); err != nil {
			a.home.ClearCommandOverlay()
			a.home.SetStatus(fmt.Sprintf("voice profile delete failed: %v", err))
			return
		}
		a.home.SetStatus(fmt.Sprintf("voice profile deleted: %s", profileID))
		a.handleVoiceProfileCommand([]string{"list"})
	case "whisper":
		profileID := "whisper-local"
		if len(args) > 1 {
			profileID = strings.TrimSpace(args[1])
		}
		model := ""
		if len(args) > 2 {
			model = strings.TrimSpace(strings.Join(args[2:], " "))
		}
		a.upsertVoiceProfile(profileID, "whisper-local", model)
	case "upsert", "add", "create":
		if len(args) < 3 {
			a.home.SetStatus("usage: /voice profile upsert <id> <adapter> [model]")
			return
		}
		profileID := strings.TrimSpace(args[1])
		adapter := strings.TrimSpace(args[2])
		model := ""
		if len(args) > 3 {
			model = strings.TrimSpace(strings.Join(args[3:], " "))
		}
		a.upsertVoiceProfile(profileID, adapter, model)
	default:
		a.home.SetStatus("usage: /voice profile [list|use <id>|upsert <id> <adapter> [model]|whisper [id] [model]|delete <id>]")
	}
}

func (a *App) upsertVoiceProfile(profileID, adapter, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profileID = strings.TrimSpace(profileID)
	adapter = strings.TrimSpace(adapter)
	if profileID == "" || adapter == "" {
		a.home.SetStatus("voice profile upsert requires id and adapter")
		return
	}
	if _, err := a.api.UpsertVoiceProfile(ctx, client.VoiceProfileUpsertRequest{
		ID:       profileID,
		Label:    profileID,
		Adapter:  adapter,
		STTModel: strings.TrimSpace(model),
	}); err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice profile upsert failed: %v", err))
		return
	}
	status, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{STTProfile: stringPtr(profileID)})
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice profile select failed: %v", err))
		return
	}
	a.home.SetCommandOverlay(voiceStatusLines(status))
	a.home.SetStatus(fmt.Sprintf("voice profile ready: %s", profileID))
	if a.home.VoiceModalVisible() {
		a.refreshVoiceModalData("")
	}
}

func (a *App) handleVoiceTTSCommand(args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "status") {
		a.openVoiceModal()
		return
	}
	provider := strings.TrimSpace(args[0])
	voiceID := ""
	if len(args) > 1 {
		voiceID = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	switch strings.ToLower(provider) {
	case "default", "auto", "clear", "none", "off":
		provider = ""
		voiceID = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	status, err := a.api.UpdateVoiceConfig(ctx, client.VoiceConfigUpdateRequest{
		TTSProvider: stringPtr(provider),
		TTSVoice:    stringPtr(voiceID),
	})
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice tts update failed: %v", err))
		if a.home.VoiceModalVisible() {
			a.home.SetVoiceModalError(fmt.Sprintf("voice tts update failed: %v", err))
		}
		return
	}
	a.home.SetCommandOverlay(voiceStatusLines(status))
	if provider == "" {
		a.home.SetStatus("voice tts provider cleared")
	} else if voiceID == "" {
		a.home.SetStatus(fmt.Sprintf("voice tts provider: %s (placeholder)", provider))
	} else {
		a.home.SetStatus(fmt.Sprintf("voice tts: %s / %s (placeholder)", provider, voiceID))
	}
	if a.home.VoiceModalVisible() {
		a.refreshVoiceModalData("")
	}
}

func (a *App) handleVoiceTestCommand(args []string) {
	req := client.VoiceTestSTTRequest{Seconds: 4}
	if len(args) > 0 {
		if parsed, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil {
			req.Seconds = parsed
			args = args[1:]
		}
	}
	if req.Seconds <= 0 {
		req.Seconds = 4
	}
	if req.Seconds > 15 {
		req.Seconds = 15
	}
	if len(args) > 0 {
		first := strings.TrimSpace(args[0])
		if strings.HasPrefix(strings.ToLower(first), "profile=") {
			req.Profile = strings.TrimSpace(strings.TrimPrefix(first, "profile="))
			args = args[1:]
		}
	}
	if len(args) > 0 {
		req.Provider = strings.TrimSpace(args[0])
	}
	if len(args) > 1 {
		req.Model = strings.TrimSpace(strings.Join(args[1:], " "))
	}

	a.home.ClearCommandOverlay()
	a.home.SetStatus(fmt.Sprintf("recording voice for %ds...", req.Seconds))
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Seconds+12)*time.Second)
	defer cancel()
	result, err := a.api.TestVoiceSTT(ctx, req)
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("voice test failed: %v", err))
		if a.home.VoiceModalVisible() {
			a.home.SetVoiceModalError(fmt.Sprintf("voice test failed: %v", err))
		}
		return
	}
	a.home.SetCommandOverlay(voiceTestLines(result))
	a.home.SetStatus(fmt.Sprintf("voice test complete (%s/%s)", result.Provider, result.Model))
	if a.home.VoiceModalVisible() {
		a.home.SetVoiceModalTestResult(mapVoiceModalTestResult(result))
		a.home.SetVoiceModalStatus(fmt.Sprintf("voice test complete (%s/%s)", result.Provider, result.Model))
	}
}

func (a *App) captureVoiceInput() {
	a.toggleVoiceCapture()
}

func mapVoiceModalStatus(status client.VoiceStatus) ui.VoiceModalStatus {
	out := ui.VoiceModalStatus{
		PathID: strings.TrimSpace(status.PathID),
		Config: ui.VoiceModalConfig{
			STTProfile:  strings.TrimSpace(status.Config.STTProfile),
			STTProvider: strings.TrimSpace(status.Config.STTProvider),
			STTModel:    strings.TrimSpace(status.Config.STTModel),
			STTLanguage: strings.TrimSpace(status.Config.STTLanguage),
			DeviceID:    strings.TrimSpace(status.Config.DeviceID),
			TTSProfile:  strings.TrimSpace(status.Config.TTSProfile),
			TTSProvider: strings.TrimSpace(status.Config.TTSProvider),
			TTSVoice:    strings.TrimSpace(status.Config.TTSVoice),
			UpdatedAt:   status.Config.UpdatedAt,
		},
		STT: ui.VoiceModalSTTStatus{
			Profile:    strings.TrimSpace(status.STT.Profile),
			Provider:   strings.TrimSpace(status.STT.Provider),
			Model:      strings.TrimSpace(status.STT.Model),
			Configured: status.STT.Configured,
			Reason:     strings.TrimSpace(status.STT.Reason),
			Providers:  make([]ui.VoiceModalSTTProviderRef, 0, len(status.STT.Providers)),
		},
		TTS: ui.VoiceModalTTSStatus{
			Provider:   strings.TrimSpace(status.TTS.Provider),
			Voice:      strings.TrimSpace(status.TTS.Voice),
			Configured: status.TTS.Configured,
			Reason:     strings.TrimSpace(status.TTS.Reason),
			Providers:  make([]ui.VoiceModalTTSProviderRef, 0, len(status.TTS.Providers)),
		},
		Profiles: make([]ui.VoiceModalProfile, 0, len(status.Profiles)),
	}
	for _, profile := range status.Profiles {
		out.Profiles = append(out.Profiles, ui.VoiceModalProfile{
			ID:                strings.TrimSpace(profile.ID),
			Label:             strings.TrimSpace(profile.Label),
			Adapter:           strings.TrimSpace(profile.Adapter),
			STTModel:          strings.TrimSpace(profile.STTModel),
			STTLanguage:       strings.TrimSpace(profile.STTLanguage),
			TTSVoice:          strings.TrimSpace(profile.TTSVoice),
			UpdatedAt:         profile.UpdatedAt,
			ActiveSTT:         profile.ActiveSTT,
			ActiveTTS:         profile.ActiveTTS,
			AdapterConfigured: profile.AdapterConfigured,
			AdapterReason:     strings.TrimSpace(profile.AdapterReason),
		})
	}
	for _, provider := range status.STT.Providers {
		out.STT.Providers = append(out.STT.Providers, ui.VoiceModalSTTProviderRef{
			ID:           strings.TrimSpace(provider.ID),
			Configured:   provider.Configured,
			Reason:       strings.TrimSpace(provider.Reason),
			Models:       append([]string(nil), provider.Models...),
			DefaultModel: strings.TrimSpace(provider.DefaultModel),
		})
	}
	for _, provider := range status.TTS.Providers {
		out.TTS.Providers = append(out.TTS.Providers, ui.VoiceModalTTSProviderRef{
			ID:         strings.TrimSpace(provider.ID),
			Configured: provider.Configured,
			Reason:     strings.TrimSpace(provider.Reason),
		})
	}
	return out
}

func mapVoiceModalDevices(devices []client.VoiceDevice) []ui.VoiceModalDevice {
	out := make([]ui.VoiceModalDevice, 0, len(devices))
	for _, device := range devices {
		out = append(out, ui.VoiceModalDevice{
			ID:       strings.TrimSpace(device.ID),
			Name:     strings.TrimSpace(device.Name),
			Default:  device.Default,
			Selected: device.Selected,
			Backend:  strings.TrimSpace(device.Backend),
		})
	}
	return out
}

func mapVoiceModalTestResult(result client.VoiceTestSTTResult) ui.VoiceModalTestResult {
	return ui.VoiceModalTestResult{
		PathID:        strings.TrimSpace(result.PathID),
		Profile:       strings.TrimSpace(result.Profile),
		Provider:      strings.TrimSpace(result.Provider),
		Model:         strings.TrimSpace(result.Model),
		Language:      strings.TrimSpace(result.Language),
		Text:          strings.TrimSpace(result.Text),
		Seconds:       result.Seconds,
		DeviceID:      strings.TrimSpace(result.DeviceID),
		RecordBackend: strings.TrimSpace(result.RecordBackend),
		AudioBytes:    result.AudioBytes,
		DurationMS:    result.DurationMS,
	}
}

func voiceStatusLines(status client.VoiceStatus) []string {
	sttTarget := emptyFallback(status.STT.Provider, "auto")
	if strings.TrimSpace(status.Config.STTProfile) != "" {
		sttTarget = "profile=" + strings.TrimSpace(status.Config.STTProfile)
	}
	lines := []string{
		fmt.Sprintf("voice status path: %s", emptyFallback(strings.TrimSpace(status.PathID), "-")),
		fmt.Sprintf("stt: target=%s model=%s configured=%t", sttTarget, emptyFallback(status.STT.Model, "-"), status.STT.Configured),
	}
	if reason := strings.TrimSpace(status.STT.Reason); reason != "" {
		lines = append(lines, "stt reason: "+reason)
	}
	lines = append(lines, fmt.Sprintf("tts: provider=%s voice=%s configured=%t", emptyFallback(status.TTS.Provider, "-"), emptyFallback(status.TTS.Voice, "-"), status.TTS.Configured))
	if reason := strings.TrimSpace(status.TTS.Reason); reason != "" {
		lines = append(lines, "tts reason: "+reason)
	}
	lines = append(lines, fmt.Sprintf("device: %s", emptyFallback(status.Config.DeviceID, "(system default)")))
	if len(status.Profiles) > 0 {
		lines = append(lines, "stt profiles:")
		for _, profile := range status.Profiles {
			line := fmt.Sprintf("- %s adapter=%s", emptyFallback(profile.ID, "-"), emptyFallback(profile.Adapter, "-"))
			if profile.STTModel != "" {
				line += " model=" + profile.STTModel
			}
			if profile.ActiveSTT {
				line += " [active]"
			}
			if !profile.AdapterConfigured && profile.AdapterReason != "" {
				line += " " + profile.AdapterReason
			}
			lines = append(lines, line)
		}
	}
	if len(status.STT.Providers) > 0 {
		lines = append(lines, "stt providers:")
		for _, provider := range status.STT.Providers {
			line := fmt.Sprintf("- %s configured=%t", provider.ID, provider.Configured)
			if provider.DefaultModel != "" {
				line += fmt.Sprintf(" default=%s", provider.DefaultModel)
			}
			if provider.Reason != "" && !provider.Configured {
				line += " " + provider.Reason
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "usage: /voice")
	return lines
}

func voiceTestLines(result client.VoiceTestSTTResult) []string {
	target := emptyFallback(result.Provider, "-") + "/" + emptyFallback(result.Model, "-")
	if strings.TrimSpace(result.Profile) != "" {
		target = fmt.Sprintf("profile=%s -> %s", result.Profile, target)
	}
	lines := []string{
		fmt.Sprintf("voice test path: %s", emptyFallback(result.PathID, "-")),
		"target: " + target,
		fmt.Sprintf("device: %s (%s)  seconds=%d  audio_bytes=%d", emptyFallback(result.DeviceID, "default"), emptyFallback(result.RecordBackend, "-"), result.Seconds, result.AudioBytes),
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		lines = append(lines, "transcript: (empty)")
		return lines
	}
	lines = append(lines, "transcript:")
	lines = append(lines, text)
	return lines
}
