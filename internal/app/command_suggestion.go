package app

import "strings"

func suggestKnownCommand(raw string, devMode bool) string {
	query := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "/")))
	if query == "" {
		return ""
	}

	best := ""
	bestScore := 0
	for _, suggestion := range buildHomeCommandSuggestions(devMode) {
		command := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(suggestion.Command, "/")))
		if command == "" {
			continue
		}

		score := 0
		switch {
		case strings.HasPrefix(command, query) || strings.HasPrefix(query, command):
			score = 3
		case isSingleEditCommandTypo(query, command):
			score = 2
		case strings.Contains(command, query) || strings.Contains(query, command):
			score = 1
		}
		if score == 0 {
			continue
		}
		if score > bestScore || (score == bestScore && (best == "" || len(command) < len(strings.TrimPrefix(best, "/")) || (len(command) == len(strings.TrimPrefix(best, "/")) && command < strings.TrimPrefix(best, "/")))) {
			best = suggestion.Command
			bestScore = score
		}
	}
	return best
}

func isSingleEditCommandTypo(left, right string) bool {
	if left == right {
		return true
	}
	if left == "" || right == "" {
		return false
	}
	if absInt(len(left)-len(right)) > 1 {
		return false
	}
	if len(left) == len(right) {
		mismatches := 0
		for i := 0; i < len(left); i++ {
			if left[i] == right[i] {
				continue
			}
			mismatches++
			if mismatches > 1 {
				return false
			}
		}
		return mismatches == 1
	}
	if len(left) > len(right) {
		left, right = right, left
	}
	i, j, skipped := 0, 0, false
	for i < len(left) && j < len(right) {
		if left[i] == right[j] {
			i++
			j++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		j++
	}
	return true
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
