package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/model"
)

func TestDrawThemeModal_SyntaxPreviewUsesTokenStyles(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetTheme(NordTheme())
	p.SetThemeModalData([]ThemeModalEntry{{ID: "nord", Name: "Nord"}}, "nord")
	p.ShowThemeModal("nord")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(90, 28)

	p.drawThemeModal(screen)

	previewText := string([]rune(themeModalSyntaxPreviewCode))
	found, x0, y := findScreenLine(screen, 90, 28, previewText)
	if !found {
		t.Fatalf("preview line not found: %q", previewText)
	}

	assertTokenStyle := func(token string, want tcell.Style) {
		t.Helper()
		offset := strings.Index(previewText, token)
		if offset < 0 {
			t.Fatalf("token %q not found in preview text %q", token, previewText)
		}
		_, _, got, _ := screen.GetContent(x0+offset, y)
		if !stylesEqual(got, want) {
			t.Fatalf("token %q style mismatch: got=%v want=%v", token, got, want)
		}
	}

	assertTokenStyle("func", p.theme.MarkdownCodeKeyword)
	assertTokenStyle("int", p.theme.MarkdownCodeType)
	assertTokenStyle("string", p.theme.MarkdownCodeType)
	assertTokenStyle("add", p.theme.MarkdownCodeFunction)
	assertTokenStyle("itoa", p.theme.MarkdownCodeFunction)
	assertTokenStyle("\"ok\"", p.theme.MarkdownCodeString)
	assertTokenStyle("42", p.theme.MarkdownCodeNumber)
	assertTokenStyle("+", p.theme.MarkdownCodeOperator)
	assertTokenStyle("// note", p.theme.MarkdownCodeComment)
}

func findScreenLine(screen tcell.Screen, width, height int, want string) (bool, int, int) {
	wantRunes := []rune(want)
	if len(wantRunes) == 0 {
		return false, 0, 0
	}
	for y := 0; y < height; y++ {
		for x := 0; x+len(wantRunes) <= width; x++ {
			match := true
			for i, r := range wantRunes {
				got, _, _, _ := screen.GetContent(x+i, y)
				if got == 0 {
					got = ' '
				}
				if got != r {
					match = false
					break
				}
			}
			if match {
				return true, x, y
			}
		}
	}
	return false, 0, 0
}

func TestThemeModalDrawsOnNarrowScreen(t *testing.T) {
	p := NewHomePage(model.EmptyHome())
	p.SetTheme(NordTheme())
	p.SetThemeModalData([]ThemeModalEntry{{ID: "nord", Name: "Nord"}}, "nord")
	p.ShowThemeModal("nord")

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	w, h := 44, 12
	screen.SetSize(w, h)
	p.drawThemeModal(screen)

	text := dumpScreenText(screen, w, h)
	if !strings.Contains(text, "Themes") {
		t.Fatalf("expected theme modal on narrow screen, got:\n%s", text)
	}
}
