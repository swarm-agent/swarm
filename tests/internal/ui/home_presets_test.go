package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeDrawPresetsRowCentersWhenRequested(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5",
			"Thinking: high",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(80, 3)

	rect := Rect{X: 0, Y: 1, W: 80, H: 1}
	page.drawPresetsRow(screen, rect, true)

	line := dumpScreenText(screen, 80, 3)
	lines := strings.Split(line, "\n")
	if len(lines) < 2 {
		t.Fatalf("screen dump too short")
	}
	row := lines[1]
	first := strings.Index(row, "[")
	if first < 0 {
		t.Fatalf("presets row not rendered: %q", row)
	}
	// For centered row on width 80, first chip should not be hard-left.
	if first < 8 {
		t.Fatalf("presets row appears left-aligned, first chip index=%d row=%q", first, row)
	}
}

func TestHomeDrawPresetsRowDoesNotHighlightFirstChip(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5-codex",
			"Thinking: high",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 3)

	rect := Rect{X: 0, Y: 1, W: 120, H: 1}
	page.drawPresetsRow(screen, rect, false)

	chipOne := "[Agent: swarm]"
	chipTwoX := len(chipOne) + 2

	_, _, styleOne, _ := screen.GetContent(0, 1)
	_, _, styleTwo, _ := screen.GetContent(chipTwoX, 1)

	fgOne, bgOne, attrOne := styleOne.Decompose()
	fgTwo, bgTwo, attrTwo := styleTwo.Decompose()
	if fgOne != fgTwo || bgOne != bgTwo || attrOne != attrTwo {
		t.Fatalf("first chip style differs: got=(%v,%v,%v) second=(%v,%v,%v)", fgOne, bgOne, attrOne, fgTwo, bgTwo, attrTwo)
	}
	if attrOne&tcell.AttrBold != 0 {
		t.Fatalf("first chip should not be bold, attrs=%v", attrOne)
	}
	want := styleForCurrentCellBackground(page.theme.TextMuted)
	got := styleForCurrentCellBackground(styleOne)
	if !stylesEqual(got, want) {
		t.Fatalf("first chip style = %v (normalized %v), want muted style %v", styleOne, got, want)
	}
}

func TestHomeDrawPresetsRowUsesCompactChatLikeChipsOnNarrowWidth(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5-codex",
			"Thinking: high",
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(56, 3)

	rect := Rect{X: 0, Y: 1, W: 56, H: 1}
	page.drawPresetsRow(screen, rect, false)

	dump := dumpScreenText(screen, 56, 3)
	lines := strings.Split(strings.TrimSuffix(dump, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("screen dump too short")
	}
	row := lines[1]
	if !strings.Contains(row, "[a:swarm]") {
		t.Fatalf("compact row missing agent chip: %q", row)
	}
	if !strings.Contains(row, "[m:gpt-5-codex]") {
		t.Fatalf("compact row missing model chip: %q", row)
	}
	if !strings.Contains(row, "[t:high]") {
		t.Fatalf("compact row missing thinking chip: %q", row)
	}
}

func TestHomePresetChipsUseHoverStyleWhenHovered(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		QuickActions: []string{
			"Agent: swarm",
			"Model: gpt-5-codex",
			"Thinking: high",
		},
	})
	page.hoverTopAction = "open-models-modal"

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 3)

	rect := Rect{X: 0, Y: 1, W: 120, H: 1}
	page.drawPresetsRow(screen, rect, false)

	chipOne := "[Agent: swarm]"
	chipTwoX := len(chipOne) + 2

	_, _, styleOne, _ := screen.GetContent(0, 1)
	_, _, styleTwo, _ := screen.GetContent(chipTwoX, 1)

	_, _, attrOne := styleOne.Decompose()
	_, _, attrTwo := styleTwo.Decompose()
	if attrOne&tcell.AttrBold != 0 || attrOne&tcell.AttrUnderline != 0 {
		t.Fatalf("non-hovered chip should remain muted, attrs=%v", attrOne)
	}
	if attrTwo&tcell.AttrBold == 0 || attrTwo&tcell.AttrUnderline == 0 {
		t.Fatalf("hovered chip should be bold+underline, attrs=%v", attrTwo)
	}
}

func TestHomePresetChipAction(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "agent", input: "Agent: swarm", want: "open-agents-modal"},
		{name: "model", input: "Model: gpt-5-codex", want: "open-models-modal"},
		{name: "thinking", input: "Thinking: high", want: "cycle-thinking"},
		{name: "unknown", input: "Auth: missing", want: ""},
		{name: "empty", input: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := homePresetChipAction(tc.input); got != tc.want {
				t.Fatalf("homePresetChipAction(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
