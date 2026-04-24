package model

import "strings"

const CodexContextMode1M = "1m"
const (
	CodexGPT54DefaultContextWindow = 272_000
	CodexGPT54LargeContextWindow   = 1_050_000
	CodexGPT55DefaultContextWindow = 400_000
	CodexGPT55LargeContextWindow   = 1_050_000
)

func SupportsCodexFastMode(provider, modelName string) bool {
	return isCodexModel(provider, modelName, "gpt-5.4") || isCodexModel(provider, modelName, "gpt-5.5")
}

func isCodexModel(provider, modelName, target string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "codex") && strings.EqualFold(strings.TrimSpace(modelName), target)
}

func IsCodexGPT55Model(provider, modelName string) bool {
	return isCodexModel(provider, modelName, "gpt-5.5")
}

func CodexFastEnabled(provider, modelName, serviceTier string) bool {
	return SupportsCodexFastMode(provider, modelName) && strings.EqualFold(strings.TrimSpace(serviceTier), "fast")
}

func Codex1MEnabled(provider, modelName, contextMode string) bool {
	return SupportsCodexFastMode(provider, modelName) && strings.EqualFold(strings.TrimSpace(contextMode), CodexContextMode1M)
}

func CodexContextWindow(provider, modelName, contextMode string, fallback int) int {
	if isCodexModel(provider, modelName, "gpt-5.4") {
		if Codex1MEnabled(provider, modelName, contextMode) {
			return CodexGPT54LargeContextWindow
		}
		return CodexGPT54DefaultContextWindow
	}
	if IsCodexGPT55Model(provider, modelName) {
		if Codex1MEnabled(provider, modelName, contextMode) {
			return CodexGPT55LargeContextWindow
		}
		return CodexGPT55DefaultContextWindow
	}
	return fallback
}
