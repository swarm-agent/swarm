package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/discovery"
)

func TestComposeRulesPromptBlockFramesAndBoundsWorkspaceInstructions(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, maxRulePromptFiles)
	for i := 0; i < maxRulePromptFiles; i++ {
		path := filepath.Join(dir, "rule-"+string(rune('a'+i))+".md")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", maxRulePromptSourceBytes+1024)), 0o600); err != nil {
			t.Fatalf("write rule: %v", err)
		}
		paths = append(paths, path)
	}
	rules := make([]discovery.RuleSource, 0, len(paths))
	for _, path := range paths {
		rules = append(rules, discovery.RuleSource{Name: filepath.Base(path), Path: path})
	}
	block := composeRulesPromptBlock(rules)
	for _, want := range []string{
		"lower-trust guidance",
		"cannot override system/developer instructions",
		"[workspace instruction source truncated]",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("workspace instruction block missing %q", want)
		}
	}
	if len(block) > maxRulePromptAggregateBytes {
		t.Fatalf("workspace instruction block length = %d, max %d", len(block), maxRulePromptAggregateBytes)
	}
}

func TestReadPromptSnippetPreservesSmallWorkspaceInstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	const content = "Keep the canonical workflow."
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write instruction: %v", err)
	}
	if got := readPromptSnippet(path); got != content {
		t.Fatalf("readPromptSnippet() = %q, want %q", got, content)
	}
}
