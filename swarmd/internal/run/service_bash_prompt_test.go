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

func TestMasterHarnessPromptRequiresBoundedTemporaryAndRecursiveWork(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"run-provided TMPDIR",
		"Do not write to a literal /tmp path",
		"durable deliverables in the workspace, not TMPDIR",
		"Do not use repository directories as scratch",
		"bound process fan-out and aggregate output size",
		"Account for descendants and generated files",
		"recursively emit unbounded stdout/stderr or files",
		"cap concurrency/output",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("temporary/recursive workload guidance missing %q", want)
		}
	}
}
