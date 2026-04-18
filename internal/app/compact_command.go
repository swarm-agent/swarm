package app

import (
	"fmt"
	"strconv"
	"strings"
)

const compactThresholdMetadataKey = "context_compaction_threshold_percent"

type compactCommandOptions struct {
	Note             string
	ThresholdPercent float64
	HasThreshold     bool
}

func parseCompactCommandArgs(args []string) compactCommandOptions {
	tokens := make([]string, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed != "" {
			tokens = append(tokens, trimmed)
		}
	}
	if len(tokens) == 0 {
		return compactCommandOptions{}
	}

	parsed := compactCommandOptions{}
	switch {
	case len(tokens) >= 1:
		if threshold, ok := parseCompactThresholdToken(tokens[0]); ok {
			parsed.ThresholdPercent = threshold
			parsed.HasThreshold = true
			tokens = tokens[1:]
			break
		}
		fallthrough
	case len(tokens) >= 2:
		if strings.EqualFold(tokens[0], "threshold") {
			if threshold, ok := parseCompactThresholdToken(tokens[1]); ok {
				parsed.ThresholdPercent = threshold
				parsed.HasThreshold = true
				tokens = tokens[2:]
			}
		}
	}
	parsed.Note = strings.TrimSpace(strings.Join(tokens, " "))
	return parsed
}

func parseCompactThresholdToken(token string) (float64, bool) {
	token = strings.TrimSpace(token)
	if !strings.HasSuffix(token, "%") {
		return 0, false
	}
	raw := strings.TrimSpace(strings.TrimSuffix(token, "%"))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return value, true
}

func formatCompactThresholdPercent(value float64) string {
	value = normalizeCompactThresholdPercent(value)
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.0f%%", value)
	}
	return fmt.Sprintf("%.1f%%", value)
}

func normalizeCompactThresholdPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
