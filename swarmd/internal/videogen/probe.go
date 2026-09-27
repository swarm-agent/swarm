package videogen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// VideoMetadata contains measured media attributes probed from actual video bytes.
type VideoMetadata struct {
	DurationSeconds float64 `json:"duration_seconds"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	SizeBytes       int64   `json:"size_bytes"`
	FormatName      string  `json:"format_name"`
	VideoCodec      string  `json:"video_codec"`
	AudioCodec      string  `json:"audio_codec"`
}

// VideoProber defines the media probe contract for measuring video duration and dimensions.
type VideoProber interface {
	ProbeVideo(ctx context.Context, videoBytes []byte) (VideoMetadata, error)
}

// FFprobeVideoProber probes video metadata using the system ffprobe binary.
type FFprobeVideoProber struct{}

// ProbeVideo writes videoBytes to a temporary file and executes ffprobe.
func (p FFprobeVideoProber) ProbeVideo(ctx context.Context, videoBytes []byte) (VideoMetadata, error) {
	if len(videoBytes) == 0 {
		return VideoMetadata{}, errors.New("cannot probe empty video bytes")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return VideoMetadata{}, fmt.Errorf("ffprobe runtime is required but not installed or not in PATH: %w", err)
	}
	tmpFile, err := os.CreateTemp("", "swarm-videogen-probe-*.mp4")
	if err != nil {
		return VideoMetadata{}, fmt.Errorf("create temp video probe file: %w", err)
	}
	defer func() {
		_ = os.Remove(tmpFile.Name())
	}()
	if _, err := tmpFile.Write(videoBytes); err != nil {
		_ = tmpFile.Close()
		return VideoMetadata{}, fmt.Errorf("write temp video probe file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return VideoMetadata{}, fmt.Errorf("close temp video probe file: %w", err)
	}

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration,size,bit_rate,format_name:stream=index,codec_type,codec_name,width,height",
		"-of", "json",
		tmpFile.Name(),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return VideoMetadata{}, fmt.Errorf("ffprobe failed: %v: %s", err, stderr.String())
	}

	var parsed struct {
		Format struct {
			Duration   string `json:"duration"`
			Size       string `json:"size"`
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return VideoMetadata{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	meta := VideoMetadata{
		FormatName: parsed.Format.FormatName,
		SizeBytes:  int64(len(videoBytes)),
	}
	if dur, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil {
		meta.DurationSeconds = dur
	}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && meta.VideoCodec == "" {
			meta.VideoCodec = stream.CodecName
			meta.Width = stream.Width
			meta.Height = stream.Height
		} else if stream.CodecType == "audio" && meta.AudioCodec == "" {
			meta.AudioCodec = stream.CodecName
		}
	}
	return meta, nil
}
