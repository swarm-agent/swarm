package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

type VoiceModalActionKind string

const (
	VoiceModalActionRefresh       VoiceModalActionKind = "refresh"
	VoiceModalActionSetDevice     VoiceModalActionKind = "set-device"
	VoiceModalActionSetSTT        VoiceModalActionKind = "set-stt"
	VoiceModalActionSetSTTProfile VoiceModalActionKind = "set-stt-profile"
	VoiceModalActionSetTTS        VoiceModalActionKind = "set-tts"
	VoiceModalActionTestSTT       VoiceModalActionKind = "test-stt"
	VoiceModalActionCreateProfile VoiceModalActionKind = "create-profile"
)

type VoiceModalAction struct {
	Kind           VoiceModalActionKind
	DeviceID       string
	STTProfile     string
	STTProvider    string
	STTModel       string
	STTLanguage    string
	TTSProvider    string
	TTSVoice       string
	ProfileID      string
	ProfileLabel   string
	ProfileAdapter string
	ProfileModel   string
	ProfileLang    string
	Seconds        int
	StatusHint     string
}

type VoiceModalStatus struct {
	PathID   string
	STT      VoiceModalSTTStatus
	TTS      VoiceModalTTSStatus
	Profiles []VoiceModalProfile
	Config   VoiceModalConfig
}

type VoiceModalConfig struct {
	STTProfile  string
	STTProvider string
	STTModel    string
	STTLanguage string
	DeviceID    string
	TTSProfile  string
	TTSProvider string
	TTSVoice    string
	UpdatedAt   int64
}

type VoiceModalProfile struct {
	ID                string
	Label             string
	Adapter           string
	STTModel          string
	STTLanguage       string
	TTSVoice          string
	UpdatedAt         int64
	ActiveSTT         bool
	ActiveTTS         bool
	AdapterConfigured bool
	AdapterReason     string
}

type VoiceModalSTTStatus struct {
	Profile    string
	Provider   string
	Model      string
	Configured bool
	Reason     string
	Providers  []VoiceModalSTTProviderRef
}

type VoiceModalSTTProviderRef struct {
	ID           string
	Configured   bool
	Reason       string
	Models       []string
	DefaultModel string
}

type VoiceModalTTSStatus struct {
	Provider   string
	Voice      string
	Configured bool
	Reason     string
	Providers  []VoiceModalTTSProviderRef
}

type VoiceModalTTSProviderRef struct {
	ID         string
	Configured bool
	Reason     string
}

type VoiceModalDevice struct {
	ID       string
	Name     string
	Default  bool
	Selected bool
	Backend  string
}

type VoiceModalTestResult struct {
	PathID        string
	Profile       string
	Provider      string
	Model         string
	Language      string
	Text          string
	Seconds       int
	DeviceID      string
	RecordBackend string
	AudioBytes    int
	DurationMS    int64
}

type voiceModalItemKind string

const (
	voiceModalItemKindSection voiceModalItemKind = "section"
	voiceModalItemKindAction  voiceModalItemKind = "action"
	voiceModalItemKindDevice  voiceModalItemKind = "device"
	voiceModalItemKindProfile voiceModalItemKind = "profile"
	voiceModalItemKindSTT     voiceModalItemKind = "stt"
	voiceModalItemKindTTS     voiceModalItemKind = "tts"
)

type voiceModalItem struct {
	Kind       voiceModalItemKind
	Selectable bool
	Title      string
	Detail     string
	Action     string
	DeviceID   string
	ProfileID  string
	Provider   string
	Model      string
	Voice      string
	Seconds    int
}

type voiceModalState struct {
	Visible   bool
	Loading   bool
	Status    string
	Error     string
	Items     []voiceModalItem
	Selected  int
	Scroll    int
	StatusDTO VoiceModalStatus
	Devices   []VoiceModalDevice
	LastTest  *VoiceModalTestResult
}

func (p *HomePage) ShowVoiceModal() {
	p.voiceModal.Visible = true
	p.voiceModal.rebuildItems()
	p.voiceModal.ensureSelection()
}

func (p *HomePage) HideVoiceModal() {
	p.voiceModal = voiceModalState{}
	p.pendingVoiceAction = nil
}

func (p *HomePage) VoiceModalVisible() bool {
	return p.voiceModal.Visible
}

func (p *HomePage) SetVoiceModalLoading(loading bool) {
	p.voiceModal.Loading = loading
}

func (p *HomePage) SetVoiceModalStatus(status string) {
	p.voiceModal.Status = strings.TrimSpace(status)
	if p.voiceModal.Status != "" {
		p.voiceModal.Error = ""
	}
}

func (p *HomePage) SetVoiceModalError(err string) {
	p.voiceModal.Error = strings.TrimSpace(err)
	if p.voiceModal.Error != "" {
		p.voiceModal.Loading = false
	}
}

func (p *HomePage) SetVoiceModalData(status VoiceModalStatus, devices []VoiceModalDevice) {
	selectedKey := p.voiceModal.selectedKey()
	p.voiceModal.StatusDTO = cloneVoiceModalStatus(status)
	p.voiceModal.Devices = cloneVoiceModalDevices(devices)
	p.voiceModal.rebuildItems()
	if !p.voiceModal.selectByKey(selectedKey) {
		p.voiceModal.selectPreferredItem()
	}
}

func (p *HomePage) SetVoiceModalTestResult(result VoiceModalTestResult) {
	copyResult := result
	p.voiceModal.LastTest = &copyResult
}

func (p *HomePage) PopVoiceModalAction() (VoiceModalAction, bool) {
	if p.pendingVoiceAction == nil {
		return VoiceModalAction{}, false
	}
	action := *p.pendingVoiceAction
	p.pendingVoiceAction = nil
	return action, true
}

func (p *HomePage) enqueueVoiceModalAction(action VoiceModalAction) {
	if action.Kind == "" {
		return
	}
	p.pendingVoiceAction = &action
	p.voiceModal.Loading = true
	if strings.TrimSpace(action.StatusHint) != "" {
		p.voiceModal.Status = action.StatusHint
	}
	p.voiceModal.Error = ""
}

func (p *HomePage) handleVoiceModalKey(ev *tcell.EventKey) {
	switch {
	case p.keybinds.Match(ev, KeybindModalClose):
		p.HideVoiceModal()
		return
	case p.keybinds.Match(ev, KeybindModalMoveUp), p.keybinds.Match(ev, KeybindModalMoveUpAlt):
		p.moveVoiceModalSelection(-1)
		return
	case p.keybinds.Match(ev, KeybindModalMoveDown), p.keybinds.Match(ev, KeybindModalMoveDownAlt):
		p.moveVoiceModalSelection(1)
		return
	case p.keybinds.Match(ev, KeybindModalPageUp):
		p.moveVoiceModalSelection(-6)
		return
	case p.keybinds.Match(ev, KeybindModalPageDown):
		p.moveVoiceModalSelection(6)
		return
	case p.keybinds.Match(ev, KeybindModalJumpHome):
		p.moveVoiceModalSelectionToEdge(true)
		return
	case p.keybinds.Match(ev, KeybindModalJumpEnd):
		p.moveVoiceModalSelectionToEdge(false)
		return
	case p.keybinds.Match(ev, KeybindModalEnter):
		p.handleVoiceModalEnter()
		return
	}

	if ev.Key() == tcell.KeyRune {
		switch {
		case p.keybinds.Match(ev, KeybindVoiceRefresh):
			p.enqueueVoiceModalAction(VoiceModalAction{
				Kind:       VoiceModalActionRefresh,
				StatusHint: "Refreshing voice status...",
			})
		case p.keybinds.Match(ev, KeybindVoiceTest):
			profile, provider, model := p.voiceModal.selectedSTTTarget()
			p.enqueueVoiceModalAction(VoiceModalAction{
				Kind:        VoiceModalActionTestSTT,
				STTProfile:  profile,
				STTProvider: provider,
				STTModel:    model,
				Seconds:     4,
				StatusHint:  "Recording voice sample...",
			})
		}
	}
}

func (p *HomePage) handleVoiceModalEnter() {
	item, ok := p.voiceModal.selectedItem()
	if !ok {
		return
	}
	switch item.Kind {
	case voiceModalItemKindAction:
		switch strings.ToLower(strings.TrimSpace(item.Action)) {
		case "refresh":
			p.enqueueVoiceModalAction(VoiceModalAction{
				Kind:       VoiceModalActionRefresh,
				StatusHint: "Refreshing voice status...",
			})
		case "test":
			profile, provider, model := p.voiceModal.selectedSTTTarget()
			p.enqueueVoiceModalAction(VoiceModalAction{
				Kind:        VoiceModalActionTestSTT,
				STTProfile:  profile,
				STTProvider: provider,
				STTModel:    model,
				Seconds:     maxInt(1, item.Seconds),
				StatusHint:  fmt.Sprintf("Recording voice sample for %ds...", maxInt(1, item.Seconds)),
			})
		case "create-whisper":
			p.enqueueVoiceModalAction(VoiceModalAction{
				Kind:           VoiceModalActionCreateProfile,
				ProfileID:      "whisper-local",
				ProfileLabel:   "Local Whisper",
				ProfileAdapter: "whisper-local",
				StatusHint:     "Creating or updating whisper-local profile...",
			})
		}
	case voiceModalItemKindDevice:
		label := "Using system default device"
		if strings.TrimSpace(item.DeviceID) != "" {
			label = "Setting device: " + strings.TrimSpace(item.DeviceID)
		}
		p.enqueueVoiceModalAction(VoiceModalAction{
			Kind:       VoiceModalActionSetDevice,
			DeviceID:   strings.TrimSpace(item.DeviceID),
			StatusHint: label,
		})
	case voiceModalItemKindProfile:
		status := "Clearing STT profile"
		if strings.TrimSpace(item.ProfileID) != "" {
			status = "Setting STT profile: " + strings.TrimSpace(item.ProfileID)
		}
		p.enqueueVoiceModalAction(VoiceModalAction{
			Kind:       VoiceModalActionSetSTTProfile,
			STTProfile: strings.TrimSpace(item.ProfileID),
			StatusHint: status,
		})
	case voiceModalItemKindSTT:
		status := "Setting STT provider"
		if strings.TrimSpace(item.Provider) == "" {
			status = "Resetting STT provider fallback"
		} else if strings.TrimSpace(item.Model) != "" {
			status = fmt.Sprintf("Setting STT fallback: %s / %s", strings.TrimSpace(item.Provider), strings.TrimSpace(item.Model))
		}
		p.enqueueVoiceModalAction(VoiceModalAction{
			Kind:        VoiceModalActionSetSTT,
			STTProfile:  "",
			STTProvider: strings.TrimSpace(item.Provider),
			STTModel:    strings.TrimSpace(item.Model),
			StatusHint:  status,
		})
	case voiceModalItemKindTTS:
		status := "Updating TTS placeholder setting"
		if strings.TrimSpace(item.Provider) == "" {
			status = "Disabling TTS placeholder setting"
		}
		p.enqueueVoiceModalAction(VoiceModalAction{
			Kind:        VoiceModalActionSetTTS,
			TTSProvider: strings.TrimSpace(item.Provider),
			TTSVoice:    strings.TrimSpace(item.Voice),
			StatusHint:  status,
		})
	}
}

func (p *HomePage) moveVoiceModalSelection(delta int) {
	if delta == 0 {
		return
	}
	selectable := p.voiceModal.selectableIndexes()
	if len(selectable) == 0 {
		return
	}
	pos := indexInList(selectable, p.voiceModal.Selected)
	if pos < 0 {
		pos = 0
	}
	pos += delta
	if pos < 0 {
		pos = 0
	}
	if pos >= len(selectable) {
		pos = len(selectable) - 1
	}
	p.voiceModal.Selected = selectable[pos]
}

func (p *HomePage) moveVoiceModalSelectionToEdge(first bool) {
	selectable := p.voiceModal.selectableIndexes()
	if len(selectable) == 0 {
		return
	}
	if first {
		p.voiceModal.Selected = selectable[0]
		return
	}
	p.voiceModal.Selected = selectable[len(selectable)-1]
}

func (s *voiceModalState) selectedItem() (voiceModalItem, bool) {
	if s.Selected < 0 || s.Selected >= len(s.Items) {
		return voiceModalItem{}, false
	}
	item := s.Items[s.Selected]
	if !item.Selectable {
		return voiceModalItem{}, false
	}
	return item, true
}

func (s *voiceModalState) selectableIndexes() []int {
	out := make([]int, 0, len(s.Items))
	for i, item := range s.Items {
		if item.Selectable {
			out = append(out, i)
		}
	}
	return out
}

func (s *voiceModalState) selectedKey() string {
	item, ok := s.selectedItem()
	if !ok {
		return ""
	}
	return voiceModalItemKey(item)
}

func (s *voiceModalState) selectByKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for i, item := range s.Items {
		if !item.Selectable {
			continue
		}
		if voiceModalItemKey(item) == key {
			s.Selected = i
			return true
		}
	}
	return false
}

func (s *voiceModalState) ensureSelection() {
	selectable := s.selectableIndexes()
	if len(selectable) == 0 {
		s.Selected = -1
		s.Scroll = 0
		return
	}
	if indexInList(selectable, s.Selected) < 0 {
		s.Selected = selectable[0]
	}
	if s.Scroll < 0 {
		s.Scroll = 0
	}
}

func (s *voiceModalState) selectPreferredItem() {
	selectable := s.selectableIndexes()
	if len(selectable) == 0 {
		s.Selected = -1
		return
	}

	activeProfile := strings.TrimSpace(s.StatusDTO.Config.STTProfile)
	activeDevice := strings.TrimSpace(s.StatusDTO.Config.DeviceID)
	activeSTTProvider := strings.TrimSpace(s.StatusDTO.Config.STTProvider)
	activeSTTModel := strings.TrimSpace(s.StatusDTO.Config.STTModel)

	for _, idx := range selectable {
		item := s.Items[idx]
		if item.Kind != voiceModalItemKindProfile {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.ProfileID), activeProfile) {
			s.Selected = idx
			return
		}
		if activeProfile == "" && strings.TrimSpace(item.ProfileID) == "" {
			s.Selected = idx
			return
		}
	}
	for _, idx := range selectable {
		item := s.Items[idx]
		if item.Kind != voiceModalItemKindDevice {
			continue
		}
		if strings.TrimSpace(item.DeviceID) == activeDevice {
			s.Selected = idx
			return
		}
		if activeDevice == "" && strings.TrimSpace(item.DeviceID) == "" {
			s.Selected = idx
			return
		}
	}
	for _, idx := range selectable {
		item := s.Items[idx]
		if item.Kind != voiceModalItemKindSTT {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Provider), activeSTTProvider) &&
			strings.EqualFold(strings.TrimSpace(item.Model), activeSTTModel) {
			s.Selected = idx
			return
		}
		if activeSTTProvider == "" && activeSTTModel == "" && strings.TrimSpace(item.Provider) == "" {
			s.Selected = idx
			return
		}
	}
	for _, idx := range selectable {
		item := s.Items[idx]
		if item.Kind == voiceModalItemKindAction &&
			strings.EqualFold(strings.TrimSpace(item.Action), "test") &&
			item.Seconds == 4 {
			s.Selected = idx
			return
		}
	}
	s.Selected = selectable[0]
}

func (s *voiceModalState) selectedSTTTarget() (profile, provider, model string) {
	item, ok := s.selectedItem()
	if ok {
		switch item.Kind {
		case voiceModalItemKindProfile:
			return strings.TrimSpace(item.ProfileID), "", ""
		case voiceModalItemKindSTT:
			return "", strings.TrimSpace(item.Provider), strings.TrimSpace(item.Model)
		}
	}
	return strings.TrimSpace(s.StatusDTO.Config.STTProfile), strings.TrimSpace(s.StatusDTO.Config.STTProvider), strings.TrimSpace(s.StatusDTO.Config.STTModel)
}

func (s *voiceModalState) rebuildItems() {
	items := make([]voiceModalItem, 0, 120)
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSection,
		Selectable: false,
		Title:      "Actions",
	})
	items = append(items,
		voiceModalItem{
			Kind:       voiceModalItemKindAction,
			Selectable: true,
			Title:      "Refresh status and devices",
			Detail:     "Read latest profile/provider and microphone state",
			Action:     "refresh",
		},
		voiceModalItem{
			Kind:       voiceModalItemKindAction,
			Selectable: true,
			Title:      "Create whisper-local profile",
			Detail:     "Open-source local STT profile sample",
			Action:     "create-whisper",
		},
		voiceModalItem{
			Kind:       voiceModalItemKindAction,
			Selectable: true,
			Title:      "Test microphone (4s)",
			Detail:     "Record and transcribe with selected STT target",
			Action:     "test",
			Seconds:    4,
		},
		voiceModalItem{
			Kind:       voiceModalItemKindAction,
			Selectable: true,
			Title:      "Test microphone (8s)",
			Detail:     "Longer sample for accuracy check",
			Action:     "test",
			Seconds:    8,
		},
	)

	configDeviceID := strings.TrimSpace(s.StatusDTO.Config.DeviceID)
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSection,
		Selectable: false,
		Title:      "Microphone Device",
	})
	defaultDetail := "Use operating system default microphone"
	if configDeviceID == "" {
		defaultDetail = "Use operating system default microphone [active]"
	}
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindDevice,
		Selectable: true,
		Title:      "System default",
		Detail:     defaultDetail,
		DeviceID:   "",
	})
	for _, device := range s.Devices {
		deviceID := strings.TrimSpace(device.ID)
		if deviceID == "" {
			continue
		}
		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = deviceID
		}
		flags := make([]string, 0, 3)
		if device.Default {
			flags = append(flags, "default")
		}
		if device.Selected || strings.EqualFold(deviceID, configDeviceID) {
			flags = append(flags, "active")
		}
		detail := strings.TrimSpace(deviceID)
		if backend := strings.TrimSpace(device.Backend); backend != "" {
			detail += " (" + backend + ")"
		}
		if len(flags) > 0 {
			detail += " [" + strings.Join(flags, ", ") + "]"
		}
		items = append(items, voiceModalItem{
			Kind:       voiceModalItemKindDevice,
			Selectable: true,
			Title:      name,
			Detail:     detail,
			DeviceID:   deviceID,
		})
	}

	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSection,
		Selectable: false,
		Title:      "STT Profiles",
	})
	autoProfileDetail := "No explicit profile (use provider fallback)"
	if strings.TrimSpace(s.StatusDTO.Config.STTProfile) == "" {
		autoProfileDetail += " [active]"
	}
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindProfile,
		Selectable: true,
		Title:      "Auto",
		Detail:     autoProfileDetail,
		ProfileID:  "",
	})
	for _, profile := range s.StatusDTO.Profiles {
		profileID := strings.TrimSpace(profile.ID)
		if profileID == "" {
			continue
		}
		title := profileID
		if label := strings.TrimSpace(profile.Label); label != "" {
			title = label + " (" + profileID + ")"
		}
		detailParts := make([]string, 0, 4)
		if adapter := strings.TrimSpace(profile.Adapter); adapter != "" {
			detailParts = append(detailParts, "adapter="+adapter)
		}
		if model := strings.TrimSpace(profile.STTModel); model != "" {
			detailParts = append(detailParts, "model="+model)
		}
		detail := strings.Join(detailParts, " ")
		if detail == "" {
			detail = "profile"
		}
		if !profile.AdapterConfigured {
			reason := strings.TrimSpace(profile.AdapterReason)
			if reason == "" {
				reason = "adapter not configured"
			}
			detail += " - " + reason
		}
		if profile.ActiveSTT || strings.EqualFold(profileID, strings.TrimSpace(s.StatusDTO.Config.STTProfile)) {
			detail += " [active]"
		}
		items = append(items, voiceModalItem{
			Kind:       voiceModalItemKindProfile,
			Selectable: true,
			Title:      title,
			Detail:     detail,
			ProfileID:  profileID,
		})
	}

	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSection,
		Selectable: false,
		Title:      "STT Provider Fallback",
	})
	autoDetail := "Auto provider and default model (used when no profile selected)"
	if strings.TrimSpace(s.StatusDTO.Config.STTProfile) == "" && strings.TrimSpace(s.StatusDTO.Config.STTProvider) == "" {
		autoDetail += " [active]"
	}
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSTT,
		Selectable: true,
		Title:      "Auto",
		Detail:     autoDetail,
	})
	for _, provider := range s.StatusDTO.STT.Providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			continue
		}
		models := dedupeVoiceModels(provider.Models, provider.DefaultModel)
		if len(models) == 0 {
			models = append(models, strings.TrimSpace(provider.DefaultModel))
		}
		if len(models) == 0 {
			models = append(models, "")
		}
		for _, model := range models {
			title := providerID
			if strings.TrimSpace(model) != "" {
				title += " / " + strings.TrimSpace(model)
			}
			detail := "configured"
			if !provider.Configured {
				detail = strings.TrimSpace(provider.Reason)
				if detail == "" {
					detail = "not configured"
				}
			}
			if strings.TrimSpace(s.StatusDTO.Config.STTProfile) == "" &&
				strings.EqualFold(providerID, strings.TrimSpace(s.StatusDTO.Config.STTProvider)) &&
				strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(s.StatusDTO.Config.STTModel)) {
				detail += " [active]"
			}
			items = append(items, voiceModalItem{
				Kind:       voiceModalItemKindSTT,
				Selectable: true,
				Title:      title,
				Detail:     detail,
				Provider:   providerID,
				Model:      strings.TrimSpace(model),
			})
		}
	}

	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindSection,
		Selectable: false,
		Title:      "Text To Speech (placeholder)",
	})
	ttsDisabledDetail := "No TTS output provider"
	if strings.TrimSpace(s.StatusDTO.Config.TTSProvider) == "" {
		ttsDisabledDetail += " [active]"
	}
	items = append(items, voiceModalItem{
		Kind:       voiceModalItemKindTTS,
		Selectable: true,
		Title:      "Disabled",
		Detail:     ttsDisabledDetail,
	})
	for _, provider := range s.StatusDTO.TTS.Providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			continue
		}
		detail := "placeholder provider"
		if !provider.Configured {
			detail = strings.TrimSpace(provider.Reason)
			if detail == "" {
				detail = "not configured"
			}
		}
		voiceID := ""
		if strings.EqualFold(providerID, strings.TrimSpace(s.StatusDTO.Config.TTSProvider)) {
			voiceID = strings.TrimSpace(s.StatusDTO.Config.TTSVoice)
			detail += " [active]"
		}
		items = append(items, voiceModalItem{
			Kind:       voiceModalItemKindTTS,
			Selectable: true,
			Title:      providerID,
			Detail:     detail,
			Provider:   providerID,
			Voice:      voiceID,
		})
	}

	s.Items = items
	s.ensureSelection()
}

func cloneVoiceModalStatus(status VoiceModalStatus) VoiceModalStatus {
	out := status
	out.STT.Providers = make([]VoiceModalSTTProviderRef, 0, len(status.STT.Providers))
	for _, provider := range status.STT.Providers {
		copyProvider := provider
		copyProvider.Models = append([]string(nil), provider.Models...)
		out.STT.Providers = append(out.STT.Providers, copyProvider)
	}
	out.TTS.Providers = append([]VoiceModalTTSProviderRef(nil), status.TTS.Providers...)
	out.Profiles = append([]VoiceModalProfile(nil), status.Profiles...)
	return out
}

func cloneVoiceModalDevices(devices []VoiceModalDevice) []VoiceModalDevice {
	return append([]VoiceModalDevice(nil), devices...)
}

func dedupeVoiceModels(models []string, defaultModel string) []string {
	out := make([]string, 0, len(models)+1)
	seen := make(map[string]struct{}, len(models)+1)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	add(defaultModel)
	for _, model := range models {
		add(model)
	}
	return out
}

func voiceModalItemKey(item voiceModalItem) string {
	switch item.Kind {
	case voiceModalItemKindAction:
		return fmt.Sprintf("action:%s:%d", strings.ToLower(strings.TrimSpace(item.Action)), item.Seconds)
	case voiceModalItemKindDevice:
		return "device:" + strings.ToLower(strings.TrimSpace(item.DeviceID))
	case voiceModalItemKindProfile:
		return "profile:" + strings.ToLower(strings.TrimSpace(item.ProfileID))
	case voiceModalItemKindSTT:
		return fmt.Sprintf(
			"stt:%s:%s",
			strings.ToLower(strings.TrimSpace(item.Provider)),
			strings.ToLower(strings.TrimSpace(item.Model)),
		)
	case voiceModalItemKindTTS:
		return fmt.Sprintf(
			"tts:%s:%s",
			strings.ToLower(strings.TrimSpace(item.Provider)),
			strings.ToLower(strings.TrimSpace(item.Voice)),
		)
	default:
		return ""
	}
}
