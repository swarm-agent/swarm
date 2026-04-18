package app

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestHandleGlobalKey_AgentsShortcutOpensAgentsModal(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlA, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.home.AgentsModalVisible() {
		t.Fatalf("AgentsModalVisible() = false, want true")
	}
}

func TestHandleGlobalKey_ThinkingShortcutCyclesWithoutOpeningModal(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.homeModel.ModelProvider = "google"

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlT, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.home.ModelsModalVisible() {
		t.Fatalf("ModelsModalVisible() = true, want false")
	}
	if a.home.Status() != "model API is unavailable" {
		t.Fatalf("status = %q, want %q", a.home.Status(), "model API is unavailable")
	}
}

func TestHandleGlobalKey_AgentsShortcutFromChatKeepsChatRoute(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlA, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat == nil, want active chat")
	}
	if !a.home.AgentsModalVisible() {
		t.Fatalf("AgentsModalVisible() = false, want true")
	}
}

func TestHandleGlobalKey_ModelsShortcutFromChatKeepsChatRoute(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModAlt)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat == nil, want active chat")
	}
	if !a.home.ModelsModalVisible() {
		t.Fatalf("ModelsModalVisible() = false, want true")
	}
}

func TestHandleGlobalKey_ModelsShortcutCtrlRuneMOpensModal(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyRune, 'm', tcell.ModCtrl)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.home.ModelsModalVisible() {
		t.Fatalf("ModelsModalVisible() = false, want true")
	}
}

func TestHandleGlobalKey_CtrlMWithoutCtrlModifierDoesNotOpenModelsModal(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlM, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.home.ModelsModalVisible() {
		t.Fatalf("ModelsModalVisible() = true, want false")
	}
}

func TestHandleGlobalKey_HomeShortcutFromChatRoutesHome(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlB, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.route != "home" {
		t.Fatalf("route = %q, want home", a.route)
	}
	if a.chat != nil {
		t.Fatalf("chat != nil, want nil")
	}
}

func TestHandleGlobalKey_CtrlCRequestsGracefulQuit(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true")
	}
}

func TestHandleGlobalKey_CtrlCInHomeWithPromptClearsBeforeQuit(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}
	a.home.SetPrompt("hello")

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if got := a.home.PromptValue(); got != "" {
		t.Fatalf("home prompt = %q, want cleared prompt", got)
	}
	if a.quitRequested {
		t.Fatalf("quitRequested = true, want false while clearing non-empty home prompt")
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true once home prompt is empty")
	}
}

func TestHandleGlobalKey_CtrlCInChatWithInputClearsBeforeQuit(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}
	a.chat.SetInput("hello")

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if got := a.chat.InputValue(); got != "" {
		t.Fatalf("chat input = %q, want cleared input", got)
	}
	if a.quitRequested {
		t.Fatalf("quitRequested = true, want false while clearing non-empty chat input")
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true once chat input is empty")
	}
}

func TestHandleGlobalKey_CtrlCInChatScrollbackJumpsToBottomThenQuits(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	for i := 0; i < 16; i++ {
		a.chat.AppendSystemMessage("scrollback line")
	}
	a.chat.HandleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.quitRequested {
		t.Fatalf("quitRequested = true, want false on first Ctrl+C while scrolled up")
	}
	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if !a.quitRequested {
		t.Fatalf("quitRequested = false, want true on second Ctrl+C from bottom")
	}
}

func TestHandleGlobalKey_RuneQDoesNotQuit(t *testing.T) {
	a := &App{
		home:   ui.NewHomePage(model.EmptyHome()),
		route:  "home",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
}

func TestHandleGlobalKey_EscapeFromChatRequiresDoublePress(t *testing.T) {
	a := &App{
		home: ui.NewHomePage(model.EmptyHome()),
		chat: ui.NewChatPage(ui.ChatPageOptions{
			SessionID:      "session-1",
			SessionTitle:   "Session",
			ShowHeader:     true,
			AuthConfigured: true,
		}),
		route:  "chat",
		config: defaultAppConfig(),
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.route != "chat" {
		t.Fatalf("route = %q, want chat after first Esc", a.route)
	}
	if a.chat == nil {
		t.Fatalf("chat = nil, want active chat after first Esc")
	}

	if done := a.handleGlobalKey(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); done {
		t.Fatalf("handleGlobalKey() done = true, want false")
	}
	if a.route != "home" {
		t.Fatalf("route = %q, want home after second Esc", a.route)
	}
	if a.chat != nil {
		t.Fatalf("chat != nil, want nil after second Esc")
	}
}
