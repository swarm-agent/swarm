package voice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PathStatus         = "voice.status.v1"
	PathSTTProviders   = "voice.stt.providers.v1"
	PathTranscribe     = "voice.stt.transcribe.v1"
	PathDevices        = "voice.devices.list.v1"
	PathConfig         = "voice.config.update.v1"
	PathTestSTT        = "voice.test-stt.v1"
	PathSynthesize     = "voice.tts.synthesize.v1"
	PathProfilesList   = "voice.profiles.list.v1"
	PathProfilesUpsert = "voice.profiles.upsert.v1"
	PathProfilesDelete = "voice.profiles.delete.v1"
)

var ErrTTSPlaceholder = errors.New("tts synthesis is not implemented for the selected provider")

type Adapter interface {
	ID() string
	STTReady(ctx context.Context) (configured bool, reason string, err error)
	STTModels() []string
	DefaultSTTModel() string
	Transcribe(ctx context.Context, input AdapterTranscribeInput) (AdapterTranscribeResult, error)
	TTSReady(ctx context.Context) (configured bool, reason string, err error)
	Synthesize(ctx context.Context, input AdapterSynthesizeInput) (AdapterSynthesizeResult, error)
}

type AdapterTranscribeInput struct {
	Audio    []byte
	Model    string
	Language string
	Options  map[string]string
}

type AdapterTranscribeResult struct {
	Model string
	Text  string
}

type AdapterSynthesizeInput struct {
	Text  string
	Voice string
	Model string
}

type AdapterSynthesizeResult struct {
	Model string
	Voice string
	Audio []byte
	Mime  string
}

type Service struct {
	store    *pebblestore.VoiceStore
	adapters map[string]Adapter
	order    []string
}

type Status struct {
	PathID           string               `json:"path_id"`
	STT              STTStatus            `json:"stt"`
	TTS              TTSStatus            `json:"tts"`
	Profiles         []VoiceProfileStatus `json:"profiles,omitempty"`
	Config           VoiceConfig          `json:"config"`
	DeviceProbeError string               `json:"device_probe_error,omitempty"`
}

type VoiceConfig struct {
	STTProfile  string `json:"stt_profile,omitempty"`
	STTProvider string `json:"stt_provider,omitempty"`
	STTModel    string `json:"stt_model,omitempty"`
	STTLanguage string `json:"stt_language,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	TTSProfile  string `json:"tts_profile,omitempty"`
	TTSProvider string `json:"tts_provider,omitempty"`
	TTSVoice    string `json:"tts_voice,omitempty"`
	UpdatedAt   int64  `json:"updated_at"`
}

type VoiceProfileStatus struct {
	ID                string            `json:"id"`
	Label             string            `json:"label,omitempty"`
	Adapter           string            `json:"adapter"`
	STTModel          string            `json:"stt_model,omitempty"`
	STTLanguage       string            `json:"stt_language,omitempty"`
	TTSVoice          string            `json:"tts_voice,omitempty"`
	Options           map[string]string `json:"options,omitempty"`
	UpdatedAt         int64             `json:"updated_at"`
	ActiveSTT         bool              `json:"active_stt"`
	ActiveTTS         bool              `json:"active_tts"`
	AdapterConfigured bool              `json:"adapter_configured"`
	AdapterReason     string            `json:"adapter_reason,omitempty"`
}

type ProfileUpsertInput struct {
	ID          string
	Label       string
	Adapter     string
	STTModel    string
	STTLanguage string
	TTSVoice    string
	Options     map[string]string
}

type ProfileDeleteResult struct {
	PathID  string `json:"path_id"`
	Deleted string `json:"deleted"`
}

type STTStatus struct {
	Profile    string              `json:"profile,omitempty"`
	Provider   string              `json:"provider,omitempty"`
	Model      string              `json:"model,omitempty"`
	Configured bool                `json:"configured"`
	Reason     string              `json:"reason,omitempty"`
	Providers  []STTProviderStatus `json:"providers"`
}

type STTProviderStatus struct {
	ID           string   `json:"id"`
	Configured   bool     `json:"configured"`
	Reason       string   `json:"reason,omitempty"`
	Models       []string `json:"models,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
}

type TTSStatus struct {
	Provider   string              `json:"provider,omitempty"`
	Voice      string              `json:"voice,omitempty"`
	Configured bool                `json:"configured"`
	Reason     string              `json:"reason,omitempty"`
	Providers  []TTSProviderStatus `json:"providers"`
}

type TTSProviderStatus struct {
	ID         string `json:"id"`
	Configured bool   `json:"configured"`
	Reason     string `json:"reason,omitempty"`
}

type Device struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Default  bool   `json:"default"`
	Selected bool   `json:"selected"`
	Backend  string `json:"backend,omitempty"`
}

type ConfigPatch struct {
	STTProfile  *string
	STTProvider *string
	STTModel    *string
	STTLanguage *string
	DeviceID    *string
	TTSProfile  *string
	TTSProvider *string
	TTSVoice    *string
}

type TranscribeInput struct {
	Profile  string
	Provider string
	Model    string
	Language string
	Audio    []byte
}

type TranscribeResult struct {
	PathID     string `json:"path_id"`
	Profile    string `json:"profile,omitempty"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Language   string `json:"language,omitempty"`
	Text       string `json:"text"`
	AudioBytes int    `json:"audio_bytes"`
	DurationMS int64  `json:"duration_ms"`
}

type TestSTTInput struct {
	Profile  string
	Provider string
	Model    string
	Language string
	DeviceID string
	Seconds  int
}

type TestSTTResult struct {
	PathID        string `json:"path_id"`
	Profile       string `json:"profile,omitempty"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Language      string `json:"language,omitempty"`
	Text          string `json:"text"`
	Seconds       int    `json:"seconds"`
	DeviceID      string `json:"device_id,omitempty"`
	RecordBackend string `json:"record_backend,omitempty"`
	AudioBytes    int    `json:"audio_bytes"`
	DurationMS    int64  `json:"duration_ms"`
}

type SynthesizeInput struct {
	Provider string
	Model    string
	Voice    string
	Text     string
}

type SynthesizeResult struct {
	PathID     string `json:"path_id"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Voice      string `json:"voice,omitempty"`
	Mime       string `json:"mime"`
	Audio      []byte `json:"audio"`
	AudioBytes int    `json:"audio_bytes"`
	DurationMS int64  `json:"duration_ms"`
}

type capturedAudio struct {
	Audio   []byte
	Backend string
}

type resolvedSTTSelection struct {
	Adapter      Adapter
	ProviderID   string
	ProfileID    string
	ModelHint    string
	LanguageHint string
	Options      map[string]string
}

func NewService(store *pebblestore.VoiceStore, adapters ...Adapter) *Service {
	svc := &Service{
		store:    store,
		adapters: make(map[string]Adapter, len(adapters)),
		order:    make([]string, 0, len(adapters)),
	}
	for _, adapter := range adapters {
		svc.RegisterAdapter(adapter)
	}
	return svc
}

func (s *Service) RegisterAdapter(adapter Adapter) {
	if s == nil || adapter == nil {
		return
	}
	id := normalizeProviderID(adapter.ID())
	if id == "" {
		return
	}
	if s.adapters == nil {
		s.adapters = make(map[string]Adapter, 4)
	}
	if _, exists := s.adapters[id]; !exists {
		s.order = append(s.order, id)
	}
	s.adapters[id] = adapter
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := s.ensureDefaultProfiles(); err != nil {
		return Status{}, err
	}
	status := Status{PathID: PathStatus}
	record, err := s.getConfig()
	if err != nil {
		return Status{}, err
	}
	status.Config = voiceConfigFromRecord(record)

	profiles, err := s.listProfiles(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Profiles = profiles

	sttProviders, err := s.ListSTTProviders(ctx)
	if err != nil {
		return Status{}, err
	}
	status.STT.Providers = sttProviders
	status.STT.Profile, status.STT.Provider, status.STT.Model, status.STT.Configured, status.STT.Reason = s.resolveConfiguredSTT(record, profiles, sttProviders)

	ttsProviders, err := s.listTTSProviders(ctx)
	if err != nil {
		return Status{}, err
	}
	status.TTS.Providers = ttsProviders
	status.TTS.Provider, status.TTS.Configured, status.TTS.Reason = s.resolveConfiguredTTSProvider(record, profiles, ttsProviders)
	status.TTS.Voice = strings.TrimSpace(record.TTSVoice)

	devices, deviceErr := s.ListDevices(ctx)
	if deviceErr != nil {
		status.DeviceProbeError = deviceErr.Error()
	} else if strings.TrimSpace(status.Config.DeviceID) != "" {
		found := false
		for _, device := range devices {
			if strings.EqualFold(strings.TrimSpace(device.ID), strings.TrimSpace(status.Config.DeviceID)) {
				found = true
				break
			}
		}
		if !found {
			status.DeviceProbeError = fmt.Sprintf("selected device not found: %s", status.Config.DeviceID)
		}
	}
	return status, nil
}

func (s *Service) ListSTTProviders(ctx context.Context) ([]STTProviderStatus, error) {
	statuses := make([]STTProviderStatus, 0, len(s.order))
	for _, id := range s.sortedProviderOrder() {
		adapter, ok := s.adapters[id]
		if !ok || adapter == nil {
			continue
		}
		configured, reason, err := adapter.STTReady(ctx)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, STTProviderStatus{
			ID:           id,
			Configured:   configured,
			Reason:       strings.TrimSpace(reason),
			Models:       append([]string(nil), adapter.STTModels()...),
			DefaultModel: strings.TrimSpace(adapter.DefaultSTTModel()),
		})
	}
	return statuses, nil
}

func (s *Service) ListProfiles(ctx context.Context) ([]VoiceProfileStatus, error) {
	if err := s.ensureDefaultProfiles(); err != nil {
		return nil, err
	}
	return s.listProfiles(ctx)
}

func (s *Service) UpsertProfile(ctx context.Context, input ProfileUpsertInput) (VoiceProfileStatus, error) {
	if s.store == nil {
		return VoiceProfileStatus{}, errors.New("voice profile store is not configured")
	}
	profileID := normalizeProfileID(input.ID)
	if profileID == "" {
		return VoiceProfileStatus{}, errors.New("voice profile id is required")
	}
	adapterID := normalizeProviderID(input.Adapter)
	if adapterID == "" {
		return VoiceProfileStatus{}, errors.New("voice profile adapter is required")
	}
	if _, ok := s.adapters[adapterID]; !ok {
		return VoiceProfileStatus{}, fmt.Errorf("unknown voice adapter %q", adapterID)
	}

	existing, ok, err := s.store.GetProfile(profileID)
	if err != nil {
		return VoiceProfileStatus{}, err
	}
	record := pebblestore.VoiceProfileRecord{}
	if ok {
		record = existing
	}
	record.ID = profileID
	record.Label = strings.TrimSpace(input.Label)
	record.Adapter = adapterID
	record.STTModel = strings.TrimSpace(input.STTModel)
	record.STTLanguage = strings.TrimSpace(input.STTLanguage)
	record.TTSVoice = strings.TrimSpace(input.TTSVoice)
	record.Options = copyStringMap(input.Options)

	saved, err := s.store.PutProfile(record)
	if err != nil {
		return VoiceProfileStatus{}, err
	}
	cfg, err := s.getConfig()
	if err != nil {
		return VoiceProfileStatus{}, err
	}
	activeSTT := normalizeProfileID(cfg.STTProfile)
	activeTTS := normalizeProfileID(cfg.TTSProfile)
	return s.mapProfileStatus(ctx, saved, activeSTT, activeTTS), nil
}

func (s *Service) DeleteProfile(ctx context.Context, profileID string) (ProfileDeleteResult, error) {
	if s.store == nil {
		return ProfileDeleteResult{}, errors.New("voice profile store is not configured")
	}
	profileID = normalizeProfileID(profileID)
	if profileID == "" {
		return ProfileDeleteResult{}, errors.New("voice profile id is required")
	}
	if _, ok, err := s.store.GetProfile(profileID); err != nil {
		return ProfileDeleteResult{}, err
	} else if !ok {
		return ProfileDeleteResult{}, fmt.Errorf("voice profile %q not found", profileID)
	}
	if err := s.store.DeleteProfile(profileID); err != nil {
		return ProfileDeleteResult{}, err
	}

	record, err := s.getConfig()
	if err != nil {
		return ProfileDeleteResult{}, err
	}
	patch := pebblestore.VoiceConfigPatch{}
	hasPatch := false
	if strings.EqualFold(strings.TrimSpace(record.STTProfile), profileID) {
		empty := ""
		patch.STTProfile = &empty
		hasPatch = true
	}
	if strings.EqualFold(strings.TrimSpace(record.TTSProfile), profileID) {
		empty := ""
		patch.TTSProfile = &empty
		hasPatch = true
	}
	if hasPatch {
		if _, err := s.store.UpdateConfig(patch); err != nil {
			return ProfileDeleteResult{}, err
		}
	}
	return ProfileDeleteResult{PathID: PathProfilesDelete, Deleted: profileID}, nil
}

func (s *Service) ListDevices(ctx context.Context) ([]Device, error) {
	record, err := s.getConfig()
	if err != nil {
		return nil, err
	}
	devices, err := listInputDevices(ctx)
	if err != nil {
		return nil, err
	}
	selectedID := strings.TrimSpace(record.DeviceID)
	out := make([]Device, 0, len(devices))
	for _, device := range devices {
		out = append(out, Device{
			ID:       strings.TrimSpace(device.ID),
			Name:     strings.TrimSpace(device.Name),
			Default:  device.Default,
			Selected: selectedID != "" && strings.EqualFold(strings.TrimSpace(device.ID), selectedID),
			Backend:  strings.TrimSpace(device.Backend),
		})
	}
	return out, nil
}

func (s *Service) UpdateConfig(ctx context.Context, patch ConfigPatch) (Status, error) {
	if s.store == nil {
		return Status{}, errors.New("voice config store is not configured")
	}
	if patch.STTProfile != nil {
		id := normalizeProfileID(*patch.STTProfile)
		if id != "" {
			if _, ok, err := s.store.GetProfile(id); err != nil {
				return Status{}, err
			} else if !ok {
				return Status{}, fmt.Errorf("unknown stt profile %q", id)
			}
		}
	}
	if patch.TTSProfile != nil {
		id := normalizeProfileID(*patch.TTSProfile)
		if id != "" {
			if _, ok, err := s.store.GetProfile(id); err != nil {
				return Status{}, err
			} else if !ok {
				return Status{}, fmt.Errorf("unknown tts profile %q", id)
			}
		}
	}
	if patch.STTProvider != nil {
		id := normalizeProviderID(*patch.STTProvider)
		if id != "" {
			if _, ok := s.adapters[id]; !ok {
				return Status{}, fmt.Errorf("unknown stt provider %q", id)
			}
		}
	}
	if patch.TTSProvider != nil {
		id := normalizeProviderID(*patch.TTSProvider)
		if id != "" {
			if _, ok := s.adapters[id]; !ok {
				return Status{}, fmt.Errorf("unknown tts provider %q", id)
			}
		}
	}
	if patch.DeviceID != nil {
		deviceID := strings.TrimSpace(*patch.DeviceID)
		if deviceID != "" {
			if devices, err := s.ListDevices(ctx); err == nil {
				found := false
				for _, device := range devices {
					if strings.EqualFold(strings.TrimSpace(device.ID), deviceID) {
						found = true
						break
					}
				}
				if !found {
					return Status{}, fmt.Errorf("voice device not found: %s", deviceID)
				}
			}
		}
	}

	_, err := s.store.UpdateConfig(pebblestore.VoiceConfigPatch{
		STTProfile:  patch.STTProfile,
		STTProvider: patch.STTProvider,
		STTModel:    patch.STTModel,
		STTLanguage: patch.STTLanguage,
		DeviceID:    patch.DeviceID,
		TTSProfile:  patch.TTSProfile,
		TTSProvider: patch.TTSProvider,
		TTSVoice:    patch.TTSVoice,
	})
	if err != nil {
		return Status{}, err
	}
	return s.Status(ctx)
}

func (s *Service) Transcribe(ctx context.Context, input TranscribeInput) (TranscribeResult, error) {
	started := time.Now()
	if len(input.Audio) == 0 {
		return TranscribeResult{}, errors.New("audio payload is required")
	}
	if err := s.ensureDefaultProfiles(); err != nil {
		return TranscribeResult{}, err
	}
	record, err := s.getConfig()
	if err != nil {
		return TranscribeResult{}, err
	}
	selection, err := s.resolveSTTSelection(ctx, record, strings.TrimSpace(input.Profile), strings.TrimSpace(input.Provider))
	if err != nil {
		return TranscribeResult{}, err
	}
	modelID := firstNonEmpty(
		strings.TrimSpace(input.Model),
		strings.TrimSpace(selection.ModelHint),
		strings.TrimSpace(record.STTModel),
		strings.TrimSpace(selection.Adapter.DefaultSTTModel()),
	)
	language := firstNonEmpty(
		strings.TrimSpace(input.Language),
		strings.TrimSpace(selection.LanguageHint),
		strings.TrimSpace(record.STTLanguage),
	)
	result, err := selection.Adapter.Transcribe(ctx, AdapterTranscribeInput{
		Audio:    input.Audio,
		Model:    modelID,
		Language: language,
		Options:  copyStringMap(selection.Options),
	})
	if err != nil {
		return TranscribeResult{}, err
	}
	modelOut := strings.TrimSpace(result.Model)
	if modelOut == "" {
		modelOut = modelID
	}
	return TranscribeResult{
		PathID:     PathTranscribe,
		Profile:    selection.ProfileID,
		Provider:   selection.ProviderID,
		Model:      modelOut,
		Language:   language,
		Text:       strings.TrimSpace(result.Text),
		AudioBytes: len(input.Audio),
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

func (s *Service) TestSTT(ctx context.Context, input TestSTTInput) (TestSTTResult, error) {
	started := time.Now()
	seconds := input.Seconds
	if seconds <= 0 {
		seconds = 4
	}
	if seconds > 15 {
		seconds = 15
	}
	record, err := s.getConfig()
	if err != nil {
		return TestSTTResult{}, err
	}
	deviceID := firstNonEmpty(strings.TrimSpace(input.DeviceID), strings.TrimSpace(record.DeviceID))
	captured, err := recordInputAudio(ctx, deviceID, seconds)
	if err != nil {
		return TestSTTResult{}, err
	}
	transcribed, err := s.Transcribe(ctx, TranscribeInput{
		Profile:  strings.TrimSpace(input.Profile),
		Provider: strings.TrimSpace(input.Provider),
		Model:    strings.TrimSpace(input.Model),
		Language: strings.TrimSpace(input.Language),
		Audio:    captured.Audio,
	})
	if err != nil {
		return TestSTTResult{}, err
	}
	return TestSTTResult{
		PathID:        PathTestSTT,
		Profile:       transcribed.Profile,
		Provider:      transcribed.Provider,
		Model:         transcribed.Model,
		Language:      transcribed.Language,
		Text:          transcribed.Text,
		Seconds:       seconds,
		DeviceID:      strings.TrimSpace(deviceID),
		RecordBackend: strings.TrimSpace(captured.Backend),
		AudioBytes:    len(captured.Audio),
		DurationMS:    time.Since(started).Milliseconds(),
	}, nil
}

func (s *Service) Synthesize(ctx context.Context, input SynthesizeInput) (SynthesizeResult, error) {
	started := time.Now()
	record, err := s.getConfig()
	if err != nil {
		return SynthesizeResult{}, err
	}

	providerID := normalizeProviderID(input.Provider)
	voiceHint := ""
	if providerID == "" {
		profileID := normalizeProfileID(record.TTSProfile)
		if profileID != "" && s.store != nil {
			if profile, ok, profileErr := s.store.GetProfile(profileID); profileErr == nil && ok {
				providerID = normalizeProviderID(profile.Adapter)
				voiceHint = strings.TrimSpace(profile.TTSVoice)
			}
		}
	}
	providerID = firstNonEmpty(providerID, normalizeProviderID(record.TTSProvider))
	if providerID == "" {
		return SynthesizeResult{}, errors.New("tts provider is not configured")
	}
	adapter, ok := s.adapters[providerID]
	if !ok || adapter == nil {
		return SynthesizeResult{}, fmt.Errorf("unknown tts provider %q", providerID)
	}
	configured, reason, err := adapter.TTSReady(ctx)
	if err != nil {
		return SynthesizeResult{}, err
	}
	if !configured {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "tts is not ready"
		}
		return SynthesizeResult{}, errors.New(reason)
	}

	modelID := strings.TrimSpace(input.Model)
	voiceID := firstNonEmpty(strings.TrimSpace(input.Voice), voiceHint, strings.TrimSpace(record.TTSVoice))
	result, err := adapter.Synthesize(ctx, AdapterSynthesizeInput{
		Text:  strings.TrimSpace(input.Text),
		Voice: voiceID,
		Model: modelID,
	})
	if err != nil {
		return SynthesizeResult{}, err
	}
	return SynthesizeResult{
		PathID:     PathSynthesize,
		Provider:   providerID,
		Model:      firstNonEmpty(strings.TrimSpace(result.Model), modelID),
		Voice:      firstNonEmpty(strings.TrimSpace(result.Voice), voiceID),
		Mime:       firstNonEmpty(strings.TrimSpace(result.Mime), "audio/wav"),
		Audio:      append([]byte(nil), result.Audio...),
		AudioBytes: len(result.Audio),
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}

func (s *Service) listProfiles(ctx context.Context) ([]VoiceProfileStatus, error) {
	if s.store == nil {
		return nil, nil
	}
	records, err := s.store.ListProfiles(500)
	if err != nil {
		return nil, err
	}
	cfg, err := s.getConfig()
	if err != nil {
		return nil, err
	}
	activeSTT := normalizeProfileID(cfg.STTProfile)
	activeTTS := normalizeProfileID(cfg.TTSProfile)
	out := make([]VoiceProfileStatus, 0, len(records))
	for _, record := range records {
		out = append(out, s.mapProfileStatus(ctx, record, activeSTT, activeTTS))
	}
	return out, nil
}

func (s *Service) mapProfileStatus(ctx context.Context, record pebblestore.VoiceProfileRecord, activeSTT, activeTTS string) VoiceProfileStatus {
	status := VoiceProfileStatus{
		ID:          normalizeProfileID(record.ID),
		Label:       strings.TrimSpace(record.Label),
		Adapter:     normalizeProviderID(record.Adapter),
		STTModel:    strings.TrimSpace(record.STTModel),
		STTLanguage: strings.TrimSpace(record.STTLanguage),
		TTSVoice:    strings.TrimSpace(record.TTSVoice),
		Options:     copyStringMap(record.Options),
		UpdatedAt:   record.UpdatedAt,
		ActiveSTT:   strings.EqualFold(normalizeProfileID(record.ID), activeSTT),
		ActiveTTS:   strings.EqualFold(normalizeProfileID(record.ID), activeTTS),
	}
	adapter := s.adapters[status.Adapter]
	if adapter == nil {
		status.AdapterConfigured = false
		if status.Adapter == "" {
			status.AdapterReason = "profile adapter is missing"
		} else {
			status.AdapterReason = "profile adapter is not registered"
		}
		return status
	}
	configured, reason, err := adapter.STTReady(ctx)
	if err != nil {
		status.AdapterConfigured = false
		status.AdapterReason = strings.TrimSpace(err.Error())
		return status
	}
	status.AdapterConfigured = configured
	status.AdapterReason = strings.TrimSpace(reason)
	return status
}

func (s *Service) listTTSProviders(ctx context.Context) ([]TTSProviderStatus, error) {
	statuses := make([]TTSProviderStatus, 0, len(s.order))
	for _, id := range s.sortedProviderOrder() {
		adapter, ok := s.adapters[id]
		if !ok || adapter == nil {
			continue
		}
		configured, reason, err := adapter.TTSReady(ctx)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, TTSProviderStatus{
			ID:         id,
			Configured: configured,
			Reason:     strings.TrimSpace(reason),
		})
	}
	return statuses, nil
}

func (s *Service) sortedProviderOrder() []string {
	if len(s.order) == 0 {
		return nil
	}
	out := append([]string(nil), s.order...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

func (s *Service) resolveSTTSelection(ctx context.Context, record pebblestore.VoiceConfigRecord, profileOverride, providerOverride string) (resolvedSTTSelection, error) {
	providerOverride = normalizeProviderID(providerOverride)
	if providerOverride != "" {
		adapter, ok := s.adapters[providerOverride]
		if !ok || adapter == nil {
			return resolvedSTTSelection{}, fmt.Errorf("unknown stt provider %q", providerOverride)
		}
		configured, reason, err := adapter.STTReady(ctx)
		if err != nil {
			return resolvedSTTSelection{}, err
		}
		if !configured {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "stt provider is not configured"
			}
			return resolvedSTTSelection{}, errors.New(reason)
		}
		return resolvedSTTSelection{Adapter: adapter, ProviderID: providerOverride}, nil
	}

	profileID := firstNonEmpty(normalizeProfileID(profileOverride), normalizeProfileID(record.STTProfile))
	if profileID != "" {
		if s.store == nil {
			return resolvedSTTSelection{}, errors.New("voice profile store is not configured")
		}
		profile, ok, err := s.store.GetProfile(profileID)
		if err != nil {
			return resolvedSTTSelection{}, err
		}
		if !ok {
			return resolvedSTTSelection{}, fmt.Errorf("stt profile %q not found", profileID)
		}
		adapterID := normalizeProviderID(profile.Adapter)
		adapter, ok := s.adapters[adapterID]
		if !ok || adapter == nil {
			return resolvedSTTSelection{}, fmt.Errorf("stt profile %q uses unknown adapter %q", profileID, adapterID)
		}
		configured, reason, err := adapter.STTReady(ctx)
		if err != nil {
			return resolvedSTTSelection{}, err
		}
		if !configured {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "stt provider is not configured"
			}
			return resolvedSTTSelection{}, errors.New(reason)
		}
		return resolvedSTTSelection{
			Adapter:      adapter,
			ProviderID:   adapterID,
			ProfileID:    profileID,
			ModelHint:    strings.TrimSpace(profile.STTModel),
			LanguageHint: strings.TrimSpace(profile.STTLanguage),
			Options:      copyStringMap(profile.Options),
		}, nil
	}

	preferred := normalizeProviderID(record.STTProvider)
	if preferred != "" {
		adapter, ok := s.adapters[preferred]
		if !ok || adapter == nil {
			return resolvedSTTSelection{}, fmt.Errorf("unknown stt provider %q", preferred)
		}
		configured, reason, err := adapter.STTReady(ctx)
		if err != nil {
			return resolvedSTTSelection{}, err
		}
		if !configured {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				reason = "stt provider is not configured"
			}
			return resolvedSTTSelection{}, errors.New(reason)
		}
		return resolvedSTTSelection{Adapter: adapter, ProviderID: preferred}, nil
	}

	for _, id := range s.sortedProviderOrder() {
		adapter := s.adapters[id]
		if adapter == nil {
			continue
		}
		configured, _, err := adapter.STTReady(ctx)
		if err != nil {
			return resolvedSTTSelection{}, err
		}
		if configured {
			return resolvedSTTSelection{Adapter: adapter, ProviderID: id}, nil
		}
	}
	return resolvedSTTSelection{}, errors.New("no stt provider is configured; set /voice profile or run /auth")
}

func (s *Service) resolveConfiguredSTT(record pebblestore.VoiceConfigRecord, profiles []VoiceProfileStatus, providers []STTProviderStatus) (profileID, providerID, modelID string, configured bool, reason string) {
	providerByID := make(map[string]STTProviderStatus, len(providers))
	for _, provider := range providers {
		providerByID[provider.ID] = provider
	}

	selectedProfile := normalizeProfileID(record.STTProfile)
	if selectedProfile != "" {
		for _, profile := range profiles {
			if !strings.EqualFold(strings.TrimSpace(profile.ID), selectedProfile) {
				continue
			}
			providerID = normalizeProviderID(profile.Adapter)
			modelID = firstNonEmpty(strings.TrimSpace(record.STTModel), strings.TrimSpace(profile.STTModel))
			if provider, ok := providerByID[providerID]; ok {
				configured = provider.Configured && profile.AdapterConfigured
				reason = firstNonEmpty(strings.TrimSpace(profile.AdapterReason), strings.TrimSpace(provider.Reason))
				if modelID == "" {
					modelID = strings.TrimSpace(provider.DefaultModel)
				}
				if configured {
					reason = ""
				}
				return selectedProfile, providerID, modelID, configured, reason
			}
			configured = profile.AdapterConfigured
			reason = firstNonEmpty(strings.TrimSpace(profile.AdapterReason), "selected stt profile adapter is not registered")
			if configured {
				reason = ""
			}
			return selectedProfile, providerID, modelID, configured, reason
		}
		return selectedProfile, "", strings.TrimSpace(record.STTModel), false, "selected stt profile not found"
	}

	selectedProvider := normalizeProviderID(record.STTProvider)
	if selectedProvider != "" {
		if provider, ok := providerByID[selectedProvider]; ok {
			modelID = firstNonEmpty(strings.TrimSpace(record.STTModel), strings.TrimSpace(provider.DefaultModel))
			return "", provider.ID, modelID, provider.Configured, strings.TrimSpace(provider.Reason)
		}
		return "", selectedProvider, strings.TrimSpace(record.STTModel), false, "selected stt provider is not registered"
	}

	for _, provider := range providers {
		if provider.Configured {
			modelID = firstNonEmpty(strings.TrimSpace(record.STTModel), strings.TrimSpace(provider.DefaultModel))
			return "", provider.ID, modelID, true, ""
		}
	}
	if len(providers) == 0 {
		return "", "", strings.TrimSpace(record.STTModel), false, "no stt providers registered"
	}
	return "", "", strings.TrimSpace(record.STTModel), false, "no configured stt provider"
}

func (s *Service) resolveConfiguredTTSProvider(record pebblestore.VoiceConfigRecord, profiles []VoiceProfileStatus, providers []TTSProviderStatus) (string, bool, string) {
	selected := normalizeProviderID(record.TTSProvider)
	if selected == "" {
		selectedProfile := normalizeProfileID(record.TTSProfile)
		if selectedProfile != "" {
			for _, profile := range profiles {
				if strings.EqualFold(strings.TrimSpace(profile.ID), selectedProfile) {
					selected = normalizeProviderID(profile.Adapter)
					break
				}
			}
		}
	}
	if selected != "" {
		for _, provider := range providers {
			if provider.ID == selected {
				return provider.ID, provider.Configured, strings.TrimSpace(provider.Reason)
			}
		}
		return selected, false, "selected tts provider is not registered"
	}
	for _, provider := range providers {
		if provider.Configured {
			return provider.ID, true, ""
		}
	}
	if len(providers) == 0 {
		return "", false, "no tts providers registered"
	}
	return "", false, "no configured tts provider"
}

func (s *Service) ensureDefaultProfiles() error {
	if s == nil || s.store == nil {
		return nil
	}
	if _, ok := s.adapters["whisper-local"]; !ok {
		return nil
	}
	if _, ok, err := s.store.GetProfile("whisper-local"); err != nil {
		return err
	} else if !ok {
		if _, err := s.store.PutProfile(pebblestore.VoiceProfileRecord{
			ID:      "whisper-local",
			Label:   "Local Whisper",
			Adapter: "whisper-local",
		}); err != nil {
			return err
		}
	}
	record, err := s.getConfig()
	if err != nil {
		return err
	}
	if normalizeProfileID(record.STTProfile) == "" && normalizeProviderID(record.STTProvider) == "" {
		defaultProfile := "whisper-local"
		if _, err := s.store.UpdateConfig(pebblestore.VoiceConfigPatch{STTProfile: &defaultProfile}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) getConfig() (pebblestore.VoiceConfigRecord, error) {
	if s == nil || s.store == nil {
		return pebblestore.VoiceConfigRecord{}, nil
	}
	record, _, err := s.store.GetConfig()
	if err != nil {
		return pebblestore.VoiceConfigRecord{}, err
	}
	return record, nil
}

func voiceConfigFromRecord(record pebblestore.VoiceConfigRecord) VoiceConfig {
	return VoiceConfig{
		STTProfile:  strings.TrimSpace(record.STTProfile),
		STTProvider: strings.TrimSpace(record.STTProvider),
		STTModel:    strings.TrimSpace(record.STTModel),
		STTLanguage: strings.TrimSpace(record.STTLanguage),
		DeviceID:    strings.TrimSpace(record.DeviceID),
		TTSProfile:  strings.TrimSpace(record.TTSProfile),
		TTSProvider: strings.TrimSpace(record.TTSProvider),
		TTSVoice:    strings.TrimSpace(record.TTSVoice),
		UpdatedAt:   record.UpdatedAt,
	}
}

func normalizeProviderID(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "auto", "default", "none", "off", "clear":
		return ""
	default:
		return value
	}
}

func normalizeProfileID(raw string) string {
	id := strings.ToLower(strings.TrimSpace(raw))
	switch id {
	case "", "auto", "default", "none", "off", "clear":
		return ""
	}
	id = strings.ReplaceAll(id, " ", "-")
	builder := strings.Builder{}
	builder.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		}
	}
	return strings.Trim(builder.String(), "-_.")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		cleanKey := strings.TrimSpace(key)
		cleanValue := strings.TrimSpace(value)
		if cleanKey == "" || cleanValue == "" {
			continue
		}
		out[cleanKey] = cleanValue
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
