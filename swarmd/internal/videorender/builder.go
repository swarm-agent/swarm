package videorender

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type RenderDimensions struct {
	Width  int
	Height int
}

type TimelinePlan struct {
	Dimensions      RenderDimensions
	FPS             float64
	TotalDurationMs int64
	Inputs          []MaterializedInput
	FilterComplex   string
	VideoMap        string
	AudioMap        string
	FFmpegArgs      []string
}

type MaterializedInput struct {
	Index           int
	ClipID          string
	FilePath        string
	IsVideo         bool
	IsImage         bool
	IsAudio         bool
	IsSynthetic     bool
	HasAudio        bool
	DurationMs      int64
	Volume          float64
	Muted           bool
	StartMs         int64
	EndMs           int64
	Track           int
	Layer           int
	TimelineStartMs int64
	TimelineEndMs   int64
	OverlayMode     string
	Captions        []pebblestore.VideoTextOverlay
	DesignInputs    []MaterializedInput
}

// ResolveDimensions derives target pixel dimensions from timeline settings and preset.
func ResolveDimensions(timeline pebblestore.VideoProjectTimeline) RenderDimensions {
	if timeline.Width > 0 && timeline.Height > 0 {
		w := (timeline.Width / 2) * 2
		h := (timeline.Height / 2) * 2
		if w < 64 {
			w = 64
		} else if w > 3840 {
			w = 3840
		}
		if h < 64 {
			h = 64
		} else if h > 3840 {
			h = 3840
		}
		return RenderDimensions{Width: w, Height: h}
	}

	switch strings.ToLower(strings.TrimSpace(timeline.OutputPreset)) {
	case pebblestore.VideoPresetLandscape1080p, pebblestore.VideoPresetLandscapeVideo:
		return RenderDimensions{Width: 1920, Height: 1080}
	case pebblestore.VideoPresetLandscape720p:
		return RenderDimensions{Width: 1280, Height: 720}
	case pebblestore.VideoPresetPortrait1080p, pebblestore.VideoPresetPortraitVideo:
		return RenderDimensions{Width: 1080, Height: 1920}
	case pebblestore.VideoPresetPortrait720p:
		return RenderDimensions{Width: 720, Height: 1280}
	case pebblestore.VideoPresetSquare1080p:
		return RenderDimensions{Width: 1080, Height: 1080}
	case pebblestore.VideoPresetSquare720p:
		return RenderDimensions{Width: 720, Height: 720}
	case pebblestore.VideoPresetXHeader:
		return RenderDimensions{Width: 1500, Height: 500}
	default:
		return RenderDimensions{Width: 1920, Height: 1080}
	}
}

// ResolveFPS clamps frame rate to the bounded [1.0, 60.0] range.
func ResolveFPS(timeline pebblestore.VideoProjectTimeline) float64 {
	fps := timeline.FPS
	if fps <= 0 || math.IsNaN(fps) || math.IsInf(fps, 0) {
		return 30.0
	}
	if fps < 1.0 {
		return 1.0
	}
	if fps > 60.0 {
		return 60.0
	}
	return math.Round(fps*100) / 100
}

// BuildFFmpegCommandLine constructs deterministic ffmpeg CLI arguments from the materialized inputs and timeline.
func BuildFFmpegCommandLine(timeline pebblestore.VideoProjectTimeline, inputs []MaterializedInput, outputPath string) (*TimelinePlan, error) {
	dims := ResolveDimensions(timeline)
	fps := ResolveFPS(timeline)

	if len(inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided for video render")
	}

	clipDurations := make([]int64, len(inputs))
	var totalDurationMs int64
	layeredTimeline := false
	for i, in := range inputs {
		dur := in.EndMs - in.StartMs
		if dur <= 0 {
			dur = in.DurationMs
		}
		if dur <= 0 {
			dur = 1000
		}
		clipDurations[i] = dur
		if !in.IsAudio {
			totalDurationMs += dur
		}
		if in.IsAudio || in.Track != 0 || in.Layer != 0 {
			layeredTimeline = true
		}
	}
	primaryInputIndexes := make([]int, 0, len(inputs))
	for index, input := range inputs {
		if input.IsAudio {
			continue
		}
		if !layeredTimeline || input.Track == 0 {
			primaryInputIndexes = append(primaryInputIndexes, index)
		}
	}
	if len(primaryInputIndexes) == 0 {
		return nil, errors.New("video render requires at least one visible primary-track clip")
	}
	// Materialization follows the durable clip slice, but edit proposals may append
	// a clip whose timeline position belongs between existing clips. Render and
	// transition adjacency must follow the timeline, not mutation/storage order.
	sort.SliceStable(primaryInputIndexes, func(i, j int) bool {
		return inputs[primaryInputIndexes[i]].TimelineStartMs < inputs[primaryInputIndexes[j]].TimelineStartMs
	})
	primaryInputs := make([]MaterializedInput, 0, len(primaryInputIndexes))
	primaryDurations := make([]int64, 0, len(primaryInputIndexes))
	for _, index := range primaryInputIndexes {
		primaryInputs = append(primaryInputs, inputs[index])
		primaryDurations = append(primaryDurations, clipDurations[index])
	}
	transitions, overlapMs, err := resolveRenderTransitions(timeline, primaryInputs, primaryDurations)
	if err != nil {
		return nil, err
	}
	if layeredTimeline {
		totalDurationMs = timeline.TotalDurationMs
		if totalDurationMs <= 0 {
			for _, input := range inputs {
				endMs := input.TimelineEndMs
				if endMs <= input.TimelineStartMs {
					endMs = input.TimelineStartMs + input.DurationMs
				}
				if endMs > totalDurationMs {
					totalDurationMs = endMs
				}
			}
		}
	} else {
		totalDurationMs -= overlapMs
		if timeline.TotalDurationMs > 0 && timeline.TotalDurationMs < totalDurationMs {
			totalDurationMs = timeline.TotalDurationMs
		}
	}

	plan := &TimelinePlan{
		Dimensions:      dims,
		FPS:             fps,
		TotalDurationMs: totalDurationMs,
		Inputs:          inputs,
	}

	args := []string{
		"-v", "error",
		"-nostdin",
	}

	for _, input := range inputs {
		if input.FilePath != "" {
			if input.IsImage {
				durSec := float64(input.DurationMs) / 1000.0
				if durSec <= 0 {
					durSec = 5.0
				}
				args = append(args, "-loop", "1", "-t", fmt.Sprintf("%.3f", durSec), "-i", input.FilePath)
			} else {
				args = append(args, "-i", input.FilePath)
			}
		}
	}

	var filterParts []string
	var videoStreams []string
	var audioStreams []string

	for i, input := range inputs {
		vIn := fmt.Sprintf("[%d:v]", i)
		vOut := fmt.Sprintf("[v%d]", i)
		aIn := fmt.Sprintf("[%d:a]", i)
		aOut := fmt.Sprintf("[a%d]", i)

		durSec := float64(input.EndMs-input.StartMs) / 1000.0
		if durSec <= 0 {
			durSec = float64(input.DurationMs) / 1000.0
		}
		if durSec <= 0 {
			durSec = 1.0
		}
		startSec := float64(input.StartMs) / 1000.0
		if startSec < 0 {
			startSec = 0
		}

		if input.IsAudio {
			videoStreams = append(videoStreams, "")
		} else {
			// Video filter chain for visual inputs only. Audio-only inputs must
			// never be referenced through an ffmpeg video stream selector.
			var vFilters []string
			if startSec > 0 || (input.EndMs > input.StartMs && input.EndMs > 0) {
				vFilters = append(vFilters, fmt.Sprintf("trim=start=%.3f:duration=%.3f", startSec, durSec))
			}
			vFilters = append(vFilters,
				fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", dims.Width, dims.Height),
				fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black", dims.Width, dims.Height),
				"setsar=1",
				fmt.Sprintf("fps=%.2f", fps),
				"format=pix_fmts=yuv420p",
				// concat emits AVTB (1/1000000) while fps commonly emits 1/FPS.
				// Normalize every source before any join so a cut followed by xfade
				// cannot feed xfade mismatched timebases. Reset timestamps for source
				// files whose first decoded frame does not begin at zero as well.
				"settb=AVTB",
				"setpts=PTS-STARTPTS",
			)

			for _, caption := range input.Captions {
				drawFilter := formatCaptionFilter(caption, dims)
				if drawFilter != "" {
					vFilters = append(vFilters, drawFilter)
				}
			}

			filterParts = append(filterParts, fmt.Sprintf("%s%s%s", vIn, strings.Join(vFilters, ","), vOut))
			videoStreams = append(videoStreams, vOut)
		}

		// Audio filter chain for this input
		if input.HasAudio && !input.Muted {
			vol := input.Volume
			if vol <= 0 {
				vol = 1.0
			}
			var aFilters []string
			if startSec > 0 || (input.EndMs > input.StartMs && input.EndMs > 0) {
				aFilters = append(aFilters, fmt.Sprintf("atrim=start=%.3f:duration=%.3f", startSec, durSec))
			}
			aFilters = append(aFilters,
				"asetpts=PTS-STARTPTS",
				fmt.Sprintf("volume=%.2f", vol),
				"aformat=sample_rates=48000:channel_layouts=stereo",
			)
			filterParts = append(filterParts, fmt.Sprintf("%s%s%s", aIn, strings.Join(aFilters, ","), aOut))
			audioStreams = append(audioStreams, aOut)
		} else {
			// Generate silent audio for consistent stream matching
			silenceOut := fmt.Sprintf("[asilence%d]", i)
			filterParts = append(filterParts, fmt.Sprintf("aevalsrc=0:d=%.3f:s=48000:c=stereo%s", durSec, silenceOut))
			audioStreams = append(audioStreams, silenceOut)
		}
	}

	// Fold primary-track clips in timeline order. Cuts concatenate streams,
	// while launch dissolve modes use server-selected xfade/acrossfade filters.
	firstPrimary := primaryInputIndexes[0]
	plan.VideoMap = videoStreams[firstPrimary]
	plan.AudioMap = audioStreams[firstPrimary]
	accumulatedMs := clipDurations[firstPrimary]
	for orderIndex := 1; orderIndex < len(primaryInputIndexes); orderIndex++ {
		inputIndex := primaryInputIndexes[orderIndex]
		transition := transitions[orderIndex-1]
		videoOut := fmt.Sprintf("[v_join_%d]", inputIndex)
		audioOut := fmt.Sprintf("[a_join_%d]", inputIndex)
		switch transition.Kind {
		case pebblestore.VideoTransitionKindCut:
			filterParts = append(filterParts,
				fmt.Sprintf("%s%sconcat=n=2:v=1:a=0%s", plan.VideoMap, videoStreams[inputIndex], videoOut),
				fmt.Sprintf("%s%sconcat=n=2:v=0:a=1%s", plan.AudioMap, audioStreams[inputIndex], audioOut),
			)
		case pebblestore.VideoTransitionKindCrossfade, pebblestore.VideoTransitionKindFadeThroughBlack,
			pebblestore.VideoTransitionKindFadeToBlack, pebblestore.VideoTransitionKindFadeFromBlack:
			durationSec := float64(transition.DurationMs) / 1000
			offsetSec := float64(accumulatedMs-transition.DurationMs) / 1000
			xfadeKind := "fade"
			if transition.Kind != pebblestore.VideoTransitionKindCrossfade {
				xfadeKind = "fadeblack"
			}
			filterParts = append(filterParts,
				fmt.Sprintf("%s%sxfade=transition=%s:duration=%.3f:offset=%.3f%s", plan.VideoMap, videoStreams[inputIndex], xfadeKind, durationSec, offsetSec, videoOut),
				fmt.Sprintf("%s%sacrossfade=d=%.3f:c1=tri:c2=tri%s", plan.AudioMap, audioStreams[inputIndex], durationSec, audioOut),
			)
			accumulatedMs -= transition.DurationMs
		}
		accumulatedMs += clipDurations[inputIndex]
		plan.VideoMap = videoOut
		plan.AudioMap = audioOut
	}

	if layeredTimeline {
		for inputIndex, input := range inputs {
			if input.Track == 0 && !input.IsAudio {
				continue
			}
			startSec := float64(input.TimelineStartMs) / 1000
			endMs := input.TimelineEndMs
			if endMs <= input.TimelineStartMs {
				endMs = input.TimelineStartMs + clipDurations[inputIndex]
			}
			if !input.IsAudio {
				endSec := float64(endMs) / 1000
				shiftedVideo := fmt.Sprintf("[v_layer_shift_%d]", inputIndex)
				videoOut := fmt.Sprintf("[v_layer_%d]", inputIndex)
				filterParts = append(filterParts,
					fmt.Sprintf("%ssetpts=PTS-STARTPTS+%.3f/TB%s", videoStreams[inputIndex], startSec, shiftedVideo),
					fmt.Sprintf("%s%soverlay=eof_action=pass:enable='between(t,%.3f,%.3f)'%s", plan.VideoMap, shiftedVideo, startSec, endSec, videoOut),
				)
				plan.VideoMap = videoOut
			}
			if input.HasAudio && !input.Muted {
				shiftedAudio := fmt.Sprintf("[a_layer_shift_%d]", inputIndex)
				audioOut := fmt.Sprintf("[a_layer_%d]", inputIndex)
				delayMs := max(input.TimelineStartMs, 0)
				filterParts = append(filterParts,
					fmt.Sprintf("%sadelay=%d|%d%s", audioStreams[inputIndex], delayMs, delayMs, shiftedAudio),
					fmt.Sprintf("%s%samix=inputs=2:duration=first:dropout_transition=0:normalize=0%s", plan.AudioMap, shiftedAudio, audioOut),
				)
				plan.AudioMap = audioOut
			}
		}
	}

	masterVolume := 1.0
	if timeline.AudioPolicy != nil {
		if timeline.AudioPolicy.Muted {
			masterVolume = 0
		} else if timeline.AudioPolicy.MasterVolume > 0 {
			masterVolume = timeline.AudioPolicy.MasterVolume
		}
	}
	masterAudioOut := "[a_master]"
	filterParts = append(filterParts, fmt.Sprintf("%svolume=%.2f,atrim=duration=%.3f%s", plan.AudioMap, masterVolume, float64(totalDurationMs)/1000, masterAudioOut))
	plan.AudioMap = masterAudioOut

	plan.FilterComplex = strings.Join(filterParts, ";")

	args = append(args,
		"-filter_complex", plan.FilterComplex,
		"-map", plan.VideoMap,
		"-map", plan.AudioMap,
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-profile:v", "high",
		"-level", "4.1",
		"-preset", "medium",
		"-crf", "23",
		"-c:a", "aac",
		"-b:a", "192k",
		"-ar", "48000",
		"-movflags", "+faststart",
		"-y",
		outputPath,
	)

	plan.FFmpegArgs = args
	return plan, nil
}

func resolveRenderTransitions(timeline pebblestore.VideoProjectTimeline, inputs []MaterializedInput, durations []int64) ([]pebblestore.VideoTimelineTransition, int64, error) {
	if len(inputs) != len(durations) {
		return nil, 0, errors.New("render transition inputs and durations do not match")
	}
	if len(inputs) < 2 {
		if len(timeline.Transitions) != 0 {
			return nil, 0, errors.New("timeline transitions require at least two visible clips")
		}
		return nil, 0, nil
	}

	boundaries := make(map[string]int, len(inputs)-1)
	for i := 0; i < len(inputs)-1; i++ {
		boundaries[inputs[i].ClipID+"\x00"+inputs[i+1].ClipID] = i
	}
	resolved := make([]pebblestore.VideoTimelineTransition, len(inputs)-1)
	for i := range resolved {
		resolved[i] = pebblestore.VideoTimelineTransition{
			Kind:       pebblestore.VideoTransitionKindCut,
			FromClipID: inputs[i].ClipID,
			ToClipID:   inputs[i+1].ClipID,
		}
	}

	var overlapMs int64
	seen := make(map[int]struct{}, len(timeline.Transitions))
	for _, transition := range timeline.Transitions {
		boundary, ok := boundaries[transition.FromClipID+"\x00"+transition.ToClipID]
		if !ok {
			return nil, 0, fmt.Errorf("transition %q must connect adjacent visible clips in timeline order", transition.ID)
		}
		if _, duplicate := seen[boundary]; duplicate {
			return nil, 0, fmt.Errorf("multiple transitions target clip boundary %q to %q", transition.FromClipID, transition.ToClipID)
		}
		seen[boundary] = struct{}{}
		switch transition.Kind {
		case pebblestore.VideoTransitionKindCut:
			if transition.DurationMs != 0 {
				return nil, 0, fmt.Errorf("cut transition %q must have zero duration", transition.ID)
			}
		case pebblestore.VideoTransitionKindCrossfade, pebblestore.VideoTransitionKindFadeThroughBlack,
			pebblestore.VideoTransitionKindFadeToBlack, pebblestore.VideoTransitionKindFadeFromBlack:
			if transition.DurationMs <= 0 {
				return nil, 0, fmt.Errorf("transition %q duration must be positive", transition.ID)
			}
			if transition.DurationMs >= durations[boundary] || transition.DurationMs >= durations[boundary+1] {
				return nil, 0, fmt.Errorf("transition %q duration %dms must be shorter than both adjacent clips (%dms and %dms)", transition.ID, transition.DurationMs, durations[boundary], durations[boundary+1])
			}
			overlapMs += transition.DurationMs
		default:
			return nil, 0, fmt.Errorf("transition %q has unsupported render kind %q", transition.ID, transition.Kind)
		}
		resolved[boundary] = transition
	}
	return resolved, overlapMs, nil
}

func formatCaptionFilter(caption pebblestore.VideoTextOverlay, dims RenderDimensions) string {
	rawText := strings.TrimSpace(caption.Text)
	if rawText == "" {
		return ""
	}
	if len(rawText) > pebblestore.MaxTextOverlayLength {
		rawText = rawText[:pebblestore.MaxTextOverlayLength]
	}

	escapedText := escapeFFmpegDrawText(rawText)
	fontSize := caption.FontSize
	if fontSize <= 0 {
		fontSize = int(float64(dims.Height) * 0.045)
		if fontSize < 16 {
			fontSize = 16
		} else if fontSize > 96 {
			fontSize = 96
		}
	}

	fontColor := "white"
	if caption.FontColor != "" {
		fontColor = sanitizeFontColor(caption.FontColor)
	}

	var xPos, yPos string
	switch strings.ToLower(strings.TrimSpace(caption.Position)) {
	case "top":
		xPos = "(w-text_w)/2"
		yPos = "40"
	case "center":
		xPos = "(w-text_w)/2"
		yPos = "(h-text_h)/2"
	case "bottom":
		fallthrough
	default:
		xPos = "(w-text_w)/2"
		yPos = fmt.Sprintf("h-text_h-%d", int(float64(dims.Height)*0.08))
	}

	var enableClause string
	if caption.EndMs > caption.StartMs && caption.EndMs > 0 {
		startSec := float64(caption.StartMs) / 1000.0
		endSec := float64(caption.EndMs) / 1000.0
		enableClause = fmt.Sprintf(":enable='between(t,%.3f,%.3f)'", startSec, endSec)
	}

	return fmt.Sprintf("drawtext=text='%s':fontsize=%d:fontcolor=%s:box=1:boxcolor=black@0.5:boxborderw=4:x=%s:y=%s%s",
		escapedText, fontSize, fontColor, xPos, yPos, enableClause)
}

func escapeFFmpegDrawText(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '\\':
			b.WriteString(`\\\\`)
		case '\'':
			b.WriteString(`\\\'`)
		case '%':
			b.WriteString(`\%%`)
		case ':':
			b.WriteString(`\:`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeFontColor(color string) string {
	color = strings.TrimSpace(color)
	if strings.HasPrefix(color, "#") && (len(color) == 7 || len(color) == 4) {
		if _, err := strconv.ParseUint(color[1:], 16, 32); err == nil {
			return color
		}
	}
	switch strings.ToLower(color) {
	case "white", "black", "yellow", "red", "green", "blue", "cyan", "magenta":
		return strings.ToLower(color)
	default:
		return "white"
	}
}
