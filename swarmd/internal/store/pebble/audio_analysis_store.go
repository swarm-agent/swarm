package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	AudioAnalysisSchemaVersion = "audio_analysis.v1"
	maxAudioAnalysisLevels     = 50_000
	maxAudioAnalysisOnsets     = 50_000
	maxAudioAnalysisBeats      = 50_000
	maxAudioAnalysisSections   = 1_000
)

type AudioAnalysisLevel struct {
	StartMs int64   `json:"start_ms"`
	EndMs   int64   `json:"end_ms"`
	RMS     float64 `json:"rms"`
	Peak    float64 `json:"peak"`
}

type AudioAnalysisOnset struct {
	TimeMs   int64   `json:"time_ms"`
	Strength float64 `json:"strength"`
}

type AudioAnalysisBeat struct {
	TimeMs     int64   `json:"time_ms"`
	Confidence float64 `json:"confidence"`
	BarBeat    int     `json:"bar_beat,omitempty"`
}

type AudioAnalysisTempo struct {
	BPM        float64 `json:"bpm"`
	Confidence float64 `json:"confidence"`
}

type AudioAnalysisSection struct {
	StartMs    int64   `json:"start_ms"`
	EndMs      int64   `json:"end_ms"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

// AudioAnalysisSnapshot is bounded deterministic DSP output. It deliberately
// contains no raw PCM, source path, provider payload, or model-generated timing.
type AudioAnalysisSnapshot struct {
	SchemaVersion     string                 `json:"schema_version"`
	Ref               string                 `json:"ref"`
	AccountScopeID    string                 `json:"account_scope_id"`
	WorkspaceID       string                 `json:"workspace_id"`
	SourceRef         string                 `json:"source_ref"`
	SourceFingerprint string                 `json:"source_fingerprint"`
	AnalyzerVersion   string                 `json:"analyzer_version"`
	DurationMs        int64                  `json:"duration_ms"`
	SampleIntervalMs  int64                  `json:"sample_interval_ms"`
	Levels            []AudioAnalysisLevel   `json:"levels"`
	Onsets            []AudioAnalysisOnset   `json:"onsets,omitempty"`
	Tempo             *AudioAnalysisTempo    `json:"tempo,omitempty"`
	Beats             []AudioAnalysisBeat    `json:"beats,omitempty"`
	Sections          []AudioAnalysisSection `json:"sections,omitempty"`
	ContentDigest     string                 `json:"content_digest"`
	CreatedAt         int64                  `json:"created_at"`
}

func KeyAudioAnalysisSnapshot(accountScopeID, workspaceID, sourceFingerprint, analyzerVersion string) string {
	return fmt.Sprintf("v3/audio_analysis/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(workspaceID), keyPart(sourceFingerprint), keyPart(analyzerVersion))
}

func (s *SessionStore) PutAudioAnalysisSnapshot(snapshot AudioAnalysisSnapshot) (AudioAnalysisSnapshot, bool, error) {
	if s == nil || s.store == nil {
		return AudioAnalysisSnapshot{}, false, errors.New("session store is not configured")
	}
	normalized, err := normalizeAudioAnalysisSnapshot(snapshot)
	if err != nil {
		return AudioAnalysisSnapshot{}, false, err
	}
	source, found, err := s.GetAudioSourceRecord(normalized.AccountScopeID, normalized.WorkspaceID, normalized.SourceRef)
	if err != nil || !found {
		if err == nil {
			err = errors.New("audio analysis source is not registered in authenticated workspace scope")
		}
		return AudioAnalysisSnapshot{}, false, err
	}
	if source.SourceFingerprint != normalized.SourceFingerprint || source.FingerprintVersion != AudioSourceFingerprintV1 {
		return AudioAnalysisSnapshot{}, false, errors.New("audio analysis source fingerprint is stale")
	}
	if err := ValidateAudioSourceRecord(source); err != nil {
		return AudioAnalysisSnapshot{}, false, err
	}
	key := KeyAudioAnalysisSnapshot(normalized.AccountScopeID, normalized.WorkspaceID, normalized.SourceFingerprint, normalized.AnalyzerVersion)
	var existing AudioAnalysisSnapshot
	if ok, readErr := s.store.GetJSON(key, &existing); readErr != nil {
		return AudioAnalysisSnapshot{}, false, readErr
	} else if ok {
		existing, readErr = normalizeAudioAnalysisSnapshot(existing)
		if readErr != nil || existing.ContentDigest != normalized.ContentDigest {
			if readErr == nil {
				readErr = errors.New("immutable audio analysis authority collision")
			}
			return AudioAnalysisSnapshot{}, false, readErr
		}
		return existing, true, nil
	}
	if err := s.store.PutJSON(key, normalized); err != nil {
		return AudioAnalysisSnapshot{}, false, err
	}
	return normalized, false, nil
}

func (s *SessionStore) GetAudioAnalysisSnapshot(accountScopeID, workspaceID, sourceFingerprint, analyzerVersion string) (AudioAnalysisSnapshot, bool, error) {
	accountScopeID, workspaceID = strings.TrimSpace(accountScopeID), strings.TrimSpace(workspaceID)
	sourceFingerprint, analyzerVersion = strings.ToLower(strings.TrimSpace(sourceFingerprint)), strings.TrimSpace(analyzerVersion)
	if accountScopeID == "" || workspaceID == "" || !validFingerprint(sourceFingerprint) || analyzerVersion == "" {
		return AudioAnalysisSnapshot{}, false, errors.New("valid audio analysis account, workspace, fingerprint, and analyzer version are required")
	}
	var snapshot AudioAnalysisSnapshot
	ok, err := s.store.GetJSON(KeyAudioAnalysisSnapshot(accountScopeID, workspaceID, sourceFingerprint, analyzerVersion), &snapshot)
	if err != nil || !ok {
		return AudioAnalysisSnapshot{}, ok, err
	}
	if snapshot.AccountScopeID != accountScopeID || snapshot.WorkspaceID != workspaceID || snapshot.SourceFingerprint != sourceFingerprint || snapshot.AnalyzerVersion != analyzerVersion {
		return AudioAnalysisSnapshot{}, false, errors.New("audio analysis ownership metadata is inconsistent")
	}
	source, found, err := s.GetAudioSourceRecord(accountScopeID, workspaceID, snapshot.SourceRef)
	if err != nil || !found {
		if err == nil {
			err = errors.New("audio analysis source is unavailable in authenticated workspace scope")
		}
		return AudioAnalysisSnapshot{}, false, err
	}
	if source.SourceFingerprint != snapshot.SourceFingerprint {
		return AudioAnalysisSnapshot{}, false, errors.New("audio analysis source fingerprint is stale")
	}
	if err := ValidateAudioSourceRecord(source); err != nil {
		return AudioAnalysisSnapshot{}, false, err
	}
	originalDigest := snapshot.ContentDigest
	snapshot, err = normalizeAudioAnalysisSnapshot(snapshot)
	if err != nil || snapshot.ContentDigest != originalDigest {
		if err == nil {
			err = errors.New("audio analysis content digest mismatch")
		}
		return AudioAnalysisSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func normalizeAudioAnalysisSnapshot(snapshot AudioAnalysisSnapshot) (AudioAnalysisSnapshot, error) {
	snapshot.SchemaVersion = strings.TrimSpace(snapshot.SchemaVersion)
	snapshot.AccountScopeID = strings.TrimSpace(snapshot.AccountScopeID)
	snapshot.WorkspaceID = strings.TrimSpace(snapshot.WorkspaceID)
	snapshot.SourceRef = strings.TrimSpace(snapshot.SourceRef)
	snapshot.SourceFingerprint = strings.ToLower(strings.TrimSpace(snapshot.SourceFingerprint))
	snapshot.AnalyzerVersion = strings.TrimSpace(snapshot.AnalyzerVersion)
	if snapshot.SchemaVersion == "" {
		snapshot.SchemaVersion = AudioAnalysisSchemaVersion
	}
	if snapshot.SchemaVersion != AudioAnalysisSchemaVersion || snapshot.AccountScopeID == "" || snapshot.WorkspaceID == "" || !strings.HasPrefix(snapshot.SourceRef, "audiosrc_") || !validFingerprint(snapshot.SourceFingerprint) || snapshot.AnalyzerVersion == "" || len(snapshot.AnalyzerVersion) > 128 || snapshot.DurationMs <= 0 || snapshot.SampleIntervalMs <= 0 || snapshot.CreatedAt <= 0 {
		return AudioAnalysisSnapshot{}, errors.New("audio analysis identity and timing metadata are invalid")
	}
	if len(snapshot.Levels) == 0 || len(snapshot.Levels) > maxAudioAnalysisLevels || len(snapshot.Onsets) > maxAudioAnalysisOnsets || len(snapshot.Beats) > maxAudioAnalysisBeats || len(snapshot.Sections) > maxAudioAnalysisSections {
		return AudioAnalysisSnapshot{}, errors.New("audio analysis arrays exceed the bounded contract")
	}
	previousEnd := int64(0)
	for i, level := range snapshot.Levels {
		if level.StartMs < previousEnd || level.EndMs <= level.StartMs || level.EndMs > snapshot.DurationMs+snapshot.SampleIntervalMs || !unitFloat(level.RMS) || !unitFloat(level.Peak) || level.RMS > level.Peak {
			return AudioAnalysisSnapshot{}, fmt.Errorf("audio analysis level %d is invalid", i)
		}
		previousEnd = level.EndMs
	}
	previous := int64(-1)
	for i, onset := range snapshot.Onsets {
		if onset.TimeMs < 0 || onset.TimeMs > snapshot.DurationMs || onset.TimeMs < previous || !unitFloat(onset.Strength) {
			return AudioAnalysisSnapshot{}, fmt.Errorf("audio analysis onset %d is invalid", i)
		}
		previous = onset.TimeMs
	}
	if snapshot.Tempo != nil && (snapshot.Tempo.BPM <= 0 || snapshot.Tempo.BPM > 400 || !unitFloat(snapshot.Tempo.Confidence)) {
		return AudioAnalysisSnapshot{}, errors.New("audio analysis tempo is invalid")
	}
	previous = -1
	for i, beat := range snapshot.Beats {
		if beat.TimeMs < 0 || beat.TimeMs > snapshot.DurationMs || beat.TimeMs < previous || !unitFloat(beat.Confidence) || beat.BarBeat < 0 || beat.BarBeat > 32 {
			return AudioAnalysisSnapshot{}, fmt.Errorf("audio analysis beat %d is invalid", i)
		}
		previous = beat.TimeMs
	}
	previousEnd = 0
	for i := range snapshot.Sections {
		section := &snapshot.Sections[i]
		section.Label = strings.TrimSpace(section.Label)
		if section.StartMs < previousEnd || section.EndMs <= section.StartMs || section.EndMs > snapshot.DurationMs || section.Label == "" || len(section.Label) > 128 || !unitFloat(section.Confidence) {
			return AudioAnalysisSnapshot{}, fmt.Errorf("audio analysis section %d is invalid", i)
		}
		previousEnd = section.EndMs
	}
	identity := strings.Join([]string{snapshot.AccountScopeID, snapshot.WorkspaceID, snapshot.SourceFingerprint, snapshot.AnalyzerVersion}, "\x00")
	snapshot.Ref = "audanalysis_" + audioAnalysisDigest(identity)
	content := struct {
		SchemaVersion string                 `json:"schema_version"`
		SourceRef string                     `json:"source_ref"`
		SourceFingerprint string             `json:"source_fingerprint"`
		AnalyzerVersion string               `json:"analyzer_version"`
		DurationMs int64                     `json:"duration_ms"`
		SampleIntervalMs int64               `json:"sample_interval_ms"`
		Levels []AudioAnalysisLevel          `json:"levels"`
		Onsets []AudioAnalysisOnset          `json:"onsets,omitempty"`
		Tempo *AudioAnalysisTempo            `json:"tempo,omitempty"`
		Beats []AudioAnalysisBeat            `json:"beats,omitempty"`
		Sections []AudioAnalysisSection      `json:"sections,omitempty"`
	}{snapshot.SchemaVersion, snapshot.SourceRef, snapshot.SourceFingerprint, snapshot.AnalyzerVersion, snapshot.DurationMs, snapshot.SampleIntervalMs, snapshot.Levels, snapshot.Onsets, snapshot.Tempo, snapshot.Beats, snapshot.Sections}
	payload, err := json.Marshal(content)
	if err != nil {
		return AudioAnalysisSnapshot{}, err
	}
	snapshot.ContentDigest = audioAnalysisDigest(string(payload))
	return snapshot, nil
}

func unitFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func audioAnalysisDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
