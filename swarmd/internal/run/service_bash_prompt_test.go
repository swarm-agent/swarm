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

func TestMasterHarnessPromptDefinesFlatGlobalWorkspaceAccessLanguage(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"request temporary access for this chat session",
		"add that folder as its own new workspace from the workspace picker",
		"Never describe this as adding or linking the folder to the current workspace or a workspace group",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("workspace access guidance missing %q", want)
		}
	}
	for _, retired := range []string{"persistent add-dir option", "workspace_add_dir"} {
		if strings.Contains(prompt, retired) {
			t.Fatalf("workspace access guidance retains retired wording %q", retired)
		}
	}
}

func TestMasterHarnessPromptDefinesEffectAwareBashClassification(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"category as exactly read, write, update, or delete",
		"Critical reads are exceptional",
		"secrets or credentials",
		"production databases",
		"private customer data",
		"protected system files",
		"large or expensive queries",
		"reads coupled to outbound exfiltration",
		"update is a non-removal in-place mutation and never means removal",
		"delete removes state and always requires critical=true",
		"Routine source reads, listings, searches, status checks, and ordinary local logs are noncritical",
		"For mixed commands, use the highest-impact category",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("effect-aware Bash guidance missing %q", want)
		}
	}
}

func TestMasterHarnessPromptDefinesDesignerIterationLifecycle(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"explicitly requested multiple UI/design iterations or variants",
		"ordinary UI request or a single design is never sufficient",
		"complete design brief",
		"preview/selector",
		"explicit selection",
		"Designer output defaults to managed artifacts",
		"server inject one trusted parent-session collection",
		"Managed Designers must use manage_artifact",
		"must not write/edit the checkout",
		"Workspace Designers may write/edit only their declared scope",
		"must not use managed output",
		"parent-owned reusable artifacts, not disposable proposals",
		"retain several, revise one, or promote one",
		"never mandate automatic deletion",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Designer lifecycle guidance missing %q", want)
		}
	}
}

func TestMasterHarnessPromptSeparatesIterationSwarmFromRegularLaunchFields(t *testing.T) {
	prompt := masterHarnessPrompt(t.TempDir())

	for _, want := range []string{
		"complete shared prompt, agent_type, count",
		"omit launches and all regular-launch-only fields",
		"top-level concurrency_reason",
		"Swarm concurrency comes only from count",
		"These per-launch fields do not apply to mode=swarm",
		`"mode":"swarm","description":"Create landscape video iterations"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Iteration Swarm field-boundary guidance missing %q", want)
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
