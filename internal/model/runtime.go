package model

import "strings"

const CodexContextMode1M = "1m"
const (
	CodexGPT54DefaultContextWindow = 272_000
	CodexGPT54LargeContextWindow   = 1_050_000
	CodexGPT55ContextWindow        = 400_000
)

func SupportsCodexFastMode(provider, modelName string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "codex") && strings.EqualFold(strings.TrimSpace(modelName), "gpt-5.4")
}

func IsCodexGPT55Model(provider, modelName string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "codex") && strings.EqualFold(strings.TrimSpace(modelName), "gpt-5.5")
}

func CodexFastEnabled(provider, modelName, serviceTier string) bool {
	return SupportsCodexFastMode(provider, modelName) && strings.EqualFold(strings.TrimSpace(serviceTier), "fast")
}

func Codex1MEnabled(provider, modelName, contextMode string) bool {
	return SupportsCodexFastMode(provider, modelName) && strings.EqualFold(strings.TrimSpace(contextMode), CodexContextMode1M)
}

func CodexContextWindow(provider, modelName, contextMode string, fallback int) int {
	if SupportsCodexFastMode(provider, modelName) {
		if Codex1MEnabled(provider, modelName, contextMode) {
			return CodexGPT54LargeContextWindow
		}
		return CodexGPT54DefaultContextWindow
	}
	if IsCodexGPT55Model(provider, modelName) {
		return CodexGPT55ContextWindow
	}
	return fallback
}
