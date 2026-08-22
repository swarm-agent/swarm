package videotranscription

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestAnalyzePreparedAudioFindsSyntheticPulseTempo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.s16le")
	const durationSeconds = 8
	samples := make([]int16, audioSampleRate*durationSeconds)
	for beat := 0; beat < durationSeconds*2; beat++ {
		start := beat * audioSampleRate / 2
		for i := 0; i < 500 && start+i < len(samples); i++ {
			envelope := 1 - float64(i)/500
			samples[start+i] = int16(math.Sin(2*math.Pi*120*float64(i)/audioSampleRate) * envelope * 28000)
		}
	}
	payload := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(payload[i*2:], uint16(sample))
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedAudio{DurationMs: durationSeconds * 1000, PCMPath: path}
	source := pebblestore.AudioSourceReference{Ref: "audiosrc_" + string(make([]byte, 64)), SourceFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	snapshot, err := AnalyzePreparedAudio(prepared, "account", "workspace", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Levels) == 0 || len(snapshot.Onsets) < 10 {
		t.Fatalf("analysis lacks pulse detail: %#v", snapshot)
	}
	if snapshot.Tempo == nil || math.Abs(snapshot.Tempo.BPM-120) > 3 || snapshot.Tempo.Confidence < .25 {
		t.Fatalf("tempo = %#v", snapshot.Tempo)
	}
	if len(snapshot.Beats) < 10 {
		t.Fatalf("beats = %d", len(snapshot.Beats))
	}
	for _, onset := range snapshot.Onsets {
		if onset.TimeMs < 0 || onset.TimeMs > prepared.DurationMs {
			t.Fatalf("onset outside PCM timeline: %#v", onset)
		}
	}
}

func TestAnalyzePreparedAudioOmitsTempoForSpeechLikeSparseSignal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.s16le")
	samples := make([]int16, audioSampleRate*4)
	for i := audioSampleRate; i < audioSampleRate+1200; i++ {
		samples[i] = int16(8000 * math.Sin(float64(i)))
	}
	payload := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(payload[i*2:], uint16(sample))
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := AnalyzePreparedAudio(&PreparedAudio{DurationMs: 4000, PCMPath: path}, "account", "workspace", pebblestore.AudioSourceReference{Ref: "audiosrc_test", SourceFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Tempo != nil || len(snapshot.Beats) != 0 {
		t.Fatalf("sparse signal must not claim beat authority: tempo=%#v beats=%d", snapshot.Tempo, len(snapshot.Beats))
	}
}
