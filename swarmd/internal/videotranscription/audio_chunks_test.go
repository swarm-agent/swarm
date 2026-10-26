package videotranscription

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type chunkModel struct {
	audioModel
	calls    []AudioAnalysisRequest
	fail     int
	attempts map[string]int
}

func (m *chunkModel) AnalyzeAudio(_ context.Context, r AudioAnalysisRequest) (GeneratedTranscript, error) {
	m.calls = append(m.calls, r)
	m.attempts[r.PrivatePath]++
	if strings.Contains(r.PrivatePath, "chunk-1.flac") && m.attempts[r.PrivatePath] <= m.fail {
		return GeneratedTranscript{}, errors.New("malformed structured answer")
	}
	return GeneratedTranscript{DurationMs: r.DurationMs, Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 1000, EndMs: 2000, Speech: "hello"}}, Words: []pebblestore.NormalizedTranscriptWord{{StartMs: 1000, EndMs: 2000, Text: "hello"}}}, nil
}

// Purpose: TranscribeAudio must bound long model inputs, offset every successful
// chunk and preserve gaps after bounded retries. Real ffmpeg plus a stub adapter
// exercises the service boundary without provider calls or credentials.
func TestTranscribeAudioLongChunks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "long.wav")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=155", path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	audio, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []int{0, 1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			model := &chunkModel{fail: fail, attempts: map[string]int{}}
			service := NewService(nil, audioCatalog{}, audioSettings{model: "gemini-test"}, model)
			got, used, err := service.TranscribeAudio(ctx, identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "account", UserID: "user"}, audio, "names")
			if err != nil {
				t.Fatal(err)
			}
			if used != "gemini-test" || got.DurationMs != 155000 {
				t.Fatalf("model/duration: %s %+v", used, got)
			}
			wantCalls := 4
			if fail > 0 {
				wantCalls++
			}
			if len(model.calls) != wantCalls {
				t.Fatalf("calls=%d want %d", len(model.calls), wantCalls)
			}
			for _, r := range model.calls {
				if r.DurationMs > 45000 || r.Model != used || r.AccountScopeID != "account" || r.FocusNotes != "names" || r.SizeBytes <= 0 {
					t.Fatalf("request %+v", r)
				}
				if _, err := os.Stat(r.PrivatePath); !os.IsNotExist(err) {
					t.Fatal("chunk not cleaned up")
				}
			}
			offsets := []int64{1000, 45000, 89000, 133000}
			if fail == 2 {
				offsets = []int64{1000, 89000, 133000}
			}
			if len(got.Segments) != len(offsets) || len(got.Words) != len(offsets) {
				t.Fatalf("result %+v", got)
			}
			for i, want := range offsets {
				if got.Segments[i].StartMs != want || got.Words[i].StartMs != want {
					t.Fatalf("offset %+v", got)
				}
			}
			if got.Partial != (fail == 2) {
				t.Fatalf("partial %+v", got)
			}
			if fail == 2 && (len(got.ChunkFailures) != 1 || !strings.Contains(got.Text, "Chunk 2 [44000-89000 ms] failed")) {
				t.Fatalf("missing failure note %+v", got)
			}
		})
	}
}

// Purpose: mergeAudioChunk must remove repeated overlap speech while keeping new
// text and absolute word timings. This unit boundary isolates stitching from IO.
func TestAudioChunkOverlap(t *testing.T) {
	dst := GeneratedTranscript{Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 43000, EndMs: 45000, Speech: "hello world"}}, Words: []pebblestore.NormalizedTranscriptWord{{StartMs: 44000, EndMs: 45000, Text: "world"}}}
	src := GeneratedTranscript{Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 0, EndMs: 2000, Speech: "world again"}}, Words: []pebblestore.NormalizedTranscriptWord{{StartMs: 0, EndMs: 1000, Text: "world"}, {StartMs: 1000, EndMs: 2000, Text: "again"}}}
	mergeAudioChunk(&dst, src, 44000)
	if len(dst.Segments) != 2 || dst.Segments[1].Speech != "again" || dst.Segments[1].StartMs != 45000 || len(dst.Words) != 2 || dst.Words[1].Text != "again" || dst.Words[1].StartMs != 45000 {
		t.Fatalf("overlap %+v", dst)
	}
	windows := audioWindows(155000, []int64{40000, 80000, 120000})
	if len(windows) != 4 || windows[0].end != 40000 || windows[1].start != 40000 {
		t.Fatalf("silence cuts %+v", windows)
	}
}

// Purpose: analyzeAudioChunks retries invalid normalized model output exactly
// once, refuses exhausted output, and respects cancellation before model calls.
// A short prepared-track stub isolates retry policy without ffmpeg or network.
func TestAudioChunkInvalidRetry(t *testing.T) {
	model := &chunkModel{fail: 2, attempts: map[string]int{}}
	_, err := analyzeAudioChunks(context.Background(), model, AudioAnalysisRequest{PrivatePath: "chunk-1.flac", DurationMs: 1000})
	if err == nil || len(model.calls) != 2 {
		t.Fatalf("calls=%d error=%v", len(model.calls), err)
	}
	invalid := &audioModel{answer: GeneratedTranscript{Partial: true}}
	if _, err := analyzeAudioChunks(context.Background(), invalid, AudioAnalysisRequest{DurationMs: 1000}); err == nil {
		t.Fatal("invalid transcript accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(model.calls)
	if _, err := analyzeAudioChunks(ctx, model, AudioAnalysisRequest{DurationMs: 1000}); !errors.Is(err, context.Canceled) || len(model.calls) != before {
		t.Fatalf("cancellation: %v", err)
	}
}
