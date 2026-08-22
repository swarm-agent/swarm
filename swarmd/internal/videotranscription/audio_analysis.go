package videotranscription

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	AudioAnalyzerVersion = "swarm-dsp.v1"
	audioSampleRate      = 16000
	audioMaxPCMBytes     = 128 << 20
)

// PreparedAudio owns one normalized 16 kHz mono timeline. FLAC is used for
// semantic provider analysis while PCM is the sole timing authority for DSP.
type PreparedAudio struct {
	DurationMs int64
	FLACPath   string
	FLACBytes  int64
	PCMPath    string
	privateDir string
}

func (a *PreparedAudio) Close() error {
	if a == nil || a.privateDir == "" {
		return nil
	}
	err := os.RemoveAll(a.privateDir)
	a.privateDir = ""
	return err
}

func PrepareRegisteredAudio(ctx context.Context, source io.ReadSeeker, sizeBytes int64) (prepared *PreparedAudio, err error) {
	if source == nil || sizeBytes <= 0 || sizeBytes > pebblestore.AudioSourceMaxBytes {
		return nil, errors.New("deterministic audio preparation requires a bounded trusted source")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("deterministic audio analysis requires ffmpeg in PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, errors.New("deterministic audio analysis requires ffprobe in PATH")
	}
	dir, err := os.MkdirTemp("", "swarm-audio-analysis-")
	if err != nil {
		return nil, errors.New("private audio analysis storage could not be created")
	}
	prepared = &PreparedAudio{privateDir: dir, FLACPath: filepath.Join(dir, "audio.flac"), PCMPath: filepath.Join(dir, "audio.s16le")}
	defer func() {
		if err != nil {
			_ = prepared.Close()
		}
	}()
	inputPath := filepath.Join(dir, "source.audio")
	input, err := os.OpenFile(inputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.New("private audio analysis source could not be created")
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		input.Close()
		return nil, errors.New("audio source could not be rewound for deterministic analysis")
	}
	written, copyErr := io.Copy(input, io.LimitReader(source, sizeBytes+1))
	closeErr := input.Close()
	if copyErr != nil || closeErr != nil || written != sizeBytes {
		return nil, errors.New("audio source could not be copied into bounded private analysis storage")
	}
	probePayload, err := runBoundedCommand(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", inputPath)
	if err != nil {
		return nil, fmt.Errorf("deterministic audio probe failed: %w", err)
	}
	var probe mediaProbe
	if jsonErr := json.Unmarshal(probePayload, &probe); jsonErr != nil {
		return nil, errors.New("deterministic audio probe returned malformed metadata")
	}
	durationSeconds, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || durationSeconds <= 0 || math.IsInf(durationSeconds, 0) || math.IsNaN(durationSeconds) {
		return nil, errors.New("deterministic audio probe returned an invalid duration")
	}
	hasAudio := false
	for _, stream := range probe.Streams {
		hasAudio = hasAudio || strings.EqualFold(strings.TrimSpace(stream.CodecType), "audio")
	}
	if !hasAudio {
		return nil, errors.New("registered audio source has no decodable audio stream")
	}
	prepared.DurationMs = int64(math.Ceil(durationSeconds * 1000))
	expectedPCMBytes := int64(math.Ceil(durationSeconds*audioSampleRate)) * 2
	if expectedPCMBytes <= 0 || expectedPCMBytes > audioMaxPCMBytes {
		return nil, errors.New("decoded audio exceeds the bounded deterministic analysis limit")
	}
	if _, err = runBoundedCommand(ctx, "ffmpeg", "-v", "error", "-nostdin", "-i", inputPath, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(audioSampleRate), "-c:a", "flac", prepared.FLACPath); err != nil {
		return nil, fmt.Errorf("deterministic FLAC preparation failed: %w", err)
	}
	if _, err = runBoundedCommand(ctx, "ffmpeg", "-v", "error", "-nostdin", "-i", inputPath, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(audioSampleRate), "-f", "s16le", "-c:a", "pcm_s16le", prepared.PCMPath); err != nil {
		return nil, fmt.Errorf("deterministic PCM preparation failed: %w", err)
	}
	flacInfo, flacErr := os.Stat(prepared.FLACPath)
	pcmInfo, pcmErr := os.Stat(prepared.PCMPath)
	if flacErr != nil || pcmErr != nil || flacInfo.Size() <= 0 || flacInfo.Size() > deterministicMaxAudioBytes || pcmInfo.Size() <= 0 || pcmInfo.Size() > audioMaxPCMBytes || pcmInfo.Size()%2 != 0 {
		return nil, errors.New("prepared audio violates the bounded analysis contract")
	}
	prepared.FLACBytes = flacInfo.Size()
	actualDuration := pcmInfo.Size() * 1000 / (audioSampleRate * 2)
	if actualDuration <= 0 || abs64(actualDuration-prepared.DurationMs) > 1000 {
		return nil, errors.New("prepared audio duration does not match its decoded timeline")
	}
	prepared.DurationMs = actualDuration
	return prepared, nil
}

// AnalyzePreparedAudio deterministically derives waveform, onset, tempo, beat,
// and conservative energy-section data from normalized PCM.
func AnalyzePreparedAudio(prepared *PreparedAudio, accountScopeID, workspaceID string, source pebblestore.AudioSourceReference) (pebblestore.AudioAnalysisSnapshot, error) {
	if prepared == nil || prepared.DurationMs <= 0 || prepared.PCMPath == "" {
		return pebblestore.AudioAnalysisSnapshot{}, errors.New("prepared PCM audio is required")
	}
	payload, err := os.ReadFile(prepared.PCMPath)
	if err != nil || len(payload) == 0 || len(payload)%2 != 0 || len(payload) > audioMaxPCMBytes {
		return pebblestore.AudioAnalysisSnapshot{}, errors.New("prepared PCM audio could not be read within bounds")
	}
	sampleCount := len(payload) / 2
	levelIntervalMs := int64(20)
	if needed := int64(math.Ceil(float64(prepared.DurationMs) / 50000)); needed > levelIntervalMs {
		levelIntervalMs = needed
	}
	levelSamples := max(1, int(levelIntervalMs*audioSampleRate/1000))
	levels := make([]pebblestore.AudioAnalysisLevel, 0, (sampleCount+levelSamples-1)/levelSamples)
	energies := make([]float64, 0, cap(levels))
	for start := 0; start < sampleCount; start += levelSamples {
		end := min(sampleCount, start+levelSamples)
		var sum, peak float64
		for sampleIndex := start; sampleIndex < end; sampleIndex++ {
			sample := float64(int16(binary.LittleEndian.Uint16(payload[sampleIndex*2:]))) / 32768
			value := math.Abs(sample)
			sum += sample * sample
			peak = math.Max(peak, value)
		}
		rms := math.Sqrt(sum / float64(end-start))
		startMs := int64(start) * 1000 / audioSampleRate
		endMs := int64(end) * 1000 / audioSampleRate
		if endMs > prepared.DurationMs {
			endMs = prepared.DurationMs
		}
		if endMs <= startMs {
			endMs = startMs + 1
		}
		levels = append(levels, pebblestore.AudioAnalysisLevel{StartMs: startMs, EndMs: endMs, RMS: clamp01(rms), Peak: clamp01(peak)})
		energies = append(energies, rms)
	}
	onsets := detectOnsets(energies, levelIntervalMs, prepared.DurationMs)
	tempo, beats := estimateTempoAndBeats(onsets, prepared.DurationMs)
	sections := deriveEnergySections(energies, levelIntervalMs, prepared.DurationMs)
	return pebblestore.AudioAnalysisSnapshot{
		SchemaVersion: pebblestore.AudioAnalysisSchemaVersion, AccountScopeID: strings.TrimSpace(accountScopeID), WorkspaceID: strings.TrimSpace(workspaceID),
		SourceRef: source.Ref, SourceFingerprint: source.SourceFingerprint, AnalyzerVersion: AudioAnalyzerVersion,
		DurationMs: prepared.DurationMs, SampleIntervalMs: levelIntervalMs, Levels: levels, Onsets: onsets, Tempo: tempo, Beats: beats, Sections: sections,
	}, nil
}

func detectOnsets(energy []float64, intervalMs, durationMs int64) []pebblestore.AudioAnalysisOnset {
	if len(energy) < 4 {
		return nil
	}
	flux := make([]float64, len(energy))
	for i := 1; i < len(energy); i++ {
		flux[i] = math.Max(0, energy[i]-energy[i-1])
	}
	var out []pebblestore.AudioAnalysisOnset
	last := int64(-200)
	for i := 2; i+2 < len(flux); i++ {
		start := max(0, i-25)
		var mean float64
		for _, value := range flux[start:i] {
			mean += value
		}
		mean /= float64(max(1, i-start))
		threshold := mean*2.5 + 0.003
		if flux[i] < threshold || flux[i] < flux[i-1] || flux[i] < flux[i+1] {
			continue
		}
		timeMs := min64(durationMs, int64(i)*intervalMs)
		if timeMs-last < 100 {
			continue
		}
		strength := clamp01((flux[i] - threshold) / (threshold + 0.02))
		out = append(out, pebblestore.AudioAnalysisOnset{TimeMs: timeMs, Strength: strength})
		last = timeMs
	}
	return out
}

func estimateTempoAndBeats(onsets []pebblestore.AudioAnalysisOnset, durationMs int64) (*pebblestore.AudioAnalysisTempo, []pebblestore.AudioAnalysisBeat) {
	if len(onsets) < 6 || durationMs < 3000 {
		return nil, nil
	}
	type candidate struct {
		interval int64
		score    float64
	}
	best := candidate{}
	for bpm := 60; bpm <= 200; bpm++ {
		interval := int64(math.Round(60000 / float64(bpm)))
		var score float64
		for i := 1; i < len(onsets); i++ {
			delta := onsets[i].TimeMs - onsets[i-1].TimeMs
			nearest := math.Round(float64(delta)/float64(interval)) * float64(interval)
			if nearest <= 0 {
				continue
			}
			errorMs := math.Abs(float64(delta) - nearest)
			score += math.Exp(-errorMs*errorMs/(2*40*40)) * onsets[i].Strength
		}
		if score > best.score {
			best = candidate{interval, score}
		}
	}
	if best.score < 1.5 {
		return nil, nil
	}
	confidence := clamp01(best.score / math.Max(3, float64(len(onsets))*0.7))
	if confidence < .25 {
		return nil, nil
	}
	interval := best.interval
	phase := onsets[0].TimeMs
	var bestScore float64
	for _, onset := range onsets {
		var score float64
		for _, other := range onsets {
			distance := math.Abs(math.Remainder(float64(other.TimeMs-onset.TimeMs), float64(interval)))
			score += math.Exp(-distance*distance/(2*35*35)) * other.Strength
		}
		if score > bestScore {
			bestScore, phase = score, onset.TimeMs
		}
	}
	for phase-interval >= 0 {
		phase -= interval
	}
	beats := make([]pebblestore.AudioAnalysisBeat, 0, int(durationMs/interval)+1)
	for timeMs, index := phase, 0; timeMs <= durationMs && len(beats) < 50000; timeMs, index = timeMs+interval, index+1 {
		beats = append(beats, pebblestore.AudioAnalysisBeat{TimeMs: timeMs, Confidence: confidence, BarBeat: index%4 + 1})
	}
	return &pebblestore.AudioAnalysisTempo{BPM: 60000 / float64(interval), Confidence: confidence}, beats
}

func deriveEnergySections(energy []float64, intervalMs, durationMs int64) []pebblestore.AudioAnalysisSection {
	if len(energy) == 0 {
		return nil
	}
	window := max(1, int(5000/intervalMs))
	var sections []pebblestore.AudioAnalysisSection
	labelFor := func(value float64) string {
		if value < .015 {
			return "quiet"
		}
		if value < .08 {
			return "moderate"
		}
		return "high_energy"
	}
	current, start := "", int64(0)
	for i := 0; i < len(energy); i += window {
		end := min(len(energy), i+window)
		var mean float64
		for _, v := range energy[i:end] {
			mean += v
		}
		mean /= float64(end - i)
		label := labelFor(mean)
		timeMs := int64(i) * intervalMs
		if current == "" {
			current = label
			start = timeMs
			continue
		}
		if label != current && timeMs-start >= 5000 {
			sections = append(sections, pebblestore.AudioAnalysisSection{StartMs: start, EndMs: min64(timeMs, durationMs), Label: current, Confidence: .65})
			current, start = label, timeMs
		}
	}
	if durationMs > start {
		sections = append(sections, pebblestore.AudioAnalysisSection{StartMs: start, EndMs: durationMs, Label: current, Confidence: .65})
	}
	return sections
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
