package videotranscription

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/uisettings"
)

type audioCatalog struct{}

func (audioCatalog) ListCatalog(string, int) ([]pebblestore.ModelCatalogRecord, error) {
	return []pebblestore.ModelCatalogRecord{{Model: "gemini-test", SourceSnapshotID: "snap", SourceSnapshotVersion: "1", CatalogModalities: pebblestore.ModelCatalogModalities{Inputs: []string{"video", "audio"}, Outputs: []string{"text"}}}}, nil
}

type audioSettings struct{ model string }

func (s audioSettings) GetForAccount(string) (uisettings.UISettings, error) {
	return uisettings.UISettings{Media: uisettings.MediaSettings{TranscriptionModel: s.model}}, nil
}

type audioModel struct {
	got    AudioAnalysisRequest
	answer GeneratedTranscript
}

func (m *audioModel) Transcribe(context.Context, TranscribeRequest) (GeneratedTranscript, error) {
	return GeneratedTranscript{}, nil
}

func (m *audioModel) AnalyzeFrameBatch(context.Context, FrameBatchRequest) ([]FrameObservation, error) {
	return nil, nil
}

func (m *audioModel) AnalyzeAudio(_ context.Context, request AudioAnalysisRequest) (GeneratedTranscript, error) {
	m.got = request
	return m.answer, nil
}

// A wav an agent hands over is prepared with ffmpeg and read by the account's
// transcription model; the answer comes back as text with its timed segments.
func TestTranscribeAudio(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	wav := filepath.Join(t.TempDir(), "tone.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", wav).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	audio, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}
	model := &audioModel{answer: GeneratedTranscript{Language: "en", Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 0, EndMs: 1000, Speech: "hello there"}}}}
	service := NewService(nil, audioCatalog{}, audioSettings{model: "gemini-test"}, model)
	transcript, used, err := service.TranscribeAudio(context.Background(), p, audio, "names")
	if err != nil {
		t.Fatal(err)
	}
	if used != "gemini-test" || model.got.Model != "gemini-test" || model.got.FocusNotes != "names" || model.got.MIMEType != "audio/flac" {
		t.Fatalf("model %q request %+v", used, model.got)
	}
	if !strings.Contains(transcript.Text, "hello there") || len(transcript.Segments) != 1 || transcript.DurationMs < 900 {
		t.Fatalf("transcript %+v", transcript)
	}
	// An answer that is not a timed transcript is refused, not passed on.
	model.answer = GeneratedTranscript{}
	if _, _, err := service.TranscribeAudio(context.Background(), p, audio, ""); err == nil || !strings.Contains(err.Error(), "timed transcript") {
		t.Fatalf("partial answer: %v", err)
	}
	// Without a transcription model chosen in Media settings it says so.
	service = NewService(nil, audioCatalog{}, audioSettings{}, model)
	if _, _, err := service.TranscribeAudio(context.Background(), p, audio, ""); err == nil || !strings.Contains(err.Error(), "Media settings") {
		t.Fatalf("no model: %v", err)
	}
}
