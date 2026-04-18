package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type VoiceConfigRecord struct {
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

type VoiceConfigPatch struct {
	STTProfile  *string
	STTProvider *string
	STTModel    *string
	STTLanguage *string
	DeviceID    *string
	TTSProfile  *string
	TTSProvider *string
	TTSVoice    *string
}

type VoiceProfileRecord struct {
	ID          string            `json:"id"`
	Label       string            `json:"label,omitempty"`
	Adapter     string            `json:"adapter"`
	STTModel    string            `json:"stt_model,omitempty"`
	STTLanguage string            `json:"stt_language,omitempty"`
	TTSVoice    string            `json:"tts_voice,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
	UpdatedAt   int64             `json:"updated_at"`
}

type VoiceStore struct {
	store *Store
}

func NewVoiceStore(store *Store) *VoiceStore {
	return &VoiceStore{store: store}
}

func (s *VoiceStore) GetConfig() (VoiceConfigRecord, bool, error) {
	if s == nil || s.store == nil {
		return VoiceConfigRecord{}, false, nil
	}
	var record VoiceConfigRecord
	ok, err := s.store.GetJSON(KeyVoiceConfigDefault, &record)
	if err != nil {
		return VoiceConfigRecord{}, false, err
	}
	if !ok {
		return VoiceConfigRecord{}, false, nil
	}
	return normalizeVoiceConfigRecord(record), true, nil
}

func (s *VoiceStore) UpdateConfig(patch VoiceConfigPatch) (VoiceConfigRecord, error) {
	record, _, err := s.GetConfig()
	if err != nil {
		return VoiceConfigRecord{}, err
	}
	record = normalizeVoiceConfigRecord(record)

	if patch.STTProfile != nil {
		record.STTProfile = normalizeVoiceProfileID(*patch.STTProfile)
	}
	if patch.STTProvider != nil {
		record.STTProvider = normalizeVoiceConfigValue(*patch.STTProvider)
	}
	if patch.STTModel != nil {
		record.STTModel = strings.TrimSpace(*patch.STTModel)
	}
	if patch.STTLanguage != nil {
		record.STTLanguage = strings.TrimSpace(*patch.STTLanguage)
	}
	if patch.DeviceID != nil {
		record.DeviceID = strings.TrimSpace(*patch.DeviceID)
	}
	if patch.TTSProfile != nil {
		record.TTSProfile = normalizeVoiceProfileID(*patch.TTSProfile)
	}
	if patch.TTSProvider != nil {
		record.TTSProvider = normalizeVoiceConfigValue(*patch.TTSProvider)
	}
	if patch.TTSVoice != nil {
		record.TTSVoice = strings.TrimSpace(*patch.TTSVoice)
	}
	record.UpdatedAt = time.Now().UnixMilli()
	record = normalizeVoiceConfigRecord(record)
	if err := s.store.PutJSON(KeyVoiceConfigDefault, record); err != nil {
		return VoiceConfigRecord{}, err
	}
	return record, nil
}

func (s *VoiceStore) GetProfile(profileID string) (VoiceProfileRecord, bool, error) {
	if s == nil || s.store == nil {
		return VoiceProfileRecord{}, false, nil
	}
	profileID = normalizeVoiceProfileID(profileID)
	if profileID == "" {
		return VoiceProfileRecord{}, false, errors.New("voice profile id is required")
	}
	var record VoiceProfileRecord
	ok, err := s.store.GetJSON(KeyVoiceProfile(profileID), &record)
	if err != nil {
		return VoiceProfileRecord{}, false, err
	}
	if !ok {
		return VoiceProfileRecord{}, false, nil
	}
	record = normalizeVoiceProfileRecord(record)
	if record.ID == "" {
		record.ID = profileID
	}
	return record, true, nil
}

func (s *VoiceStore) PutProfile(profile VoiceProfileRecord) (VoiceProfileRecord, error) {
	if s == nil || s.store == nil {
		return VoiceProfileRecord{}, errors.New("voice store is not configured")
	}
	profile = normalizeVoiceProfileRecord(profile)
	if profile.ID == "" {
		return VoiceProfileRecord{}, errors.New("voice profile id is required")
	}
	if profile.Adapter == "" {
		return VoiceProfileRecord{}, errors.New("voice profile adapter is required")
	}
	profile.UpdatedAt = time.Now().UnixMilli()
	if err := s.store.PutJSON(KeyVoiceProfile(profile.ID), profile); err != nil {
		return VoiceProfileRecord{}, err
	}
	return profile, nil
}

func (s *VoiceStore) DeleteProfile(profileID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	profileID = normalizeVoiceProfileID(profileID)
	if profileID == "" {
		return errors.New("voice profile id is required")
	}
	return s.store.Delete(KeyVoiceProfile(profileID))
}

func (s *VoiceStore) ListProfiles(limit int) ([]VoiceProfileRecord, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]VoiceProfileRecord, 0, minVoiceInt(limit, 32))
	err := s.store.IteratePrefix(VoiceProfilePrefix(), limit, func(key string, value []byte) error {
		var record VoiceProfileRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("decode voice profile: %w", err)
		}
		record = normalizeVoiceProfileRecord(record)
		if record.ID == "" {
			record.ID = decodeVoiceProfileIDFromKey(key)
		}
		if record.ID == "" {
			return nil
		}
		out = append(out, record)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].ID))
		right := strings.ToLower(strings.TrimSpace(out[j].ID))
		if left == right {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return left < right
	})
	return out, nil
}

func normalizeVoiceConfigRecord(record VoiceConfigRecord) VoiceConfigRecord {
	record.STTProfile = normalizeVoiceProfileID(record.STTProfile)
	record.STTProvider = normalizeVoiceConfigValue(record.STTProvider)
	record.STTModel = strings.TrimSpace(record.STTModel)
	record.STTLanguage = strings.TrimSpace(record.STTLanguage)
	record.DeviceID = strings.TrimSpace(record.DeviceID)
	record.TTSProfile = normalizeVoiceProfileID(record.TTSProfile)
	record.TTSProvider = normalizeVoiceConfigValue(record.TTSProvider)
	record.TTSVoice = strings.TrimSpace(record.TTSVoice)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeVoiceProfileRecord(record VoiceProfileRecord) VoiceProfileRecord {
	record.ID = normalizeVoiceProfileID(record.ID)
	record.Label = strings.TrimSpace(record.Label)
	record.Adapter = normalizeVoiceConfigValue(record.Adapter)
	record.STTModel = strings.TrimSpace(record.STTModel)
	record.STTLanguage = strings.TrimSpace(record.STTLanguage)
	record.TTSVoice = strings.TrimSpace(record.TTSVoice)
	record.Options = normalizeVoiceProfileOptions(record.Options)
	if record.UpdatedAt < 0 {
		record.UpdatedAt = 0
	}
	return record
}

func normalizeVoiceProfileOptions(options map[string]string) map[string]string {
	if len(options) == 0 {
		return nil
	}
	out := make(map[string]string, len(options))
	for key, value := range options {
		cleanKey := strings.ToLower(strings.TrimSpace(key))
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

func normalizeVoiceProfileID(raw string) string {
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

func normalizeVoiceConfigValue(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "auto", "default", "none", "off", "clear":
		return ""
	default:
		return value
	}
}

func decodeVoiceProfileIDFromKey(key string) string {
	if !strings.HasPrefix(key, VoiceProfilePrefix()) {
		return ""
	}
	raw := strings.TrimPrefix(key, VoiceProfilePrefix())
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		decoded = raw
	}
	return normalizeVoiceProfileID(decoded)
}

func minVoiceInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
