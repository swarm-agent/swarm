package voice

import (
	"context"
	"errors"
	"strings"

	"swarm/packages/swarmd/internal/provider/codex"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type codexAdapter struct {
	authStore *pebblestore.AuthStore
	client    *codex.Client
}

func NewCodexAdapter(authStore *pebblestore.AuthStore, client *codex.Client) Adapter {
	return &codexAdapter{
		authStore: authStore,
		client:    client,
	}
}

func (a *codexAdapter) ID() string {
	return "codex"
}

func (a *codexAdapter) STTReady(context.Context) (bool, string, error) {
	record, ok, err := a.codexRecord()
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, "missing codex auth", nil
	}
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.CodexAuthTypeOAuth:
		return false, "codex stt requires API key auth; configure Codex API key in /auth", nil
	}
	if strings.TrimSpace(record.APIKey) == "" {
		return false, "codex stt requires API key auth; configure Codex API key in /auth", nil
	}
	return true, "", nil
}

func (a *codexAdapter) STTModels() []string {
	return []string{"gpt-4o-transcribe"}
}

func (a *codexAdapter) DefaultSTTModel() string {
	return "gpt-4o-transcribe"
}

func (a *codexAdapter) Transcribe(ctx context.Context, input AdapterTranscribeInput) (AdapterTranscribeResult, error) {
	if a.client == nil {
		return AdapterTranscribeResult{}, errors.New("codex client is not configured")
	}
	out, err := a.client.TranscribeAudio(ctx, input.Audio, strings.TrimSpace(input.Model), strings.TrimSpace(input.Language))
	if err != nil {
		return AdapterTranscribeResult{}, err
	}
	return AdapterTranscribeResult{
		Model: strings.TrimSpace(out.Model),
		Text:  strings.TrimSpace(out.Text),
	}, nil
}

func (a *codexAdapter) TTSReady(context.Context) (bool, string, error) {
	record, ok, err := a.codexRecord()
	if err != nil {
		return false, "", err
	}
	if !ok || !isCodexRecordReady(record) {
		return false, "missing codex auth", nil
	}
	return false, "codex tts is a placeholder in this build", nil
}

func (a *codexAdapter) Synthesize(context.Context, AdapterSynthesizeInput) (AdapterSynthesizeResult, error) {
	return AdapterSynthesizeResult{}, ErrTTSPlaceholder
}

func (a *codexAdapter) codexRecord() (pebblestore.CodexAuthRecord, bool, error) {
	if a == nil || a.authStore == nil {
		return pebblestore.CodexAuthRecord{}, false, nil
	}
	return a.authStore.GetCodexAuthRecord()
}

func isCodexRecordReady(record pebblestore.CodexAuthRecord) bool {
	switch strings.ToLower(strings.TrimSpace(record.Type)) {
	case pebblestore.CodexAuthTypeOAuth:
		return strings.TrimSpace(record.AccessToken) != "" && strings.TrimSpace(record.RefreshToken) != ""
	default:
		return strings.TrimSpace(record.APIKey) != ""
	}
}
