package run

import (
	"strings"
	"testing"
)

func TestMasterHarnessPromptRequiresConciseRiskFocusedBashIntent(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"one direct, human-scannable sentence",
		"do not narrate obvious shell mechanics",
		"stdout/stderr capture",
		"generic build-artifact behavior",
		"multiple concise items only",
		"listeners and ports opened",
		"public network exposure",
		"privileges used",
		"destructive actions",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Bash guidance missing %q", want)
		}
	}
}
