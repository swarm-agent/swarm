package videogen

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AssembleClips joins independently generated clips in caller order. An assembled
// cut has no provider interaction/resource: it must never masquerade as the last
// scene for native extension. Every input and the output are measured locally.
func AssembleClips(ctx context.Context, clips []ManagedVideoResult) (ManagedVideoResult, error) {
	if len(clips) < 2 || len(clips) > 8 {
		return ManagedVideoResult{}, errors.New("assembly requires 2..8 clips")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ManagedVideoResult{}, errors.New("ffmpeg is required for video assembly")
	}
	dir, err := os.MkdirTemp("", "swarm-video-assembly-")
	if err != nil {
		return ManagedVideoResult{}, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-filter_complex_threads", "1"}
	var filters, streams []string
	var totalBytes int64
	var totalDuration float64
	var width, height int
	for i, clip := range clips {
		totalBytes += int64(len(clip.Bytes))
		if len(clip.Bytes) == 0 || totalBytes > managedVideoMaxBytes {
			return ManagedVideoResult{}, errors.New("assembly input exceeds byte budget or is empty")
		}
		meta, err := (FFprobeVideoProber{}).ProbeVideo(ctx, clip.Bytes)
		if err != nil {
			return ManagedVideoResult{}, fmt.Errorf("probe scene %d: %w", i+1, err)
		}
		if i == 0 {
			width, height = meta.Width, meta.Height
		}
		if meta.Width != width || meta.Height != height {
			return ManagedVideoResult{}, errors.New("scene dimensions must match")
		}
		totalDuration += meta.DurationSeconds
		if totalDuration > 240 {
			return ManagedVideoResult{}, errors.New("assembled video exceeds 240 seconds")
		}
		name := filepath.Join(dir, fmt.Sprintf("scene-%d.mp4", i))
		if err := os.WriteFile(name, clip.Bytes, 0600); err != nil {
			return ManagedVideoResult{}, err
		}
		args = append(args, "-threads", "1", "-protocol_whitelist", "file", "-f", "mov", "-i", name)
		filters = append(filters, fmt.Sprintf("[%d:v:0]setpts=PTS-STARTPTS,setsar=1,fps=24,format=yuv420p[v%d]", i, i))
		if meta.AudioCodec == "" {
			filters = append(filters, fmt.Sprintf("anullsrc=r=48000:cl=stereo,atrim=duration=%.6f,asetpts=PTS-STARTPTS[a%d]", meta.DurationSeconds, i))
		} else {
			filters = append(filters, fmt.Sprintf("[%d:a:0]aresample=48000,aformat=channel_layouts=stereo,apad,atrim=duration=%.6f,asetpts=PTS-STARTPTS[a%d]", i, meta.DurationSeconds, i))
		}
		streams = append(streams, fmt.Sprintf("[v%d][a%d]", i, i))
	}
	filters = append(filters, strings.Join(streams, "")+fmt.Sprintf("concat=n=%d:v=1:a=1[v][a]", len(clips)))
	out := filepath.Join(dir, "assembled.mp4")
	args = append(args, "-filter_complex", strings.Join(filters, ";"), "-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-crf", "20", "-c:a", "aac", "-movflags", "+faststart", "-fs", fmt.Sprint(managedVideoMaxBytes), out)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stderr := newBoundedBuffer(8192)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return ManagedVideoResult{}, fmt.Errorf("assemble video: %w: %s", err, stderr.String())
	}
	stat, err := os.Stat(out)
	if err != nil {
		return ManagedVideoResult{}, err
	}
	if stat.Size() >= managedVideoMaxBytes {
		return ManagedVideoResult{}, errors.New("assembled output exceeds byte budget")
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return ManagedVideoResult{}, err
	}
	meta, err := (FFprobeVideoProber{}).ProbeVideo(ctx, data)
	if err != nil {
		return ManagedVideoResult{}, err
	}
	if math.Abs(meta.DurationSeconds-totalDuration) > 0.5 {
		return ManagedVideoResult{}, errors.New("assembled duration does not match scene total")
	}
	return ManagedVideoResult{Bytes: data, MediaType: "video/mp4", Model: clips[0].Model, Provider: clips[0].Provider, Operation: "create", DurationMs: int(math.Round(meta.DurationSeconds * 1000)), DurationSeconds: int(math.Ceil(meta.DurationSeconds)), Width: meta.Width, Height: meta.Height, AspectRatio: clips[0].AspectRatio, Resolution: clips[0].Resolution}, nil
}
