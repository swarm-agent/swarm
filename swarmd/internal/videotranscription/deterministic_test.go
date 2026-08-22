package videotranscription

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type selectiveRetryAdapter struct {
	mu      sync.Mutex
	calls   [][]string
	omitted string
}

func (a *selectiveRetryAdapter) AnalyzeFrameBatch(_ context.Context, request FrameBatchRequest) ([]FrameObservation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, len(request.Frames))
	observations := make([]FrameObservation, 0, len(request.Frames))
	for index, frame := range request.Frames {
		ids[index] = frame.ID
		if frame.ID == a.omitted {
			a.omitted = ""
			continue
		}
		observations = append(observations, FrameObservation{FrameID: frame.ID, Visual: frame.ID})
	}
	a.calls = append(a.calls, ids)
	return observations, nil
}

func (*selectiveRetryAdapter) AnalyzeAudio(context.Context, AudioAnalysisRequest) (GeneratedTranscript, error) {
	return GeneratedTranscript{}, nil
}

func TestAnalyzeDeterministicFramesBatchesConsecutivelyAndRetriesOnlyOmission(t *testing.T) {
	frames := make([]PreparedFrame, 45)
	for index := range frames {
		frames[index] = PreparedFrame{ID: frameIDForTest(index), TimestampMs: int64(index) * 1000, EndMs: int64(index+1) * 1000}
	}
	adapter := &selectiveRetryAdapter{omitted: frames[22].ID}
	observations, err := AnalyzeDeterministicFrames(context.Background(), adapter, FrameBatchRequest{Frames: frames})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != len(frames) {
		t.Fatalf("observations = %d", len(observations))
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.calls) != 4 {
		t.Fatalf("calls = %#v", adapter.calls)
	}
	batchSizes := map[int]int{}
	var retry []string
	for _, call := range adapter.calls {
		batchSizes[len(call)]++
		if len(call) == 1 {
			retry = call
		}
	}
	if batchSizes[20] != 2 || batchSizes[5] != 1 || len(retry) != 1 || retry[0] != frames[22].ID {
		t.Fatalf("calls = %#v", adapter.calls)
	}
}

func TestPrepareDeterministicMediaExtractsStableSecondFramesAndAudio(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local ffmpeg runtime")
	}
	dir := t.TempDir()
	sourcePath := dir + "/source.mp4"
	command := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x240:r=10:d=2.2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000:duration=2.2", "-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", sourcePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v: %s", err, output)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	media, err := PrepareDeterministicMedia(context.Background(), file, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	if len(media.Frames) != 2 || media.Frames[0].ID != frameIDForTest(0) || media.Frames[1].EndMs != media.DurationMs || !media.HasAudio || media.AudioSizeBytes <= 0 {
		t.Fatalf("media = %#v", media)
	}
}

func TestMergeDeterministicTracksPreservesCoverageAndOverlaysAudio(t *testing.T) {
	media := &PreparedMedia{DurationMs: 2500, Frames: []PreparedFrame{
		{ID: frameIDForTest(0), TimestampMs: 0, EndMs: 1000},
		{ID: frameIDForTest(1), TimestampMs: 1000, EndMs: 2000},
		{ID: frameIDForTest(2), TimestampMs: 2000, EndMs: 2500},
	}}
	visual := []FrameObservation{
		{FrameID: frameIDForTest(0), Visual: "first"},
		{FrameID: frameIDForTest(1), Visual: "second", OnScreenText: "Settings"},
		{FrameID: frameIDForTest(2), Visual: "third"},
	}
	audio := GeneratedTranscript{Language: "en", Summary: "audio", Segments: []pebblestore.NormalizedTranscriptSegment{
		{StartMs: 500, EndMs: 2200, Speech: "hello", Audio: "music"},
	}}
	got, err := MergeDeterministicTracks(media, visual, audio)
	if err != nil {
		t.Fatal(err)
	}
	if got.Partial || got.DurationMs != 2500 || len(got.Segments) != 3 {
		t.Fatalf("transcript = %#v", got)
	}
	for index, segment := range got.Segments {
		if segment.Speech != "hello" || segment.Audio != "music" {
			t.Fatalf("segment %d = %#v", index, segment)
		}
	}
}

func frameIDForTest(index int) string {
	return fmt.Sprintf("frame_%012d", index*1000)
}
