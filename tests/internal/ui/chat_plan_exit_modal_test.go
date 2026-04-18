package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

type chatPlanModalBackendStub struct {
	setModes      []string
	resolveCalls  []chatResolveCall
	resolveStatus string
	sessionMode   string
}

type chatResolveCall struct {
	PermissionID      string
	Action            string
	Reason            string
	ApprovedArguments string
}

func (s *chatPlanModalBackendStub) LoadMessages(context.Context, string, uint64, int) ([]ChatMessageRecord, error) {
	return nil, nil
}

func (s *chatPlanModalBackendStub) GetSessionUsageSummary(context.Context, string) (*ChatUsageSummary, error) {
	return nil, nil
}

func (s *chatPlanModalBackendStub) GetSessionMode(context.Context, string) (string, error) {
	mode := strings.TrimSpace(s.sessionMode)
	if mode == "" {
		mode = "plan"
	}
	return mode, nil
}

func (s *chatPlanModalBackendStub) GetSessionPreference(context.Context, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatPlanModalBackendStub) SetSessionPreference(context.Context, string, string, string, string, string, string) (string, string, string, string, string, int, error) {
	return "", "", "", "", "", 0, nil
}

func (s *chatPlanModalBackendStub) GetActiveSessionPlan(context.Context, string) (ChatSessionPlan, bool, error) {
	return ChatSessionPlan{}, false, nil
}

func (s *chatPlanModalBackendStub) SaveSessionPlan(_ context.Context, _ string, plan ChatSessionPlan) (ChatSessionPlan, error) {
	return plan, nil
}

func (s *chatPlanModalBackendStub) ListPermissions(context.Context, string, int) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatPlanModalBackendStub) ListPendingPermissions(context.Context, string, int) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatPlanModalBackendStub) ResolvePermission(_ context.Context, _ string, permissionID, action, reason string) (ChatPermissionRecord, error) {
	return s.ResolvePermissionWithArguments(context.Background(), "", permissionID, action, reason, "")
}

func (s *chatPlanModalBackendStub) ResolvePermissionWithArguments(_ context.Context, _ string, permissionID, action, reason, approvedArguments string) (ChatPermissionRecord, error) {
	s.resolveCalls = append(s.resolveCalls, chatResolveCall{
		PermissionID:      permissionID,
		Action:            action,
		Reason:            reason,
		ApprovedArguments: approvedArguments,
	})
	if strings.EqualFold(strings.TrimSpace(action), "approve") && strings.Contains(strings.ToLower(strings.TrimSpace(permissionID)), "exit") {
		s.sessionMode = "auto"
	}
	status := strings.TrimSpace(s.resolveStatus)
	if status == "" {
		status = "approved"
	}
	return ChatPermissionRecord{
		ID:     permissionID,
		Status: status,
	}, nil
}

func (s *chatPlanModalBackendStub) ResolveAllPermissions(context.Context, string, string, string) ([]ChatPermissionRecord, error) {
	return nil, nil
}

func (s *chatPlanModalBackendStub) GetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatPlanModalBackendStub) AddPermissionRule(context.Context, ChatPermissionRule) (ChatPermissionRule, error) {
	return ChatPermissionRule{}, nil
}

func (s *chatPlanModalBackendStub) RemovePermissionRule(context.Context, string) (bool, error) {
	return false, nil
}

func (s *chatPlanModalBackendStub) ResetPermissionPolicy(context.Context) (ChatPermissionPolicy, error) {
	return ChatPermissionPolicy{}, nil
}

func (s *chatPlanModalBackendStub) ExplainPermission(context.Context, string, string, string) (ChatPermissionExplain, error) {
	return ChatPermissionExplain{}, nil
}

func (s *chatPlanModalBackendStub) StopRun(context.Context, string, string) error {
	return nil
}

func (s *chatPlanModalBackendStub) RunTurn(context.Context, string, ChatRunRequest) (ChatRunResponse, error) {
	return ChatRunResponse{}, nil
}

func (s *chatPlanModalBackendStub) RunTurnStream(context.Context, string, ChatRunRequest, func(ChatRunStreamEvent)) (ChatRunResponse, error) {
	return ChatRunResponse{}, nil
}

func TestChatPlanExitModalEscapeClosesModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenExitPlanModeModal("Exit Plan", "Line 1\nLine 2") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}
	if !page.ExitPlanModalVisible() {
		t.Fatalf("ExitPlanModalVisible() = false, want true")
	}

	if consumed := page.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true")
	}
	if page.ExitPlanModalVisible() {
		t.Fatalf("ExitPlanModalVisible() = true, want false after escape")
	}
}

func TestChatPlanExitModalScrollsWithKeys(t *testing.T) {
	body := strings.Repeat("This is a long handoff line for scrolling.\n", 60)
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenExitPlanModeModal("Exit Plan", body) {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	page.HandleKey(tcell.NewEventKey(tcell.KeyPgDn, 0, tcell.ModNone))
	if page.planExitScroll <= 0 {
		t.Fatalf("planExitScroll = %d, want > 0 after scrolling", page.planExitScroll)
	}
}

func TestChatPlanExitModalConfirmQueuesAutoMode(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	if !page.OpenExitPlanModeModal("Exit Plan", "ready") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.ExitPlanModalVisible() {
		t.Fatalf("ExitPlanModalVisible() = true, want false after confirm")
	}

	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.setModes) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.setModes) == 0 {
		t.Fatalf("expected SetSessionMode call, got none")
	}
	if backend.setModes[0] != "auto" {
		t.Fatalf("SetSessionMode mode = %q, want auto", backend.setModes[0])
	}
}

func TestChatPlanExitModalLinesRenderMarkdownBody(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	body := "## List Example\n- Item one\n- Item two\n\nThis is **bold** text."
	if !page.OpenExitPlanModeModal("Exit Plan", body) {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	lines := page.planExitModalLines(80)
	if len(lines) == 0 {
		t.Fatalf("planExitModalLines() empty, want content")
	}

	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		joined = append(joined, chatRenderLineText(line))
	}
	text := strings.Join(joined, "\n")

	if strings.Contains(text, "## List Example") {
		t.Fatalf("heading markdown marker leaked in modal body: %q", text)
	}
	if strings.Contains(text, "**bold**") {
		t.Fatalf("inline markdown marker leaked in modal body: %q", text)
	}
	if !strings.Contains(text, "List Example") {
		t.Fatalf("rendered heading text missing: %q", text)
	}
	if !strings.Contains(text, "• Item one") || !strings.Contains(text, "• Item two") {
		t.Fatalf("rendered bullet lines missing: %q", text)
	}

	handoffHeader := lineIndexContaining(joined, "Handoff notes:")
	if handoffHeader < 0 {
		t.Fatalf("handoff header missing: %q", text)
	}
	listHeading := lineIndexContaining(joined, "List Example")
	if listHeading < 0 {
		t.Fatalf("markdown heading missing: %q", text)
	}
	if listHeading-handoffHeader < 2 {
		t.Fatalf("expected blank spacer between handoff header and markdown heading; lines=%v", joined)
	}
}

func TestChatPlanExitModalLinesRenderNestedMarkdownListBody(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	body := "# Plan\n1. Keep the existing first-add behavior that seeds the global model and built-in utility subagents.\n2. Remove the post-onboarding fallback that currently reassigns subagents when their provider is blank.\n   - Result: adding or activating credentials after the first provider will not mutate agent settings.\n   - Add/adjust coverage in tests/swarmd/internal/api/auth_defaults_test.go to lock in the rule that later auth-key additions do not reapply agent defaults or override user-managed agent settings.\n   - Leave the UI behavior unchanged except for the backend no longer returning applied auto-defaults after the first onboarding, which prevents the misleading \"defaults applied\" messaging on later key additions."
	if !page.OpenExitPlanModeModal("Exit Plan", body) {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	lines := page.planExitModalLines(72)
	if len(lines) == 0 {
		t.Fatalf("planExitModalLines() empty, want content")
	}

	joined := make([]string, 0, len(lines))
	for _, line := range lines {
		joined = append(joined, chatRenderLineText(line))
	}
	text := strings.Join(joined, "\n")
	if !strings.Contains(text, "1. Keep the existing first-add behavior") {
		t.Fatalf("missing ordered-list first item in modal body: %q", text)
	}
	if !strings.Contains(text, "2. Remove the post-onboarding fallback") {
		t.Fatalf("missing ordered-list second item in modal body: %q", text)
	}
	if !strings.Contains(text, "   • Result: adding or activating") {
		t.Fatalf("missing nested bullet line in modal body: %q", text)
	}
	if !strings.Contains(text, "     credentials after the first") {
		t.Fatalf("missing nested bullet continuation indent in modal body: %q", text)
	}
	if !strings.Contains(text, "   • Add/adjust coverage in") {
		t.Fatalf("missing second nested bullet line in modal body: %q", text)
	}
	if !strings.Contains(text, "     tests/swarmd/internal/api/auth_defaults_test.go") {
		t.Fatalf("missing second nested bullet continuation indent in modal body: %q", text)
	}
}

func TestChatPlanExitPermissionModalConfirmResolvesPermission(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		Meta: ChatSessionMeta{
			Workspace: "workspace-1",
			Path:      "/tmp/workspace/project",
		},
	})

	record := ChatPermissionRecord{
		ID:            "perm_exit_1",
		SessionID:     "session-1",
		ToolName:      "exit_plan_mode",
		ToolArguments: `{"title":"Exit Plan","plan":"# Plan\n\n- [ ] ship","plan_id":"plan_123"}`,
		Status:        "pending",
	}
	page.upsertPendingPermission(record)
	if !page.ExitPlanModalVisible() {
		t.Fatalf("expected exit plan permission modal to open")
	}

	for _, r := range "Return this note" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		if len(backend.resolveCalls) > 0 && page.SessionMode() == "auto" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	if backend.resolveCalls[0].PermissionID != "perm_exit_1" {
		t.Fatalf("expected permission id perm_exit_1, got %q", backend.resolveCalls[0].PermissionID)
	}
	if backend.resolveCalls[0].Action != "approve" {
		t.Fatalf("expected approve action, got %q", backend.resolveCalls[0].Action)
	}
	if backend.resolveCalls[0].Reason != "Return this note" {
		t.Fatalf("expected note to be returned, got %q", backend.resolveCalls[0].Reason)
	}
	if got := page.SessionMode(); got != "auto" {
		t.Fatalf("session mode = %q, want auto after exit-plan approval", got)
	}
	if line := page.footerInfoLine(1000); !strings.Contains(line, "mode auto") {
		t.Fatalf("footerInfoLine() = %q, want mode auto immediately after exit-plan approval", line)
	}
}

func TestChatPlanExitModalDrawHeaderIncludesPlanTitle(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Release Handoff", "Ship when checks pass.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 32)
	if !strings.Contains(text, "Exit Plan Mode: Release Handoff") {
		t.Fatalf("expected plan title in exit-plan modal header, got:\n%s", text)
	}
}

func TestChatPlanExitModalDrawShowsHelpAndScrollLabel(t *testing.T) {
	body := strings.Repeat("Long handoff paragraph for scrolling.\n", 120)
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Exit Plan", body) {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 28)
	page.Draw(screen)

	text := dumpScreenText(screen, 100, 28)
	if !strings.Contains(text, "↑/↓ scroll") {
		t.Fatalf("expected help text in exit-plan modal footer, got:\n%s", text)
	}
	if !strings.Contains(text, "scroll 1/") {
		t.Fatalf("expected scroll label in exit-plan modal footer, got:\n%s", text)
	}
}

func TestExitPlanPermissionUsesDedicatedModalOnly(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_exit_1",
		SessionID:     "session-1",
		ToolName:      "exit_plan_mode",
		ToolArguments: `{"title":"Exit Plan","plan":"handoff","plan_id":"plan_123"}`,
		Status:        "pending",
	})

	if !page.ExitPlanModalVisible() {
		t.Fatalf("expected dedicated exit-plan modal to be visible")
	}
	if page.PermissionModalVisible() {
		t.Fatalf("generic permission modal should stay hidden for exit-plan permission")
	}
}

func TestResolveAllSkipsExitPlanPermission(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.pendingPerms = []ChatPermissionRecord{
		{
			ID:            "perm_exit_1",
			SessionID:     "session-1",
			ToolName:      "exit_plan_mode",
			ToolArguments: `{"title":"Exit Plan","plan":"handoff","plan_id":"plan_123"}`,
			Status:        "pending",
		},
		{
			ID:            "perm_write_1",
			SessionID:     "session-1",
			ToolName:      "write",
			ToolArguments: `{"path":"README.md","content":"x"}`,
			Status:        "pending",
		},
	}
	page.syncSpecialPermissionModals()

	page.queueResolveAll("approve", "")
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		if len(backend.resolveCalls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) != 1 {
		t.Fatalf("resolve call count = %d, want 1", len(backend.resolveCalls))
	}
	if backend.resolveCalls[0].PermissionID != "perm_write_1" {
		t.Fatalf("resolved permission = %q, want perm_write_1", backend.resolveCalls[0].PermissionID)
	}
	if !page.ExitPlanModalVisible() {
		t.Fatalf("exit-plan modal should remain visible while exit permission is still pending")
	}
}

func TestGenericPermissionModalHiddenWhileExitPlanModalVisible(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.pendingPerms = []ChatPermissionRecord{
		{
			ID:            "perm_exit_1",
			SessionID:     "session-1",
			ToolName:      "exit_plan_mode",
			ToolArguments: `{"title":"Exit Plan","plan":"handoff","plan_id":"plan_123"}`,
			Status:        "pending",
		},
		{
			ID:            "perm_ask_1",
			SessionID:     "session-1",
			ToolName:      "ask-user",
			ToolArguments: `{"question":"Pick one","options":["A","B"]}`,
			Status:        "pending",
		},
	}
	page.syncSpecialPermissionModals()

	if !page.ExitPlanModalVisible() {
		t.Fatalf("expected exit-plan modal to be visible")
	}
	if page.PermissionModalVisible() {
		t.Fatalf("generic permission modal should be hidden while exit-plan modal is open")
	}
}

func TestChatPlanExitModalAcceptsTypedNoteOnApprove(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	record := ChatPermissionRecord{
		ID:            "perm_exit_note",
		SessionID:     "session-1",
		ToolName:      "exit_plan_mode",
		ToolArguments: `{"title":"Exit Plan","plan":"handoff","plan_id":"plan_123"}`,
		Status:        "pending",
	}
	page.upsertPendingPermission(record)
	if !page.ExitPlanModalVisible() {
		t.Fatalf("expected exit plan permission modal to open")
	}

	for _, r := range "Need docs updates first" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))

	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	if got := backend.resolveCalls[0].Reason; got != "Need docs updates first" {
		t.Fatalf("resolve reason = %q, want typed note", got)
	}
}

func TestChatPlanExitModalEscapeSendsTypedNoteOnDeny(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	record := ChatPermissionRecord{
		ID:            "perm_exit_deny_note",
		SessionID:     "session-1",
		ToolName:      "exit_plan_mode",
		ToolArguments: `{"title":"Exit Plan","plan":"handoff","plan_id":"plan_123"}`,
		Status:        "pending",
	}
	page.upsertPendingPermission(record)
	if !page.ExitPlanModalVisible() {
		t.Fatalf("expected exit plan permission modal to open")
	}

	for _, r := range "Not ready yet" {
		page.HandleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))

	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	if got := backend.resolveCalls[0].Action; got != "deny" {
		t.Fatalf("resolve action = %q, want deny", got)
	}
	if got := backend.resolveCalls[0].Reason; got != "Not ready yet" {
		t.Fatalf("resolve reason = %q, want typed note", got)
	}
}

func TestChatPlanExitModalDrawShowsInputLabel(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Exit Plan", "Ship when checks pass.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 32)
	if !strings.Contains(text, "Message to agent (optional):") {
		t.Fatalf("expected input label in exit-plan modal, got:\n%s", text)
	}
}

func TestChatPlanExitModalDrawUsesUnboxedInputArea(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Exit Plan", "Ship when checks pass.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 32)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	labelY := lineIndexContaining(lines, "Message to agent (optional):")
	if labelY < 0 {
		t.Fatalf("input label missing in exit-plan modal render:\n%s", text)
	}
	labelX := strings.Index(lines[labelY], "Message to agent (optional):")
	if labelX < 0 {
		t.Fatalf("input label x position not found")
	}
	leftBorderX := labelX
	rightBorderX := minInt(119, labelX+80)
	for y := labelY + 1; y <= labelY+chatPlanExitInputMaxLines; y++ {
		if y >= len(lines) {
			break
		}
		for _, x := range []int{leftBorderX, rightBorderX} {
			r, _, _, _ := screen.GetContent(x, y)
			if r == tcell.RuneVLine || r == tcell.RuneULCorner || r == tcell.RuneURCorner || r == tcell.RuneLLCorner || r == tcell.RuneLRCorner {
				t.Fatalf("unexpected boxed input border rune %q at (%d,%d)", r, x, y)
			}
		}
	}
}

func TestChatPlanExitModalDrawWrapsLongTypedNote(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Exit Plan", "Ship when checks pass.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}
	page.planExitInput = "NOTE-HEAD-123 " + strings.Repeat("x", 140) + " NOTE-TAIL-789"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(90, 30)
	page.Draw(screen)

	text := dumpScreenText(screen, 90, 30)
	if !strings.Contains(text, "NOTE-HEAD-123") {
		t.Fatalf("expected beginning of wrapped note in modal input, got:\n%s", text)
	}
	if !strings.Contains(text, "OTE-TAIL-789") {
		t.Fatalf("expected wrapped tail token in modal input, got:\n%s", text)
	}
}

func TestChatPlanExitModalUsesSinglePanelBackground(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Release Handoff", "Use this plan.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(110, 30)
	page.Draw(screen)

	text := dumpScreenText(screen, 110, 30)
	lineIndex := lineIndexContaining(strings.Split(strings.TrimSuffix(text, "\n"), "\n"), "Exit Plan Mode")
	if lineIndex < 0 {
		t.Fatalf("exit-plan modal header not found in render:\n%s", text)
	}
	headerX := strings.Index(strings.Split(strings.TrimSuffix(text, "\n"), "\n")[lineIndex], "Exit Plan Mode")
	if headerX < 0 {
		t.Fatalf("header x not found in line")
	}
	y := lineIndex

	_, modalBG, _ := pStyleAt(screen, headerX, y).Decompose()
	if modalBG == tcell.ColorDefault {
		t.Fatalf("modal background should be explicit, got default")
	}
	for i, r := range "Exit Plan Mode" {
		if r == ' ' {
			continue
		}
		style := pStyleAt(screen, headerX+i, y)
		_, bg, _ := style.Decompose()
		if bg != modalBG {
			t.Fatalf("header rune %q at %d has bg %v, want %v", r, i, bg, modalBG)
		}
	}
}

func pStyleAt(screen tcell.Screen, x, y int) tcell.Style {
	_, _, style, _ := screen.GetContent(x, y)
	return style
}

func TestPlanExitModalLinesUseCurrentCellBackground(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})
	if !page.OpenExitPlanModeModal("Release", "Use the **latest** plan in ./docs.") {
		t.Fatalf("OpenExitPlanModeModal() = false, want true")
	}

	lines := page.planExitModalLines(80)
	if len(lines) == 0 {
		t.Fatalf("planExitModalLines returned no lines")
	}
	for i, line := range lines {
		if len(line.Spans) == 0 {
			if !stylesEqual(styleForCurrentCellBackground(line.Style), line.Style) {
				t.Fatalf("line %d style should use current-cell background", i)
			}
			continue
		}
		for j, span := range line.Spans {
			if !stylesEqual(styleForCurrentCellBackground(span.Style), span.Style) {
				t.Fatalf("line %d span %d style should use current-cell background", i, j)
			}
		}
	}
}

func TestPermissionComposerDrawsPromptPrefixAboveActions(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "auto",
	})
	page.pendingPerms = []ChatPermissionRecord{{
		ID:            "perm_bash_1",
		SessionID:     "session-1",
		ToolName:      "bash",
		ToolArguments: `{"command":"git status --short --branch && git log --oneline -1"}`,
		Requirement:   "permission",
		Status:        "pending",
	}}
	page.permSelected = 0
	page.permInput = "looks good"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 32)
	page.Draw(screen)

	text := dumpScreenText(screen, 120, 32)
	if !strings.Contains(text, "› looks good") {
		t.Fatalf("expected permission input prompt prefix in render, got:\n%s", text)
	}
	approveY := lineIndexContaining(strings.Split(strings.TrimSuffix(text, "\n"), "\n"), "[Enter Approve]")
	if approveY < 0 {
		t.Fatalf("approve action row missing in render:\n%s", text)
	}
	prefixY := lineIndexContaining(strings.Split(strings.TrimSuffix(text, "\n"), "\n"), "› looks good")
	if prefixY < 0 {
		t.Fatalf("prefixed permission input line missing in render:\n%s", text)
	}
	if prefixY >= approveY {
		t.Fatalf("permission input line should render above action row: prefixY=%d approveY=%d\n%s", prefixY, approveY, text)
	}
}

func TestExitPlanPermissionPayloadExplainsPlanContinues(t *testing.T) {
	title, body, planID := exitPlanPermissionPayload(ChatPermissionRecord{ToolArguments: `{"title":"Release Handoff","plan":"Ship it","plan_id":"plan_123"}`})
	if title != "Release Handoff" {
		t.Fatalf("title = %q", title)
	}
	if planID != "plan_123" {
		t.Fatalf("planID = %q", planID)
	}
	if !strings.Contains(body, "same active plan/checklist") || !strings.Contains(body, "plan_manage can still update it later") {
		t.Fatalf("body = %q", body)
	}
}
