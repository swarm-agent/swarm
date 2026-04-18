package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestAgentChangePermissionUsesDedicatedModal(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_1",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"create","change":{"operation":"create","target":"agent_profile","after":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read"}},"summary":"proposed new agent demo-agent"}`,
		Status:        "pending",
	})

	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}
	if page.PermissionModalVisible() {
		t.Fatalf("generic permission modal should stay hidden for agent-change permission")
	}
}

func TestAgentChangeModalEscapeDeniesPermission(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_1",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"delete","change":{"operation":"delete","target":"agent_profile","before":{"name":"demo-agent"}},"summary":"proposed delete for agent demo-agent"}`,
		Status:        "pending",
	})
	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}

	if consumed := page.HandleEscape(); !consumed {
		t.Fatalf("HandleEscape() consumed = false, want true")
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	if backend.resolveCalls[0].Action != "deny" {
		t.Fatalf("resolve action = %q, want deny", backend.resolveCalls[0].Action)
	}
}

func TestAgentChangeApprovedArgumentsBuildsConfirmedArguments(t *testing.T) {
	page := NewChatPage(ChatPageOptions{SessionID: "session-1"})
	record := ChatPermissionRecord{
		ID:            "perm_agent_confirm",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"create","approved_arguments":{"action":"create","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read"}},"change":{"operation":"create","after":{"name":"demo-agent"}},"summary":"proposed new agent demo-agent"}`,
	}
	approved := page.agentChangeApprovedArguments(record)
	if strings.TrimSpace(approved) == "" {
		t.Fatalf("expected non-empty approved arguments")
	}
	if !strings.Contains(approved, `"confirm":true`) {
		t.Fatalf("approved arguments missing confirm=true: %s", approved)
	}
	if !strings.Contains(approved, `"agent":"demo-agent"`) {
		t.Fatalf("approved arguments missing agent name: %s", approved)
	}
}

func TestAgentChangeModalApproveUsesApprovedArgumentsNotReason(t *testing.T) {
	backend := &chatPlanModalBackendStub{}
	page := NewChatPage(ChatPageOptions{
		Backend:        backend,
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_approve",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"create","approved_arguments":{"action":"create","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read"}},"change":{"operation":"create","after":{"name":"demo-agent"}},"summary":"proposed new agent demo-agent"}`,
		Status:        "pending",
	})
	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}

	page.resolveAgentChangeModal(true)
	deadline := time.Now().Add(300 * time.Millisecond)
	for len(backend.resolveCalls) == 0 && time.Now().Before(deadline) {
		_ = page.drainPermissionActions()
		time.Sleep(10 * time.Millisecond)
	}
	if len(backend.resolveCalls) == 0 {
		t.Fatalf("expected ResolvePermission call")
	}
	call := backend.resolveCalls[0]
	if call.Action != "approve" {
		t.Fatalf("resolve action = %q, want approve", call.Action)
	}
	if got := strings.TrimSpace(call.Reason); got != "" {
		t.Fatalf("approve reason = %q, want empty", got)
	}
	if !strings.Contains(call.ApprovedArguments, `"confirm":true`) {
		t.Fatalf("approved arguments missing confirm=true: %s", call.ApprovedArguments)
	}
	if !strings.Contains(call.ApprovedArguments, `"agent":"demo-agent"`) {
		t.Fatalf("approved arguments missing agent name: %s", call.ApprovedArguments)
	}
}

func TestAgentChangeModalDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_narrow",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"create","change":{"operation":"create","target":"agent_profile","after":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read"}},"summary":"proposed new agent demo-agent"}`,
		Status:        "pending",
	})
	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	width, height := 44, 14
	screen.SetSize(width, height)
	page.Draw(screen)

	text := dumpScreenText(screen, width, height)
	if !strings.Contains(text, "Review Agent Change") {
		t.Fatalf("expected agent change modal header on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(text, "Enter") {
		t.Fatalf("expected agent change modal controls on narrow screen, got:\n%s", text)
	}
}

func TestAgentChangeModelPickerSupportsProviderSwitching(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		AvailableModels: []ModelsModalEntry{
			{Provider: "codex", Model: "gpt-5.4"},
			{Provider: "codex", Model: "gpt-5.4-mini"},
			{Provider: "google", Model: "gemini-2.0-flash"},
			{Provider: "google", Model: "gemini-2.5-pro"},
		},
	})

	record := ChatPermissionRecord{
		ID:            "perm_agent_model_switch",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"update","agent":"demo-agent","approved_arguments":{"action":"update","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read","provider":"codex","model":"gpt-5.4"}},"change":{"operation":"update","target":"agent_profile","before":{"name":"demo-agent","provider":"codex","model":"gpt-5.4"},"after":{"name":"demo-agent","mode":"subagent","description":"Demo","prompt":"Help with review.","execution_setting":"read","provider":"codex","model":"gpt-5.4"}},"summary":"proposed update for demo-agent"}`,
		Status:        "pending",
	}
	page.upsertPendingPermission(record)
	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))
	if !page.agentChangeModelPickerVisible {
		t.Fatalf("expected model picker to be visible")
	}
	if got := page.agentChangeModelPickerProvider; got != "codex" {
		t.Fatalf("initial provider = %q, want codex", got)
	}
	if providers := page.agentChangeModelPickerProviders(); len(providers) != 2 {
		t.Fatalf("provider count = %d, want 2", len(providers))
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := page.agentChangeModelPickerProvider; got != "google" {
		t.Fatalf("provider after right = %q, want google", got)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if got := page.agentChangeModelPickerProvider; got != "codex" {
		t.Fatalf("provider after left = %q, want codex", got)
	}
	page.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if got := page.agentChangeModelPickerProvider; got != "google" {
		t.Fatalf("provider after second right = %q, want google", got)
	}
	if got := page.agentChangeModelPickerSelected; got != 0 {
		t.Fatalf("selected model index after provider switch = %d, want 0", got)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	options := page.agentChangeModelPickerCurrentOptions()
	if got := options[page.agentChangeModelPickerSelected].Model; got != "gemini-2.5-pro" {
		t.Fatalf("selected model after down = %q, want gemini-2.5-pro", got)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if page.agentChangeModelPickerVisible {
		t.Fatalf("expected model picker to close after selection")
	}
	if got := page.agentChangeOverrideProvider; got != "google" {
		t.Fatalf("override provider = %q, want google", got)
	}
	if got := page.agentChangeOverrideModel; got != "gemini-2.5-pro" {
		t.Fatalf("override model = %q, want gemini-2.5-pro", got)
	}

	approved := page.agentChangeApprovedArguments(record)
	if !strings.Contains(approved, `"provider":"google"`) {
		t.Fatalf("approved arguments missing provider override: %s", approved)
	}
	if !strings.Contains(approved, `"model":"gemini-2.5-pro"`) {
		t.Fatalf("approved arguments missing model override: %s", approved)
	}
	if !strings.Contains(approved, `"thinking":"medium"`) {
		t.Fatalf("approved arguments missing default thinking override: %s", approved)
	}
}

func TestAgentChangeModalCyclesThinkingForReasoningModel(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "",
		AvailableModels: []ModelsModalEntry{
			{Provider: "codex", Model: "gpt-5.4", Reasoning: true},
			{Provider: "google", Model: "gemini-2.5-pro", Reasoning: true},
		},
	})

	record := ChatPermissionRecord{
		ID:            "perm_agent_thinking_cycle",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"update","agent":"demo-agent","approved_arguments":{"action":"update","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"change":{"operation":"update","target":"agent_profile","before":{"name":"demo-agent","provider":"codex","model":"gpt-5.4"},"after":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"summary":"proposed update for demo-agent"}`,
		Status:        "pending",
	}
	page.upsertPendingPermission(record)
	if !page.AgentChangeModalVisible() {
		t.Fatalf("expected agent change modal to be visible")
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 't', tcell.ModNone))
	if got := page.agentChangeOverrideThinking; got != "medium" {
		t.Fatalf("first thinking cycle override = %q, want medium", got)
	}
	if got := page.agentChangeSelectedThinking(); got != "medium" {
		t.Fatalf("first thinking cycle selected thinking = %q, want medium", got)
	}

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 't', tcell.ModNone))
	if got := page.agentChangeOverrideThinking; got != "high" {
		t.Fatalf("second thinking cycle override = %q, want high", got)
	}

	approved := page.agentChangeApprovedArguments(record)
	if !strings.Contains(approved, `"thinking":"high"`) {
		t.Fatalf("approved arguments missing cycled thinking override: %s", approved)
	}
}

func TestAgentChangeModalThinkingFallsBackOffForNonReasoningModel(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		ThinkingLevel:  "high",
		AvailableModels: []ModelsModalEntry{
			{Provider: "codex", Model: "gpt-5.4", Reasoning: false},
		},
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_thinking_non_reasoning",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"update","agent":"demo-agent","approved_arguments":{"action":"update","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"change":{"operation":"update","target":"agent_profile","before":{"name":"demo-agent","provider":"codex","model":"gpt-5.4"},"after":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"summary":"proposed update for demo-agent"}`,
		Status:        "pending",
	})

	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 't', tcell.ModNone))
	if got := page.agentChangeOverrideThinking; got != "off" {
		t.Fatalf("non-reasoning thinking override = %q, want off", got)
	}
}

func TestAgentChangeModelPickerDrawShowsProviders(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
		ModelProvider:  "codex",
		ModelName:      "gpt-5.4",
		AvailableModels: []ModelsModalEntry{
			{Provider: "codex", Model: "gpt-5.4"},
			{Provider: "codex", Model: "gpt-5.4-mini"},
			{Provider: "google", Model: "gemini-2.0-flash"},
			{Provider: "google", Model: "gemini-2.5-pro"},
		},
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_agent_picker_draw",
		SessionID:     "session-1",
		ToolName:      "manage-agent",
		Requirement:   "agent_change",
		ToolArguments: `{"action":"update","agent":"demo-agent","approved_arguments":{"action":"update","agent":"demo-agent","content":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"change":{"operation":"update","target":"agent_profile","before":{"name":"demo-agent","provider":"codex","model":"gpt-5.4"},"after":{"name":"demo-agent","mode":"subagent","provider":"codex","model":"gpt-5.4"}},"summary":"proposed update for demo-agent"}`,
		Status:        "pending",
	})
	page.HandleKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModNone))

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	width, height := 110, 28
	screen.SetSize(width, height)
	page.Draw(screen)

	text := dumpScreenText(screen, width, height)
	if !strings.Contains(text, "Providers") {
		t.Fatalf("expected provider pane in picker, got:\n%s", text)
	}
	if !strings.Contains(text, "> codex") {
		t.Fatalf("expected active provider row in picker, got:\n%s", text)
	}
	if !strings.Contains(text, "google") {
		t.Fatalf("expected secondary provider to be visible in picker, got:\n%s", text)
	}
}
