package videotranscription

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const audioChunkMs int64 = 45000
const audioOverlapMs int64 = 1000

type audioWindow struct{ start, end int64 }

func audioWindows(duration int64, silences []int64) []audioWindow {
	var windows []audioWindow
	for start := int64(0); start < duration; {
		end := min(start+audioChunkMs, duration)
		next := end - audioOverlapMs
		if end < duration {
			for _, silence := range silences {
				if silence >= start+30000 && silence <= end {
					end, next = silence, silence
				}
			}
		}
		windows = append(windows, audioWindow{start, end})
		if end == duration {
			break
		}
		start = next
	}
	return windows
}

// Silence detection is optional: fixed overlapping windows remain bounded when
// detection fails or its bounded diagnostics contain no suitable cut.
func audioSilences(ctx context.Context, path string) []int64 {
	output := &boundedBuffer{remaining: 1 << 20}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-i", path, "-af", "silencedetect=noise=-35dB:d=0.3", "-f", "null", "-")
	cmd.Stderr = output
	if cmd.Run() != nil {
		return nil
	}
	var cuts []int64
	for _, match := range regexp.MustCompile(`silence_end: ([0-9.]+) \| silence_duration: ([0-9.]+)`).FindAllStringSubmatch(output.String(), -1) {
		end, e1 := strconv.ParseFloat(match[1], 64)
		duration, e2 := strconv.ParseFloat(match[2], 64)
		if e1 == nil && e2 == nil {
			cuts = append(cuts, int64((end-duration/2)*1000))
		}
	}
	return cuts
}

// analyzeAudioChunks keeps model/schema/account authority on the existing
// adapter. Every attempt uses the same context, including its usage collector.
func analyzeAudioChunks(ctx context.Context, adapter DeterministicAdapter, request AudioAnalysisRequest) (GeneratedTranscript, error) {
	return AnalyzeAudioChunks(ctx, request, adapter.AnalyzeAudio)
}

// ErrInvalidTranscript marks an invalid structured model answer, not a transport
// or configuration failure. Only this error and normalization failures retry.
var ErrInvalidTranscript = errors.New("invalid structured transcript")

// AnalyzeAudioChunks shares extraction, retry and stitching with speech alignment.
// analyze must account for usage on every attempt, including invalid answers.
func AnalyzeAudioChunks(ctx context.Context, request AudioAnalysisRequest, analyze func(context.Context, AudioAnalysisRequest) (GeneratedTranscript, error)) (GeneratedTranscript, error) {
	windows := []audioWindow{{0, request.DurationMs}}
	if request.DurationMs > audioChunkMs {
		windows = audioWindows(request.DurationMs, audioSilences(ctx, request.PrivatePath))
	}
	if err := ctx.Err(); err != nil {
		return GeneratedTranscript{}, err
	}
	dir, err := os.MkdirTemp("", "swarm-audio-chunks-")
	if err != nil {
		return GeneratedTranscript{}, err
	}
	defer os.RemoveAll(dir)
	merged := GeneratedTranscript{DurationMs: request.DurationMs}
	successes := 0
	for i, window := range windows {
		if err := ctx.Err(); err != nil {
			return GeneratedTranscript{}, err
		}
		chunk := request
		chunk.DurationMs = window.end - window.start
		if len(windows) > 1 {
			chunk.PrivatePath = filepath.Join(dir, fmt.Sprintf("chunk-%d.flac", i))
			_, err = runBoundedCommand(ctx, "ffmpeg", "-v", "error", "-nostdin", "-ss", fmt.Sprintf("%.3f", float64(window.start)/1000), "-i", request.PrivatePath, "-t", fmt.Sprintf("%.3f", float64(chunk.DurationMs)/1000), "-c:a", "flac", chunk.PrivatePath)
			if err != nil {
				return GeneratedTranscript{}, err
			}
			info, statErr := os.Stat(chunk.PrivatePath)
			if statErr != nil {
				return GeneratedTranscript{}, statErr
			}
			chunk.SizeBytes = info.Size()
		}
		var generated GeneratedTranscript
		for attempt := 0; attempt < 2; attempt++ {
			generated, err = analyze(ctx, chunk)
			if ctx.Err() != nil {
				return GeneratedTranscript{}, ctx.Err()
			}
			if err != nil && !invalidTranscriptError(err) {
				return GeneratedTranscript{}, err
			}
			if err == nil {
				if chunk.DurationMs > 0 {
					generated.DurationMs = chunk.DurationMs
				}
				generated, err = NormalizeGeneratedTranscript(generated)
				if err == nil && !generated.Partial {
					break
				}
			}
		}
		if err != nil || generated.Partial {
			merged.ChunkFailures = append(merged.ChunkFailures, fmt.Sprintf("Chunk %d [%d-%d ms] failed after 2 attempts; transcript unavailable for this interval.", i+1, window.start, window.end))
			continue
		}
		if len(windows) == 1 {
			return generated, nil
		}
		successes++
		if merged.Language == "" {
			merged.Language = generated.Language
		}
		mergeAudioChunk(&merged, generated, window.start)
	}
	if successes == 0 {
		return GeneratedTranscript{}, errors.New("the transcription model's answer did not form a timed transcript in any chunk")
	}
	merged.Summary = strings.Join(merged.ChunkFailures, "\n")
	merged, err = NormalizeGeneratedTranscript(merged)
	if err != nil || merged.Partial {
		return GeneratedTranscript{}, errors.New("merged audio did not form a timed transcript")
	}
	merged.Partial = len(merged.ChunkFailures) > 0
	return merged, nil
}

func mergeAudioChunk(dst *GeneratedTranscript, src GeneratedTranscript, offset int64) {
	for _, segment := range src.Segments {
		segment.StartMs += offset
		segment.EndMs += offset
		if n := len(dst.Segments); n > 0 {
			previous := dst.Segments[n-1]
			if segment.StartMs < previous.EndMs {
				segment.Speech = trimOverlap(previous.Speech, segment.Speech)
				if segment.Speech == "" && segment.Audio == previous.Audio && segment.Visual == previous.Visual && segment.OnScreenText == previous.OnScreenText {
					continue
				}
				segment.StartMs = previous.EndMs
			}
		}
		if strings.TrimSpace(segment.Speech) == "" && strings.TrimSpace(segment.Audio) == "" && strings.TrimSpace(segment.Visual) == "" && strings.TrimSpace(segment.OnScreenText) == "" {
			continue
		}
		if segment.EndMs > segment.StartMs {
			dst.Segments = append(dst.Segments, segment)
		}
	}
	for _, word := range src.Words {
		word.StartMs += offset
		word.EndMs += offset
		if n := len(dst.Words); n > 0 && word.StartMs < dst.Words[n-1].EndMs {
			if strings.EqualFold(word.Text, dst.Words[n-1].Text) {
				continue
			}
			word.StartMs = dst.Words[n-1].EndMs
		}
		if word.EndMs > word.StartMs {
			dst.Words = append(dst.Words, word)
		}
	}
}

func trimOverlap(previous, current string) string {
	a, b := strings.Fields(previous), strings.Fields(current)
	for n := min(len(a), len(b)); n > 0; n-- {
		if strings.EqualFold(strings.Join(a[len(a)-n:], " "), strings.Join(b[:n], " ")) {
			return strings.Join(b[n:], " ")
		}
	}
	return current
}

// The existing Google adapter predates typed parse errors. Keep this exact,
// narrow compatibility list until that adapter adopts ErrInvalidTranscript;
// never classify credentials, policy, upload or HTTP failures by substring.
func invalidTranscriptError(err error) bool {
	if errors.Is(err, ErrInvalidTranscript) {
		return true
	}
	switch err.Error() {
	case "google video transcription output did not match the structured schema", "google video transcription output contained trailing structured data":
		return true
	}
	return false
}
