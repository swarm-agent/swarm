package audiogen

import (
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videotranscription"
	"testing"
)

// Purpose: TimingsFromTranscript must retain valid partial chunk timing and its
// gap notes, but still reject malformed intervals. This conversion unit is the
// narrowest boundary owning alignment output; no provider calls are needed.
func TestTimingsFromPartialChunks(t *testing.T) {
	transcript := videotranscription.GeneratedTranscript{DurationMs: 155000, Partial: true, ChunkFailures: []string{"Chunk 2 failed"}, Segments: []pebblestore.NormalizedTranscriptSegment{{StartMs: 89000, EndMs: 90000, Speech: "hello"}}}
	got, err := TimingsFromTranscript(transcript)
	if err != nil || !got.Partial || len(got.ChunkFailures) != 1 || len(got.Segments) != 1 || got.Segments[0].Start != 89 {
		t.Fatalf("%+v %v", got, err)
	}
	transcript.Segments[0].EndMs = 88000
	if _, err := TimingsFromTranscript(transcript); err == nil {
		t.Fatal("invalid interval accepted with failure note")
	}
}
