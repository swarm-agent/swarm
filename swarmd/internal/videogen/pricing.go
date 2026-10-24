package videogen

import (
	"fmt"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// AdmissionDurationSeconds is a budget dimension, never a provider request option.
// Prefer catalog defaults, then the largest supported single-clip duration.
func AdmissionDurationSeconds(model string, requested int, opts *ParsedVideoOptions) int {
	if requested > 0 {
		return requested
	}
	if opts != nil {
		if opts.DefaultDur > 0 {
			return opts.DefaultDur
		}
		duration := 0
		for _, value := range opts.Durations {
			duration = max(duration, value)
		}
		if duration > 0 {
			return duration
		}
	}
	// Omni generates single clips of up to ten seconds; duration is not selectable.
	if IsOmniModel(model) {
		return 10
	}
	return 0
}

const omniTimingToleranceSeconds = 0.050

// AdmissionOperationDurationSeconds reserves only generated extension duration:
// Omni's maximum ten-second delta plus container timing tolerance, or Veo's
// locked eight seconds. Neither includes the source video.
func AdmissionOperationDurationSeconds(model, operation string, requested int, opts *ParsedVideoOptions) float64 {
	if IsVeoModel(model) && operation == pebblestore.VideoOperationExtend {
		return 8
	}
	if IsOmniModel(model) && operation == pebblestore.VideoOperationExtend {
		return 10 + omniTimingToleranceSeconds
	}
	return float64(AdmissionDurationSeconds(model, requested, opts))
}

// BillingOptions carries measured dimensions without rounding up to started seconds.
// Veo retains requested billing seconds; Omni uses measured output (or extension delta).
func (r ManagedVideoResult) BillingOptions() pebblestore.MediaCostEstimateOptions {
	resolution := r.Resolution
	if r.Width > 0 && r.Height > 0 {
		short := min(r.Width, r.Height)
		if short == 2160 {
			resolution = "4k"
		} else {
			resolution = fmt.Sprintf("%dp", short)
		}
	}
	measured := r.MeasuredDurationSeconds
	if measured <= 0 && r.DurationMs > 0 {
		measured = float64(r.DurationMs) / 1000
	}
	requested := r.DurationSeconds
	if IsVeoModel(r.Model) && requested > 0 {
		measured = 0 // Requested seconds are authoritative, not container timing drift.
	}
	extension := r.Operation == pebblestore.VideoOperationExtend
	if extension {
		// Never fall back to the combined output's display duration.
		requested = 0
		if IsVeoModel(r.Model) {
			measured = 8 // Provider charges the locked extension, not the concatenated file.
		} else if r.SourceDurationSeconds > 0 && measured > r.SourceDurationSeconds {
			measured -= r.SourceDurationSeconds
		} else {
			measured = 0
		}
	}
	return pebblestore.MediaCostEstimateOptions{
		Provider: r.Provider, Model: r.Model, Kind: "video", Count: 1,
		GeneratedExtensionOutput: extension,
		DurationSeconds:          requested, MeasuredDurationSeconds: measured,
		Resolution: resolution, AspectRatio: r.AspectRatio, IncludesAudio: true,
		IsIteration: r.Operation != "" && r.Operation != pebblestore.VideoOperationCreate,
	}
}
