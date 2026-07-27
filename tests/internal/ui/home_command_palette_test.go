package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func newPaletteHomePage() *HomePage {
	p := NewHomePage(model.EmptyHome())
	p.SetCommandSuggestions([]CommandSuggestion{
		{Command: "/auth", Hint: "Auth status or key setup"},
		{Command: "/header", Hint: "Toggle chat header", QuickTips: []string{"/header toggle", "/header status"}},
		{Command: "/help", Hint: "Show help"},
	})
	return p
}

func TestAcceptCommandPaletteEnter_CompletesSelection(t *testing.T) {
	p := newPaletteHomePage()
	p.SetPrompt("/")

	if handled := p.AcceptCommandPaletteEnter(); !handled {
		t.Fatalf("AcceptCommandPaletteEnter() = false, want true")
	}
	if got := p.PromptValue(); got != "/auth " {
		t.Fatalf("prompt = %q, want %q", got, "/auth ")
	}
}

func TestAcceptCommandPaletteEnter_DoesNotEatExactCommand(t *testing.T) {
	p := newPaletteHomePage()
	p.SetPrompt("/auth")

	if handled := p.AcceptCommandPaletteEnter(); handled {
		t.Fatalf("AcceptCommandPaletteEnter() = true, want false")
	}
	if got := p.PromptValue(); got != "/auth" {
		t.Fatalf("prompt = %q, want %q", got, "/auth")
	}
}

func TestAcceptCommandPaletteEnter_DoesNotEatWhenArgsPresent(t *testing.T) {
	p := newPaletteHomePage()
	p.SetPrompt("/header off")

	if handled := p.AcceptCommandPaletteEnter(); handled {
		t.Fatalf("AcceptCommandPaletteEnter() = true, want false")
	}
	if got := p.PromptValue(); got != "/header off" {
		t.Fatalf("prompt = %q, want %q", got, "/header off")
	}
}

func TestCommandPaletteOptionsTrackExactSubcommandQuery(t *testing.T) {
	p := newPaletteHomePage()
	p.SetCommandSuggestions([]CommandSuggestion{{
		Command:   "/codex",
		Hint:      "Codex commands",
		QuickTips: []string{"/codex status", "/codex fast", "/fast"},
	}})
	p.SetPrompt("/codex s")

	selected, ok := p.selectedCommandSuggestion()
	if !ok {
		t.Fatalf("selectedCommandSuggestion() = false, want true")
	}
	if selected.Command != "/codex" {
		t.Fatalf("selected command = %q, want /codex", selected.Command)
	}
	option, ok := p.selectedCommandPaletteOption()
	if !ok {
		t.Fatalf("selectedCommandPaletteOption() = false, want true")
	}
	if option.Command != "/codex status" {
		t.Fatalf("selected option = %q, want /codex status", option.Command)
	}
	if handled := p.AcceptCommandPaletteEnter(); !handled {
		t.Fatalf("AcceptCommandPaletteEnter() = false, want true")
	}
	if got := p.PromptValue(); got != "/codex status " {
		t.Fatalf("prompt = %q, want %q", got, "/codex status ")
	}
}

func TestCommandPaletteArrowKeysMoveInlineOptions(t *testing.T) {
	p := newPaletteHomePage()
	p.SetCommandSuggestions([]CommandSuggestion{{
		Command:   "/codex",
		Hint:      "Codex commands",
		QuickTips: []string{"/codex status", "/codex fast", "/fast"},
	}})
	p.SetPrompt("/codex")

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	option, ok := p.selectedCommandPaletteOption()
	if !ok {
		t.Fatalf("selectedCommandPaletteOption() = false after right")
	}
	if option.Command != "/codex status" {
		t.Fatalf("selected option after right = %q, want /codex status", option.Command)
	}

	p.HandleKey(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	option, ok = p.selectedCommandPaletteOption()
	if !ok {
		t.Fatalf("selectedCommandPaletteOption() = false after second right")
	}
	if option.Command != "/codex fast" {
		t.Fatalf("selected option after second right = %q, want /codex fast", option.Command)
	}
}

func TestDrawCommandPaletteRegistersOptionMouseTargets(t *testing.T) {
	p := newPaletteHomePage()
	p.SetCommandSuggestions([]CommandSuggestion{{
		Command:   "/codex",
		Hint:      "Codex commands",
		QuickTips: []string{"/codex status", "/codex fast"},
	}})
	p.SetPrompt("/codex")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(80, 24)

	p.drawCommandPalette(screen, Rect{X: 10, Y: 12, W: 50, H: 3}, layoutVariant{}, 2)
	var optionTarget clickTarget
	found := false
	for _, target := range p.commandPaletteTargets {
		if target.Action == "palette-option" && target.Meta == "/codex status" {
			optionTarget = target
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("palette option target for /codex status not found: %#v", p.commandPaletteTargets)
	}

	p.HandleMouse(tcell.NewEventMouse(optionTarget.Rect.X, optionTarget.Rect.Y, tcell.Button1, 0))
	if got := p.PromptValue(); got != "/codex status " {
		t.Fatalf("prompt after option click = %q, want %q", got, "/codex status ")
	}
}

func TestHomeCommandPaletteOffersOnlyWorktreeNewCommands(t *testing.T) {
	home := NewHomePage(model.EmptyHome())
	home.SetCommandSuggestions([]CommandSuggestion{
		{Command: "/worktrees new", Hint: "Create a new session in its own worktree"},
		{Command: "/wt new", Hint: "Create a new worktree session (short alias)"},
	})

	home.SetPrompt("/wt")
	matches := home.commandPaletteMatches()
	if len(matches) != 1 || matches[0].Command != "/wt new" {
		t.Fatalf("worktree matches = %#v, want only /wt new", matches)
	}
	if !home.AcceptCommandPaletteEnter() {
		t.Fatal("expected Enter to complete /wt to /wt new")
	}
	if got := home.PromptValue(); got != "/wt new " {
		t.Fatalf("completed prompt = %q, want /wt new", got)
	}
}
