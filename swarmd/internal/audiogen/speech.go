package audiogen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/videotranscription"
)

// SpeechProvider is generation-only. Providers may supply native alignment;
// otherwise the caller must align the generated audio, never the input script.
type SpeechProvider interface {
	GenerateSpeech(context.Context, SpeechRequest) (SpeechResult, error)
}
type SpeechRequest struct {
	Principal identity.Principal
	Model     string
	Text      string
	Voice     string
	Style     string
	Language  string
	Format    string
}
type SpeechResult struct {
	Bytes           []byte
	MediaType       string
	Model           string
	DurationSeconds float64
	PromptTokens    int64
	AudioTokens     int64
	Timings         *SpeechTimings
}
type SpeechTiming struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
type SpeechTimings struct {
	Granularity string         `json:"granularity"`
	Source      string         `json:"source"`
	Words       []SpeechTiming `json:"words,omitempty"`
	Segments    []SpeechTiming `json:"segments,omitempty"`
}

// The byte ceiling conservatively bounds the complete prompt below the 8192
// token input limit even for scripts whose bytes tokenize individually.
const SpeechMaxPromptBytes = 8000

func SpeechModel(model string) bool {
	model = strings.TrimPrefix(model, "google/")
	return model == "gemini-2.5-flash-preview-tts" || model == "gemini-2.5-pro-preview-tts"
}
func SpeechVoices() []string {
	return strings.Fields("Zephyr Puck Charon Kore Fenrir Leda Orus Aoede Callirrhoe Autonoe Enceladus Iapetus Umbriel Algieba Despina Erinome Algenib Rasalgethi Laomedeia Achernar Alnilam Schedar Gacrux Pulcherrima Achird Zubenelgenubi Vindemiatrix Sadachbia Sadaltager Sulafat")
}
func speechPrompt(r SpeechRequest) string {
	if r.Style == "" && r.Language == "" {
		return r.Text
	}
	return "Direction: " + r.Style + "\nLanguage: " + r.Language + "\nRead the following script aloud:\n" + r.Text
}
func ValidateSpeech(r SpeechRequest) error {
	if !SpeechModel(r.Model) {
		return errors.New("speech requires a supported explicit Google TTS model")
	}
	if strings.TrimSpace(r.Text) == "" || len(speechPrompt(r)) > SpeechMaxPromptBytes {
		return fmt.Errorf("speech requires text and a complete prompt of at most %d UTF-8 bytes", SpeechMaxPromptBytes)
	}
	found := false
	for _, v := range SpeechVoices() {
		found = found || v == r.Voice
	}
	if !found {
		return errors.New("unsupported speech voice; consult swarm_media_models")
	}
	if r.Format != "" && r.Format != "wav" {
		return errors.New("Gemini speech supports format wav only (native PCM wrapped as WAV)")
	}
	return nil
}

func (s *Service) GenerateSpeech(ctx context.Context, r SpeechRequest) (SpeechResult, error) {
	if err := ValidateSpeech(r); err != nil {
		return SpeechResult{}, err
	}
	if s == nil {
		return SpeechResult{}, errors.New("audio service unavailable")
	}
	key, err := s.getGoogleAPIKey(r.Principal.AccountScopeID)
	if err != nil {
		return SpeechResult{}, err
	}
	select {
	case s.managedSlots <- struct{}{}:
		defer func() { <-s.managedSlots }()
	case <-ctx.Done():
		return SpeechResult{}, ctx.Err()
	}
	if err := provideriface.CheckBillableDispatch(ctx); err != nil {
		return SpeechResult{}, err
	}
	return s.generateGoogleSpeech(ctx, key, r)
}
func (s *Service) generateGoogleSpeech(ctx context.Context, key string, r SpeechRequest) (SpeechResult, error) {
	if err := ValidateSpeech(r); err != nil {
		return SpeechResult{}, err
	}
	model := strings.TrimPrefix(r.Model, "google/")
	body := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": speechPrompt(r)}}}}, "generationConfig": map[string]any{"responseModalities": []string{"AUDIO"}, "speechConfig": map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]string{"voiceName": r.Voice}}}}}
	payload, err := json.Marshal(body)
	if err != nil {
		return SpeechResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.googleURL()+"/v1beta/models/"+model+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return SpeechResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", key)
	client := *s.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return SpeechResult{}, errors.New("speech provider request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return SpeechResult{}, fmt.Errorf("speech provider returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil || len(data) > 32<<20 {
		return SpeechResult{}, errors.New("speech response exceeds limit or could not be read")
	}
	var wire struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		Usage struct {
			Prompt int64 `json:"promptTokenCount"`
			Output int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return SpeechResult{}, errors.New("invalid speech response")
	}
	if len(wire.Candidates) != 1 || wire.Candidates[0].FinishReason != "STOP" {
		return SpeechResult{}, errors.New("speech generation incomplete or blocked")
	}
	var pcm []byte
	for _, part := range wire.Candidates[0].Content.Parts {
		if part.InlineData.Data == "" {
			continue
		}
		if !strings.EqualFold(part.InlineData.MIMEType, "audio/L16;codec=pcm;rate=24000") {
			return SpeechResult{}, errors.New("unsupported speech PCM format")
		}
		chunk, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
		if err != nil {
			return SpeechResult{}, errors.New("invalid speech audio encoding")
		}
		pcm = append(pcm, chunk...)
	}
	if len(pcm) == 0 || len(pcm)%2 != 0 {
		return SpeechResult{}, errors.New("empty or invalid speech PCM")
	}
	wav := make([]byte, 44+len(pcm))
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(36+len(pcm)))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 24000)
	binary.LittleEndian.PutUint32(wav[28:], 48000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(pcm)))
	copy(wav[44:], pcm)
	return SpeechResult{Bytes: wav, MediaType: "audio/wav", Model: model, DurationSeconds: float64(len(pcm)) / 48000, PromptTokens: wire.Usage.Prompt, AudioTokens: wire.Usage.Output}, nil
}

func TimingsFromTranscript(t videotranscription.GeneratedTranscript) (SpeechTimings, error) {
	normalized, err := videotranscription.NormalizeGeneratedTranscript(t)
	if err != nil {
		return SpeechTimings{}, err
	}
	if normalized.Partial {
		return SpeechTimings{}, errors.New("invalid or partial transcript timings")
	}
	result := SpeechTimings{Granularity: "segment", Source: "transcription"}
	for _, w := range normalized.Words {
		result.Words = append(result.Words, SpeechTiming{w.Text, float64(w.StartMs) / 1000, float64(w.EndMs) / 1000})
	}
	if len(result.Words) > 0 {
		result.Granularity = "word"
	} else {
		for _, segment := range normalized.Segments {
			if segment.Speech != "" {
				result.Segments = append(result.Segments, SpeechTiming{segment.Speech, float64(segment.StartMs) / 1000, float64(segment.EndMs) / 1000})
			}
		}
	}
	if len(result.Words)+len(result.Segments) == 0 {
		return SpeechTimings{}, errors.New("transcription returned no speech timings")
	}
	return result, nil
}
