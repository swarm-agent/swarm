package videorender

import (
	"fmt"
	"math"
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
	Index        int
	ClipID       string
	FilePath     string
	IsVideo      bool
	IsImage      bool
	IsAudio      bool
	IsSynthetic  bool
	HasAudio     bool
	DurationMs   int64
	Volume       float64
	Muted        bool
	StartMs      int64
	EndMs        int64
	OverlayMode  string
	Captions     []pebblestore.VideoTextOverlay
	DesignInputs []MaterializedInput
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

	var totalDurationMs int64
	for _, in := range inputs {
		dur := in.EndMs - in.StartMs
		if dur <= 0 {
			dur = in.DurationMs
		}
		if dur <= 0 {
			dur = 1000
		}
		totalDurationMs += dur
	}
	if timeline.TotalDurationMs > 0 && timeline.TotalDurationMs < totalDurationMs {
		totalDurationMs = timeline.TotalDurationMs
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

		// Video filter chain for this input
		var vFilters []string
		if startSec > 0 || (input.EndMs > input.StartMs && input.EndMs > 0) {
			vFilters = append(vFilters, fmt.Sprintf("trim=start=%.3f:duration=%.3f,setpts=PTS-STARTPTS", startSec, durSec))
		}
		vFilters = append(vFilters,
			fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", dims.Width, dims.Height),
			fmt.Sprintf("pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black", dims.Width, dims.Height),
			"setsar=1",
			fmt.Sprintf("fps=%.2f", fps),
		)

		for _, caption := range input.Captions {
			drawFilter := formatCaptionFilter(caption, dims)
			if drawFilter != "" {
				vFilters = append(vFilters, drawFilter)
			}
		}

		filterParts = append(filterParts, fmt.Sprintf("%s%s%s", vIn, strings.Join(vFilters, ","), vOut))
		videoStreams = append(videoStreams, vOut)

		// Audio filter chain for this input
		if input.HasAudio && !input.Muted {
			vol := input.Volume
			if vol <= 0 {
				vol = 1.0
			}
			var aFilters []string
			if startSec > 0 || (input.EndMs > input.StartMs && input.EndMs > 0) {
				aFilters = append(aFilters, fmt.Sprintf("atrim=start=%.3f:duration=%.3f,asetpts=PTS-STARTPTS", startSec, durSec))
			}
			aFilters = append(aFilters, fmt.Sprintf("volume=%.2f", vol), "aformat=sample_rates=48000:channel_layouts=stereo")
			filterParts = append(filterParts, fmt.Sprintf("%s%s%s", aIn, strings.Join(aFilters, ","), aOut))
			audioStreams = append(audioStreams, aOut)
		} else {
			// Generate silent audio for consistent stream matching
			silenceOut := fmt.Sprintf("[asilence%d]", i)
			filterParts = append(filterParts, fmt.Sprintf("aevalsrc=0:d=%.3f:s=48000:c=stereo%s", durSec, silenceOut))
			audioStreams = append(audioStreams, silenceOut)
		}
	}

	// Concat video and audio
	if len(videoStreams) == 1 {
		plan.VideoMap = videoStreams[0]
	} else {
		concatVideo := fmt.Sprintf("%sconcat=n=%d:v=1:a=0[v_concat]", strings.Join(videoStreams, ""), len(videoStreams))
		filterParts = append(filterParts, concatVideo)
		plan.VideoMap = "[v_concat]"
	}

	if len(audioStreams) == 1 {
		plan.AudioMap = audioStreams[0]
	} else {
		concatAudio := fmt.Sprintf("%sconcat=n=%d:v=0:a=1[a_concat]", strings.Join(audioStreams, ""), len(audioStreams))
		filterParts = append(filterParts, concatAudio)
		plan.AudioMap = "[a_concat]"
	}

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
