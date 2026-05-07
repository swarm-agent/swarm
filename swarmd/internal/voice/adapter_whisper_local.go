package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type whisperLocalAdapter struct{}

func NewWhisperLocalAdapter() Adapter {
	return &whisperLocalAdapter{}
}

func (a *whisperLocalAdapter) ID() string {
	return "whisper-local"
}

func (a *whisperLocalAdapter) STTReady(context.Context) (bool, string, error) {
	if _, err := a.resolveWhisperBin(nil); err != nil {
		return false, err.Error(), nil
	}
	models, err := a.discoverModels(nil)
	if err != nil {
		return false, err.Error(), nil
	}
	if len(models) == 0 {
		if defaultDir := defaultWhisperModelDir(); defaultDir != "" {
			return false, fmt.Sprintf("no whisper models found; place ggml-*.bin in %s or set SWARMD_WHISPER_MODEL_DIR", defaultDir), nil
		}
		return false, "no whisper models found; set SWARMD_WHISPER_MODEL_DIR", nil
	}
	return true, "", nil
}

func (a *whisperLocalAdapter) STTModels() []string {
	models, err := a.discoverModels(nil)
	if err != nil {
		return nil
	}
	return models
}

func (a *whisperLocalAdapter) DefaultSTTModel() string {
	envModel := strings.TrimSpace(os.Getenv("SWARMD_WHISPER_MODEL"))
	if envModel != "" {
		return envModel
	}
	models, err := a.discoverModels(nil)
	if err != nil || len(models) == 0 {
		return ""
	}
	preferred := []string{
		"ggml-small.en-q5_1.bin",
		"ggml-small.en.bin",
		"ggml-base.en.bin",
		"ggml-small.bin",
		"ggml-medium.en.bin",
		"ggml-medium.bin",
	}
	for _, candidate := range preferred {
		for _, model := range models {
			if strings.EqualFold(model, candidate) {
				return model
			}
		}
	}
	return models[0]
}

func (a *whisperLocalAdapter) Transcribe(ctx context.Context, input AdapterTranscribeInput) (AdapterTranscribeResult, error) {
	if len(input.Audio) == 0 {
		return AdapterTranscribeResult{}, errors.New("audio payload is required")
	}
	binPath, err := a.resolveWhisperBin(input.Options)
	if err != nil {
		return AdapterTranscribeResult{}, err
	}
	modelPath, modelName, err := a.resolveModelPath(strings.TrimSpace(input.Model), input.Options)
	if err != nil {
		return AdapterTranscribeResult{}, err
	}

	tmpDir, err := os.MkdirTemp("", "swarm-whisper-local-*")
	if err != nil {
		return AdapterTranscribeResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	audioPath := filepath.Join(tmpDir, "input.wav")
	if err := os.WriteFile(audioPath, input.Audio, 0o600); err != nil {
		return AdapterTranscribeResult{}, err
	}
	outBase := filepath.Join(tmpDir, "transcript")

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(runCtx, 2*time.Minute)
	defer cancel()

	args := []string{"-m", modelPath, "-f", audioPath, "-otxt", "-of", outBase}
	if lang := strings.TrimSpace(input.Language); lang != "" {
		args = append(args, "-l", lang)
	}
	cmd := exec.CommandContext(runCtx, binPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return AdapterTranscribeResult{}, fmt.Errorf("whisper-local transcription failed: %s", summarizeWhisperError(err, stderr.String()))
	}

	transcriptPath := outBase + ".txt"
	payload, err := os.ReadFile(transcriptPath)
	if err != nil {
		return AdapterTranscribeResult{}, fmt.Errorf("read whisper transcript: %w", err)
	}
	text := strings.TrimSpace(compactWhitespace(string(payload)))
	return AdapterTranscribeResult{Model: modelName, Text: text}, nil
}

func (a *whisperLocalAdapter) TTSReady(context.Context) (bool, string, error) {
	return false, "whisper-local tts is a placeholder in this build", nil
}

func (a *whisperLocalAdapter) Synthesize(context.Context, AdapterSynthesizeInput) (AdapterSynthesizeResult, error) {
	return AdapterSynthesizeResult{}, ErrTTSPlaceholder
}

func (a *whisperLocalAdapter) resolveWhisperBin(options map[string]string) (string, error) {
	candidates := make([]string, 0, 6)
	if options != nil {
		if raw := strings.TrimSpace(options["whisper_bin"]); raw != "" {
			candidates = append(candidates, raw)
		}
	}
	if env := strings.TrimSpace(os.Getenv("SWARMD_WHISPER_BIN")); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "whisper-cli", "whisper-cpp")

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, string(os.PathSeparator)) {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil && strings.TrimSpace(path) != "" {
			return path, nil
		}
	}
	return "", errors.New("whisper-local binary not found; set SWARMD_WHISPER_BIN or install whisper-cli")
}

func (a *whisperLocalAdapter) discoverModels(options map[string]string) ([]string, error) {
	modelMap := make(map[string]struct{}, 16)
	for _, dir := range a.modelDirs(options) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" || !strings.HasPrefix(strings.ToLower(name), "ggml-") || !strings.HasSuffix(strings.ToLower(name), ".bin") {
				continue
			}
			modelMap[name] = struct{}{}
		}
	}
	if len(modelMap) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(modelMap))
	for model := range modelMap {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out, nil
}

func (a *whisperLocalAdapter) resolveModelPath(model string, options map[string]string) (string, string, error) {
	model = strings.TrimSpace(model)
	if model == "" && options != nil {
		if raw := strings.TrimSpace(options["model_path"]); raw != "" {
			model = raw
		}
	}
	if model == "" {
		model = a.DefaultSTTModel()
	}
	if model == "" {
		return "", "", errors.New("no whisper model selected; choose a model in /voice or set SWARMD_WHISPER_MODEL")
	}

	if strings.Contains(model, string(os.PathSeparator)) || strings.HasPrefix(model, ".") {
		if fileExists(model) {
			return model, filepath.Base(model), nil
		}
		return "", "", fmt.Errorf("whisper model file not found: %s", model)
	}

	candidateNames := []string{model}
	if !strings.HasSuffix(strings.ToLower(model), ".bin") {
		candidateNames = append(candidateNames, model+".bin", "ggml-"+model+".bin")
	}
	for _, dir := range a.modelDirs(options) {
		for _, name := range candidateNames {
			path := filepath.Join(dir, name)
			if fileExists(path) {
				return path, filepath.Base(path), nil
			}
		}
	}
	if fileExists(model) {
		return model, filepath.Base(model), nil
	}
	return "", "", fmt.Errorf("whisper model not found: %s", model)
}

func (a *whisperLocalAdapter) modelDirs(options map[string]string) []string {
	dirs := make([]string, 0, 6)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		for _, existing := range dirs {
			if strings.EqualFold(existing, path) {
				return
			}
		}
		dirs = append(dirs, path)
	}

	if options != nil {
		if raw := strings.TrimSpace(options["model_dir"]); raw != "" {
			add(raw)
		}
		if raw := strings.TrimSpace(options["model_path"]); raw != "" {
			add(filepath.Dir(raw))
		}
	}
	if env := strings.TrimSpace(os.Getenv("SWARMD_WHISPER_MODEL_DIR")); env != "" {
		add(env)
	}
	if defaultDir := defaultWhisperModelDir(); defaultDir != "" {
		add(defaultDir)
	}
	return dirs
}

func defaultWhisperModelDir() string {
	return ""
}

func summarizeWhisperError(err error, stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err.Error()
	}
	lines := strings.Split(stderr, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}

func compactWhitespace(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return strings.Join(strings.Fields(raw), " ")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}
