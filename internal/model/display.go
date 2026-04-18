package model

import "strings"

const fireworksModelPrefix = "accounts/fireworks/models/"

func DisplayModelName(provider, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(provider), "fireworks") && strings.HasPrefix(strings.ToLower(modelName), fireworksModelPrefix) {
		return strings.TrimSpace(modelName[len(fireworksModelPrefix):])
	}
	return modelName
}

func DisplayModelLabel(provider, modelName, serviceTier, contextMode string) string {
	displayName := DisplayModelName(provider, modelName)
	if displayName == "" {
		return "unset"
	}
	suffixes := make([]string, 0, 2)
	if CodexFastEnabled(provider, modelName, serviceTier) {
		suffixes = append(suffixes, "fast")
	}
	if Codex1MEnabled(provider, modelName, contextMode) {
		suffixes = append(suffixes, "1m")
	}
	if len(suffixes) > 0 {
		return displayName + " (" + strings.Join(suffixes, ",") + ")"
	}
	return displayName
}
