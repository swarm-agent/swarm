package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func dumpScreenText(screen tcell.Screen, width, height int) string {
	var out strings.Builder
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			main, _, _, _ := screen.GetContent(x, y)
			if main == 0 {
				main = ' '
			}
			out.WriteRune(main)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

func lineIndexContaining(lines []string, needle string) int {
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

func TestHomeDrawCompactDoesNotHardFail(t *testing.T) {
	page := NewHomePage(model.EmptyHome())

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(56, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 56, 24)
	if strings.Contains(text, "Terminal too small") || strings.Contains(text, "Need at least") {
		t.Fatalf("home draw should render compact layout, got:\n%s", text)
	}
}

func TestHomeDrawCompactPinsMetaTopAndCentersBody(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		Directories: []model.DirectoryItem{
			{
				Name:        "swarm",
				Path:        "/workspace/swarm",
				Branch:      "main",
				DirtyCount:  3,
				IsWorkspace: true,
			},
		},
		RecentSessions: []model.SessionSummary{
			{ID: "1", Title: "first", UpdatedAgo: "1m"},
			{ID: "2", Title: "second", UpdatedAgo: "2m"},
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(56, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 56, 24)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("no lines rendered")
	}
	metaY := lineIndexContaining(lines, "workspace:")
	if metaY < 0 {
		t.Fatalf("workspace meta line not rendered:\n%s", text)
	}
	if metaY > 2 {
		t.Fatalf("workspace meta should stay near top, got y=%d\n%s", metaY, text)
	}
	inputY := lineIndexContaining(lines, "› ")
	if inputY < 0 {
		t.Fatalf("input line not rendered:\n%s", text)
	}
	if inputY <= metaY+2 {
		t.Fatalf("input should be below top-pinned meta, got metaY=%d inputY=%d\n%s", metaY, inputY, text)
	}
}

func TestHomeBottomStatusUsesAvailableWidth(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		ServerMode:    "local",
		ModelProvider: "codex",
		ModelName:     "gpt-5.4",
		ThinkingLevel: "xhigh",
		ActiveAgent:   "swarm",
	})
	status := "warning: provider status unavailable; run /auth key openai <api_key> to recover fully"
	page.SetStatus(status)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 24)
	if !strings.Contains(text, status) {
		t.Fatalf("expected full status line to render without one-third truncation; status=%q\nrendered:\n%s", status, text)
	}
	for _, token := range []string{" local ", " plan ", " [a:swarm] ", " [m:gpt-5.4] ", " [t:xhigh] "} {
		if !strings.Contains(text, token) {
			t.Fatalf("expected footer metadata to include %q, got:\n%s", token, text)
		}
	}
	if strings.Contains(text, " wt on ") {
		t.Fatalf("expected footer metadata to omit worktree indicator when disabled, got:\n%s", text)
	}
	if strings.Contains(text, "% left") {
		t.Fatalf("expected footer metadata to omit compact context usage when no session summary is available, got:\n%s", text)
	}
}

func TestHomeBottomStatusShowsWorktreeIndicatorWhenEnabled(t *testing.T) {
	page := NewHomePage(model.HomeModel{WorktreesEnabled: true, ServerMode: "local", ActiveAgent: "swarm"})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 24)
	if !strings.Contains(text, " local ") || !strings.Contains(text, " plan ") || !strings.Contains(text, " [a:swarm] ") {
		t.Fatalf("expected footer metadata to include runtime/mode/agent chips, got:\n%s", text)
	}
	if !strings.Contains(text, " wt on ") {
		t.Fatalf("expected footer metadata to show worktree indicator when enabled, got:\n%s", text)
	}
}

func TestHomeBottomStatusShowsWorktreeThenCompactUsageWhenSelectedSessionHasSummary(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		WorktreesEnabled: true,
		ServerMode:       "local",
		ActiveAgent:      "swarm",
		RecentSessions: []model.SessionSummary{{
			ID:    "session-1",
			Title: "first",
			Metadata: map[string]any{
				"context_window":   1000,
				"remaining_tokens": int64(800),
			},
		}},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(120, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 24)
	if !strings.Contains(text, "wt on  80% left") {
		t.Fatalf("expected home footer to render worktree before compact usage on the right, got:\n%s", text)
	}
}

func TestHomeCommandPaletteShowsInlineOptionsOnSingleRow(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.SetCommandSuggestions([]CommandSuggestion{{
		Command:   "/codex",
		Hint:      "Codex commands",
		QuickTips: []string{"/codex status", "/codex fast", "/fast"},
	}})
	page.SetPrompt("/codex")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(56, 18)
	page.Draw(screen)

	text := dumpScreenText(screen, 56, 18)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	row := ""
	for _, line := range lines {
		if strings.Contains(line, "/codex") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("expected /codex row, got:\n%s", text)
	}
	if !strings.Contains(row, "[status]") || !strings.Contains(row, "[fast]") {
		t.Fatalf("expected inline options on selected row, got: %q", row)
	}
	for _, line := range lines {
		if line != row && (strings.Contains(line, "[status]") || strings.Contains(line, "[fast]") || strings.Contains(line, "[/fast]")) {
			t.Fatalf("expected inline options on one row only, got:\n%s", text)
		}
	}
}

func TestHomeAuthModalCallbackRendersWrappedFullAuthURL(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.ShowAuthModal()
	page.authModal.Login = &authModalLoginState{
		Provider:    "codex",
		Active:      true,
		Method:      "code",
		OpenBrowser: false,
	}
	longAuthURL := "https://auth.example.com/oauth/authorize?client_id=abc123&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&scope=openid%20profile%20email%20offline_access&state=very-long-state-token&code_challenge=abcdefghijklmnopqrstuvwxyz0123456789"
	page.StartAuthModalCodexCallbackPrompt("", longAuthURL)

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(86, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 86, 24)
	if !strings.Contains(text, "https://auth.example.com/oauth/authorize?") {
		t.Fatalf("expected beginning of auth URL in modal render, got:\n%s", text)
	}
	if !strings.Contains(text, "[Copy auth URL to clipboard]") {
		t.Fatalf("expected copy-url button in modal render, got:\n%s", text)
	}
	if !strings.Contains(text, "swarm ctl auth codex login --method code") || !strings.Contains(text, "--no-open") {
		t.Fatalf("expected remote troubleshooting command in modal render, got:\n%s", text)
	}
}

func TestHomeAuthModalCopilotEmptyCredentialsShowsVerifyInstructions(t *testing.T) {
	page := NewHomePage(model.EmptyHome())
	page.SetAuthModalData(
		[]AuthModalProvider{{ID: "copilot", Ready: false, Runnable: false, Reason: "not authenticated"}},
		nil,
	)
	page.ShowAuthModal()
	page.authModal.Focus = authModalFocusCredentials

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(110, 28)
	page.Draw(screen)

	text := dumpScreenText(screen, 110, 28)
	if !strings.Contains(text, "Copilot uses sidecar auth (no credentials listed here).") {
		t.Fatalf("expected copilot sidecar message, got:\n%s", text)
	}
	if !strings.Contains(text, "run `copilot login` in terminal.") {
		t.Fatalf("expected copilot login command guidance, got:\n%s", text)
	}
	if !strings.Contains(text, "press Enter, r, or v here to verify auth.getStatus.") {
		t.Fatalf("expected verify key guidance, got:\n%s", text)
	}
}

func TestChatDrawCompactDoesNotHardFail(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Branch:    "main",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(56, 24)
	page.Draw(screen)

	text := dumpScreenText(screen, 56, 24)
	if strings.Contains(text, "Terminal too small") || strings.Contains(text, "Need at least") {
		t.Fatalf("chat draw should render compact layout, got:\n%s", text)
	}
}

func TestChatDrawThinBusyKeepsThinkingIndicator(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Branch:    "main",
		},
	})
	page.busy = true

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(44, 12)
	page.Draw(screen)

	text := dumpScreenText(screen, 44, 12)
	if !strings.Contains(text, "Thinking") {
		t.Fatalf("thin busy chat should render thinking indicator, got:\n%s", text)
	}
}

func TestChatDrawWideLayoutDoesNotRenderSidebarBox(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Branch:    "main",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	const width = 140
	const height = 28
	screen.SetSize(width, height)
	page.Draw(screen)

	if corner, _, _, _ := screen.GetContent(width-1, 0); corner == tcell.RuneURCorner {
		t.Fatalf("unexpected sidebar border corner at right edge")
	}
	if edge, _, _, _ := screen.GetContent(width-1, height/2); edge == tcell.RuneVLine {
		t.Fatalf("unexpected sidebar vertical border at right edge")
	}
}

func TestChatDrawWideLayoutHeaderStillShowsBranch(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-test",
		ShowHeader:     true,
		AuthConfigured: true,
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
			Branch:    "feature/abc",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()

	screen.SetSize(140, 28)
	page.Draw(screen)

	text := dumpScreenText(screen, 140, 28)
	if !strings.Contains(text, "feature/abc") {
		t.Fatalf("wide header should show branch when sidebar is removed, got:\n%s", text)
	}
}
