package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestChatFooterSettingsLine_IncludesSwarmModeAgentModelThinking(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "swarm.name",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
		Meta: ChatSessionMeta{
			Agent: "swarm",
		},
	})

	line := p.footerSettingsLine(1000)
	if !strings.Contains(line, "swarm.name") {
		t.Fatalf("settings line missing swarm name: %q", line)
	}
	if !strings.Contains(line, "auto") {
		t.Fatalf("settings line missing mode value: %q", line)
	}
	if !strings.Contains(line, "[a:swarm]") {
		t.Fatalf("settings line missing agent token: %q", line)
	}
	if !strings.Contains(line, "[m:gpt-5-codex]") {
		t.Fatalf("settings line missing model token: %q", line)
	}
	if !strings.Contains(line, "[t:high]") {
		t.Fatalf("settings line missing thinking token: %q", line)
	}
}

func TestChatFooterTokenRow_TruncatesCleanlyByWidth(t *testing.T) {
	tokens := []footerToken{
		{Text: "swarm.name", Style: tcell.StyleDefault},
		{Text: "auto", Style: tcell.StyleDefault},
		{Text: "[a:swarm]", Style: tcell.StyleDefault},
		{Text: "[m:gpt-5-codex]", Style: tcell.StyleDefault},
	}
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(24, 1)

	drawFooterTokenRow(screen, 0, 0, 24, tokens)
	line := dumpScreenText(screen, 24, 1)
	if !strings.Contains(line, "swarm.name") {
		t.Fatalf("token row missing first token: %q", line)
	}
	if strings.Contains(line, "m:gpt-5-codex") {
		t.Fatalf("token row should drop tokens that do not fit: %q", line)
	}
}

func TestChatFooterWorkspaceLine_IncludesWorkspaceBranchAndPath(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Branch:    "feature/test",
			Path:      "/tmp/workspace/project",
		},
	})

	line := p.footerWorkspaceLine(1000)
	if !strings.Contains(line, "workspace workspace-1") {
		t.Fatalf("workspace line missing workspace label/value: %q", line)
	}
	if !strings.Contains(line, "branch feature/test") {
		t.Fatalf("workspace line missing branch label/value: %q", line)
	}
	if !strings.Contains(line, "cwd /tmp/workspace/project") {
		t.Fatalf("workspace line missing cwd label/value: %q", line)
	}
}

func TestChatFooterInfoLine_IncludesWorkspaceFields(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Plan:      "plan-alpha",
		},
		ModelName: "gpt-5-codex",
	})
	p.SetStatus("running turn")

	line := p.footerInfoLine(1000)
	if !strings.Contains(line, "mode auto") {
		t.Fatalf("line missing mode label/value: %q", line)
	}
	if !strings.Contains(line, "workspace workspace-1") {
		t.Fatalf("line missing workspace label/value: %q", line)
	}
	if !strings.Contains(line, "cwd /tmp/workspace/project") {
		t.Fatalf("line missing cwd label/value: %q", line)
	}
	if strings.Contains(line, "plan") || strings.Contains(line, "plan-alpha") {
		t.Fatalf("wide footer should omit plan segment: %q", line)
	}
	if strings.Contains(line, "status") {
		t.Fatalf("line should not include status segment: %q", line)
	}
}

func TestChatFooterInfoLine_UpdatesPlanLabel(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Plan:      "none",
		},
	})

	before := p.footerInfoLine(1000)
	if strings.Contains(before, "plan none") {
		t.Fatalf("line should not include default plan placeholder: %q", before)
	}
	if strings.Contains(before, "plan ") {
		t.Fatalf("line should not include plan segment when no active plan: %q", before)
	}

	p.SetActivePlan("Ship Ready")
	after := p.footerInfoLine(1000)
	if strings.Contains(after, "plan ") || strings.Contains(after, "Ship Ready") {
		t.Fatalf("wide footer should omit active plan segment: %q", after)
	}
	if !strings.Contains(after, "mode auto") || !strings.Contains(after, "workspace workspace-1") || !strings.Contains(after, "cwd /tmp/workspace/project") {
		t.Fatalf("wide footer should keep labeled mode/workspace/cwd segments: %q", after)
	}
}

func TestChatFooterInfoLine_ThinLayoutHidesVerboseMeta(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})

	line := p.footerInfoLine(60)
	if strings.Contains(line, "mode ") || strings.Contains(line, "workspace ") || strings.Contains(line, "cwd ") {
		t.Fatalf("thin footer should hide verbose metadata labels: %q", line)
	}
	if strings.Contains(line, "plan ") {
		t.Fatalf("thin footer should not include plan label when no active plan: %q", line)
	}
	if !strings.Contains(line, "auto") {
		t.Fatalf("thin footer should still show mode value: %q", line)
	}
	if !strings.Contains(line, "workspace-1") {
		t.Fatalf("thin footer should still show workspace value: %q", line)
	}
	if !strings.Contains(line, "/tmp/workspace/project") {
		t.Fatalf("thin footer should still show cwd value: %q", line)
	}
	if strings.Contains(strings.ToLower(line), "none") {
		t.Fatalf("thin footer should not show none placeholder: %q", line)
	}

	p.SetActivePlan("Ship Ready")
	withPlan := p.footerInfoLine(60)
	if strings.Contains(withPlan, "mode ") || strings.Contains(withPlan, "workspace ") || strings.Contains(withPlan, "cwd ") {
		t.Fatalf("thin footer should hide verbose metadata labels even with active plan: %q", withPlan)
	}
	if strings.Contains(withPlan, "plan ") {
		t.Fatalf("thin footer should not include plan label in compact mode: %q", withPlan)
	}
	if !strings.Contains(withPlan, "Ship Ready") {
		t.Fatalf("thin footer should show active plan value: %q", withPlan)
	}
}

func TestChatFooterFocusLine_UsesCompactSettingsOnly(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
		ModelName:     "gpt-5-codex",
		ThinkingLevel: "high",
	})

	line := p.footerSettingsLine(1000)
	if !strings.Contains(line, "[a:swarm]") {
		t.Fatalf("settings line missing compact agent chip: %q", line)
	}
	if !strings.Contains(line, "[m:gpt-5-codex]") {
		t.Fatalf("settings line missing compact model chip: %q", line)
	}
	if !strings.Contains(line, "[t:high]") {
		t.Fatalf("settings line missing compact thinking chip: %q", line)
	}
	if strings.Contains(line, ":1.0k") {
		t.Fatalf("settings line should not include context usage: %q", line)
	}
	if strings.Contains(line, "ctx") || strings.Contains(line, "context") {
		t.Fatalf("settings line should not include ctx/context label: %q", line)
	}
}

func TestChatFooterFocusLine_AppendsCodexRuntimeFlagsForGPT54(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		ServiceTier:    "fast",
		ContextMode:    "1m",
		Meta:           ChatSessionMeta{Agent: "swarm"},
	})

	line := p.footerSettingsLine(1000)
	if !strings.Contains(line, "m:gpt-5.4 (fast)") {
		t.Fatalf("settings line missing model chip with fast suffix: %q", line)
	}
}

func TestChatFooterFocusLine_HidesCodexRuntimeFlagsForNonGPT54(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		ModelProvider:  "codex",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
		ServiceTier:    "fast",
		ContextMode:    "1m",
		Meta:           ChatSessionMeta{Agent: "swarm"},
	})

	line := p.footerSettingsLine(1000)
	if strings.Contains(line, "(fast)") {
		t.Fatalf("settings line should hide fast suffix for non-gpt-5.4 models: %q", line)
	}
}

func TestChatFooterFocusLine_HidesCodexRuntimeFlagsForNonCodexProvider(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		ModelProvider:  "google",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		ServiceTier:    "fast",
		ContextMode:    "1m",
		Meta:           ChatSessionMeta{Agent: "swarm"},
	})

	line := p.footerSettingsLine(1000)
	if strings.Contains(line, "(fast)") {
		t.Fatalf("settings line should hide fast suffix for non-codex providers: %q", line)
	}
}

func TestChatFooterContextUsageLabel_UsesPercentFormat(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})

	label := p.footerContextUsageLabel()
	if label != "" {
		t.Fatalf("context usage label = %q, want hidden label before backend summary", label)
	}

	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     250,
		CacheReadTokens: 0,
		RemainingTokens: 750,
	})
	label = p.footerContextUsageLabel()
	if strings.Contains(label, "ctx") || strings.Contains(label, "context") {
		t.Fatalf("context usage label should not include ctx/context text label: %q", label)
	}
	if label != "75% left" {
		t.Fatalf("context usage label = %q, want compact percent-left label", label)
	}
}

func TestChatFooterContextUsageLabel_UsesUsageSummaryWithCacheAdjustment(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})

	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     500,
		CacheReadTokens: 300,
		RemainingTokens: 800,
	})

	label := p.footerContextUsageLabel()
	if label != "80% left" {
		t.Fatalf("context usage label = %q, want compact backend-summary usage chip", label)
	}
}

func TestChatFooterRightLine_ShowsWorktreeThenContextUsage(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace:       "workspace-1",
			Path:            "/tmp/workspace/project",
			WorktreeEnabled: true,
		},
	})
	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     500,
		CacheReadTokens: 300,
		RemainingTokens: 800,
	})

	if got := p.footerRightLine(1000); got != "wt on  80% left" {
		t.Fatalf("footerRightLine = %q, want worktree before compact usage label", got)
	}
}

func TestChatFooterRightLine_HidesWorktreeIndicatorWhenDisabled(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})
	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     250,
		CacheReadTokens: 0,
		RemainingTokens: 750,
	})

	if got := p.footerRightLine(1000); got != "75% left" {
		t.Fatalf("footerRightLine = %q, want compact usage label without worktree", got)
	}
}

func TestChatFooterBarRendersWorktreeIndicatorOnRight(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		ContextWindow:  1000,
		AuthConfigured: true,
		SwarmName:      "swarm.name",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		Meta: ChatSessionMeta{
			Agent:           "swarm",
			WorktreeEnabled: true,
		},
	})
	p.applyContextUsageSummary(&ChatUsageSummary{
		ContextWindow:   1000,
		TotalTokens:     500,
		CacheReadTokens: 300,
		RemainingTokens: 800,
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 24)

	p.Draw(screen)

	text := dumpScreenText(screen, 120, 24)
	if !strings.Contains(text, "wt on  80% left") {
		t.Fatalf("expected chat footer to render compact worktree/context indicator on the right, got:\n%s", text)
	}
}

func TestChatFooterRegistersClickableAMTChips(t *testing.T) {
	p := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		SessionMode:    "auto",
		AuthConfigured: true,
		SwarmName:      "swarm.name",
		ModelProvider:  "codex",
		ModelName:      "gpt-5-codex",
		ThinkingLevel:  "high",
		Meta: ChatSessionMeta{
			Agent: "swarm",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 24)

	p.Draw(screen)

	if len(p.footerTargets) < 3 {
		t.Fatalf("footerTargets = %d, want at least 3", len(p.footerTargets))
	}

	want := map[string]bool{
		"open-agents-modal": false,
		"open-models-modal": false,
		"cycle-thinking":    false,
	}
	for _, target := range p.footerTargets {
		if _, ok := want[target.Action]; ok {
			want[target.Action] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Fatalf("missing footer target for %s", action)
		}
	}
}
