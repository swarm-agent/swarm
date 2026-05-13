package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestDrawCommandSuggestionRowShowsInlineHintWhenEnabled(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(80, 4)

	drawCommandSuggestionRow(screen, 0, 0, 50, tcell.StyleDefault, tcell.StyleDefault, "› ", CommandSuggestion{
		Command:    "/copy 1",
		Hint:       "restart display color service with a very long hint",
		InlineHint: true,
	})

	text := dumpCommandPaletteTestScreen(screen, 50, 1)
	if !strings.Contains(text, "/copy 1  restart display") {
		t.Fatalf("expected inline hint preview, got %q", text)
	}
	if !strings.Contains(text, "...") {
		t.Fatalf("expected inline hint to truncate with ellipsis, got %q", text)
	}
}

func TestDrawCommandSuggestionRowKeepsRegularHintOutOfRow(t *testing.T) {
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(80, 4)

	drawCommandSuggestionRow(screen, 0, 0, 50, tcell.StyleDefault, tcell.StyleDefault, "› ", CommandSuggestion{
		Command: "/agents",
		Hint:    "Open agents manager modal",
	})

	text := dumpCommandPaletteTestScreen(screen, 50, 1)
	if strings.Contains(text, "Open agents") {
		t.Fatalf("expected regular hint to stay out of row, got %q", text)
	}
	if !strings.Contains(text, "/agents") {
		t.Fatalf("expected command row, got %q", text)
	}
}

func dumpCommandPaletteTestScreen(screen tcell.Screen, width, height int) string {
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
