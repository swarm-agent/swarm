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

// CompositionPlacement is immutable, pixel-resolved spatial information for one
// composition source. It is produced from the revision-owned composition catalog
// during materialization and consumed by both frame inspection and final render.
type CompositionPlacement struct {
	SlotID        string
	X             int
	Y             int
	Width         int
	Height        int
	Fit           string
	AlignmentX    float64
	AlignmentY    float64
	CropTop       float64
	CropRight     float64
	CropBottom    float64
	CropLeft      float64
	MaskKind       string
	MaskRadius     float64
	ZIndex         int
	SourceSpanMs   int64
	TimelineSpanMs int64
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
	Composition     *CompositionPlacement
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
	var tailPadMs int64
	layeredTimeline := false
	for i, in := range inputs {
		dur := in.EndMs - in.StartMs
		if in.Composition != nil && in.Composition.TimelineSpanMs > 0 {
			dur = in.Composition.TimelineSpanMs
		} else if dur <= 0 {
			dur = in.DurationMs
		}
		if dur <= 0 {
			dur = 1000
		}
		clipDurations[i] = dur
		if !in.IsAudio && in.Composition == nil && in.Track == 0 {
			totalDurationMs += dur
		}
		if in.IsAudio || in.Track != 0 || in.Layer != 0 || in.Composition != nil {
			layeredTimeline = true
		}
	}
	primaryInputIndexes := make([]int, 0, len(inputs))
	for index, input := range inputs {
		if input.IsAudio || input.Composition != nil {
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
		if timeline.TotalDurationMs > 0 {
			if timeline.TotalDurationMs > totalDurationMs {
				tailPadMs = timeline.TotalDurationMs - totalDurationMs
			}
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
		if input.Composition != nil && input.Composition.TimelineSpanMs > 0 {
			durSec = float64(input.Composition.TimelineSpanMs) / 1000.0
		} else if durSec <= 0 {
			durSec = float64(input.DurationMs) / 1000.0
		}
		if durSec <= 0 {
			durSec = 1.0
		}
		if input.Composition != nil && (input.Composition.SourceSpanMs <= 0 || input.Composition.TimelineSpanMs <= 0) {
			return nil, fmt.Errorf("composition slot %q requires positive source and timeline spans", input.Composition.SlotID)
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
				sourceDurationSec := durSec
				if input.Composition != nil && input.EndMs > input.StartMs {
					sourceDurationSec = float64(input.EndMs-input.StartMs) / 1000.0
				}
				vFilters = append(vFilters, fmt.Sprintf("trim=start=%.3f:duration=%.3f", startSec, sourceDurationSec))
			}
			if input.Composition != nil {
				placementFilters, err := compositionVideoFilters(*input.Composition)
				if err != nil {
					return nil, fmt.Errorf("composition slot %q: %w", input.Composition.SlotID, err)
				}
				vFilters = append(vFilters, placementFilters...)
				vFilters = append(vFilters,
					"setsar=1",
					fmt.Sprintf("fps=%.2f", fps),
					"settb=AVTB",
				)
				if input.Composition.SourceSpanMs > 0 && input.Composition.TimelineSpanMs > 0 && input.Composition.SourceSpanMs != input.Composition.TimelineSpanMs {
					vFilters = append(vFilters, fmt.Sprintf("setpts=(PTS-STARTPTS)*%.9f", float64(input.Composition.TimelineSpanMs)/float64(input.Composition.SourceSpanMs)))
				} else {
					vFilters = append(vFilters, "setpts=PTS-STARTPTS")
				}
			} else {
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
			}

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
			if vol <= 0 && input.Composition == nil {
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
		} else if input.IsAudio || input.Track == 0 {
			// Primary-track visuals need deterministic silence so joins always
			// receive matched audio streams. Muted/non-audio overlay inputs are
			// never mixed and must not leave an unconnected filter output.
			silenceOut := fmt.Sprintf("[asilence%d]", i)
			filterParts = append(filterParts, fmt.Sprintf("aevalsrc=0:d=%.3f:s=48000:c=stereo%s", durSec, silenceOut))
			audioStreams = append(audioStreams, silenceOut)
		} else {
			audioStreams = append(audioStreams, "")
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

	if !layeredTimeline && timeline.TotalDurationMs > 0 {
		videoDurationOut := "[v_timeline_duration]"
		videoDurationFilters := make([]string, 0, 3)
		if tailPadMs > 0 {
			videoDurationFilters = append(videoDurationFilters, fmt.Sprintf("tpad=stop_mode=clone:stop_duration=%.3f", float64(tailPadMs)/1000))
		}
		videoDurationFilters = append(videoDurationFilters,
			fmt.Sprintf("trim=duration=%.3f", float64(totalDurationMs)/1000),
			"setpts=PTS-STARTPTS",
		)
		filterParts = append(filterParts, fmt.Sprintf("%s%s%s", plan.VideoMap, strings.Join(videoDurationFilters, ","), videoDurationOut))
		plan.VideoMap = videoDurationOut
	}

	if layeredTimeline {
		overlayIndexes := make([]int, 0, len(inputs))
		for inputIndex, input := range inputs {
			if input.Track != 0 || input.IsAudio || input.Composition != nil {
					overlayIndexes = append(overlayIndexes, inputIndex)
			}
		}
		sort.SliceStable(overlayIndexes, func(i, j int) bool {
			left, right := inputs[overlayIndexes[i]], inputs[overlayIndexes[j]]
			leftLayer, rightLayer := left.Layer, right.Layer
			if left.Composition != nil {
				leftLayer = left.Composition.ZIndex
			}
			if right.Composition != nil {
				rightLayer = right.Composition.ZIndex
			}
			if leftLayer != rightLayer {
				return leftLayer < rightLayer
			}
			if left.TimelineStartMs != right.TimelineStartMs {
				return left.TimelineStartMs < right.TimelineStartMs
			}
			return left.ClipID < right.ClipID
		})
		for _, inputIndex := range overlayIndexes {
			input := inputs[inputIndex]
			if input.TimelineStartMs < 0 {
				if input.Composition != nil {
					return nil, fmt.Errorf("composition input %q has negative timeline start", input.ClipID)
				}
				input.TimelineStartMs = 0
			}
			startSec := float64(input.TimelineStartMs) / 1000
			endMs := input.TimelineEndMs
			if endMs <= input.TimelineStartMs {
				endMs = input.TimelineStartMs + clipDurations[inputIndex]
			}
			if endMs > totalDurationMs {
				if input.Composition != nil {
					return nil, fmt.Errorf("composition input %q exceeds total timeline duration", input.ClipID)
				}
				endMs = totalDurationMs
			}
			if !input.IsAudio {
				endSec := float64(endMs) / 1000
				shiftedVideo := fmt.Sprintf("[v_layer_shift_%d]", inputIndex)
				videoOut := fmt.Sprintf("[v_layer_%d]", inputIndex)
				overlay := "overlay=eof_action=pass"
				if input.Composition != nil {
					overlay = fmt.Sprintf("overlay=x=%d:y=%d:eof_action=pass", input.Composition.X, input.Composition.Y)
				}
				filterParts = append(filterParts,
					fmt.Sprintf("%ssetpts=PTS+%.3f/TB%s", videoStreams[inputIndex], startSec, shiftedVideo),
					fmt.Sprintf("%s%s%s:enable='between(t,%.3f,%.3f)'%s", plan.VideoMap, shiftedVideo, overlay, startSec, endSec, videoOut),
				)
				plan.VideoMap = videoOut
			}
			if input.HasAudio && !input.Muted {
				if plan.AudioMap == "" {
					return nil, errors.New("included overlay audio requires a primary timeline audio authority")
				}
				shiftedAudio := fmt.Sprintf("[a_layer_shift_%d]", inputIndex)
				audioOut := fmt.Sprintf("[a_layer_%d]", inputIndex)
				delayMs := max(input.TimelineStartMs, 0)
				audioShift := fmt.Sprintf("adelay=%d|%d", delayMs, delayMs)
				if input.Composition != nil {
					audioShift += fmt.Sprintf(",apad,atrim=duration=%.3f", float64(totalDurationMs)/1000)
				}
				filterParts = append(filterParts,
					fmt.Sprintf("%s%s%s", audioStreams[inputIndex], audioShift, shiftedAudio),
					fmt.Sprintf("%s%samix=inputs=2:duration=first:dropout_transition=0:normalize=0%s", plan.AudioMap, shiftedAudio, audioOut),
				)
				plan.AudioMap = audioOut
			}
		}
	}

	if plan.AudioMap == "" {
		return nil, errors.New("video render requires a primary timeline audio authority")
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
	masterAudioFilters := []string{fmt.Sprintf("volume=%.2f", masterVolume)}
	if tailPadMs > 0 {
		masterAudioFilters = append(masterAudioFilters, fmt.Sprintf("apad=pad_dur=%.3f", float64(tailPadMs)/1000))
	}
	masterAudioFilters = append(masterAudioFilters, fmt.Sprintf("atrim=duration=%.3f", float64(totalDurationMs)/1000))
	filterParts = append(filterParts, fmt.Sprintf("%s%s%s", plan.AudioMap, strings.Join(masterAudioFilters, ","), masterAudioOut))
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

func compositionVideoFilters(placement CompositionPlacement) ([]string, error) {
	if placement.Width < 2 || placement.Height < 2 || placement.Width%2 != 0 || placement.Height%2 != 0 || placement.X < 0 || placement.Y < 0 || placement.X%2 != 0 || placement.Y%2 != 0 {
		return nil, errors.New("placement must use non-negative even coordinates and positive even dimensions")
	}
	if math.IsNaN(placement.AlignmentX) || math.IsNaN(placement.AlignmentY) || math.IsInf(placement.AlignmentX, 0) || math.IsInf(placement.AlignmentY, 0) ||
		placement.AlignmentX < 0 || placement.AlignmentX > 1 || placement.AlignmentY < 0 || placement.AlignmentY > 1 {
		return nil, errors.New("alignment must be finite and within 0..1")
	}
	crops := []float64{placement.CropTop, placement.CropRight, placement.CropBottom, placement.CropLeft}
	for _, crop := range crops {
		if math.IsNaN(crop) || math.IsInf(crop, 0) || crop < 0 {
			return nil, errors.New("crop must be finite and non-negative")
		}
	}
	if placement.CropLeft+placement.CropRight >= 1 || placement.CropTop+placement.CropBottom >= 1 {
		return nil, errors.New("crop must leave a positive source area")
	}
	filters := make([]string, 0, 8)
	if placement.CropTop != 0 || placement.CropRight != 0 || placement.CropBottom != 0 || placement.CropLeft != 0 {
		cropWidth := 1 - placement.CropLeft - placement.CropRight
		cropHeight := 1 - placement.CropTop - placement.CropBottom
		filters = append(filters, fmt.Sprintf("crop=w=trunc(iw*%.6f/2)*2:h=trunc(ih*%.6f/2)*2:x=trunc(iw*%.6f/2)*2:y=trunc(ih*%.6f/2)*2", cropWidth, cropHeight, placement.CropLeft, placement.CropTop))
	}
	switch placement.Fit {
	case "contain":
		padX := fmt.Sprintf("trunc((ow-iw)*%.6f/2)*2", placement.AlignmentX)
		padY := fmt.Sprintf("trunc((oh-ih)*%.6f/2)*2", placement.AlignmentY)
		filters = append(filters,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", placement.Width, placement.Height),
			fmt.Sprintf("pad=%d:%d:%s:%s:color=black@0", placement.Width, placement.Height, padX, padY),
		)
	case "cover":
		cropX := fmt.Sprintf("trunc((iw-ow)*%.6f/2)*2", placement.AlignmentX)
		cropY := fmt.Sprintf("trunc((ih-oh)*%.6f/2)*2", placement.AlignmentY)
		filters = append(filters,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", placement.Width, placement.Height),
			fmt.Sprintf("crop=%d:%d:%s:%s", placement.Width, placement.Height, cropX, cropY),
		)
	default:
		return nil, fmt.Errorf("unsupported fit %q", placement.Fit)
	}

	switch placement.MaskKind {
	case "", "none":
		filters = append(filters, "format=pix_fmts=yuva420p")
	case "ellipse":
		filters = append(filters,
			"format=pix_fmts=yuva420p",
			"geq=r='r(X,Y)':g='g(X,Y)':b='b(X,Y)':a='if(lte(pow((X-W/2)/(W/2),2)+pow((Y-H/2)/(H/2),2),1),255,0)'",
		)
	case "rounded_rect":
		if math.IsNaN(placement.MaskRadius) || math.IsInf(placement.MaskRadius, 0) || placement.MaskRadius < 0 || placement.MaskRadius > .5 {
			return nil, errors.New("rounded rectangle radius must be within 0..0.5")
		}
		radiusPixels := int(math.Round(placement.MaskRadius * float64(min(placement.Width, placement.Height))))
		if radiusPixels < 1 {
			filters = append(filters, "format=pix_fmts=yuva420p")
			break
		}
		alpha := fmt.Sprintf("if(gte(min(min(X,W-1-X),min(Y,H-1-Y)),%d),255,if(lte(pow(max(%d-min(X,W-1-X),0),2)+pow(max(%d-min(Y,H-1-Y),0),2),pow(%d,2)),255,0))", radiusPixels, radiusPixels, radiusPixels, radiusPixels)
		filters = append(filters, "format=pix_fmts=yuva420p", fmt.Sprintf("geq=r='r(X,Y)':g='g(X,Y)':b='b(X,Y)':a='%s'", alpha))
	default:
		return nil, fmt.Errorf("unsupported mask %q", placement.MaskKind)
	}
	return filters, nil
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
