package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestDrawWorkspaceInfoBoxUsesSingleLineGitAndCwd(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		Directories: []model.DirectoryItem{{
			Name:           "proj",
			Path:           "/tmp/proj",
			HasGit:         true,
			Branch:         "feature/header",
			Upstream:       "origin/main",
			DirtyCount:     24,
			StagedCount:    3,
			ModifiedCount:  5,
			UntrackedCount: 7,
			ConflictCount:  9,
			AheadCount:     2,
			BehindCount:    4,
			IsWorkspace:    false,
		}},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 3)

	page.drawWorkspaceInfoBox(screen, Rect{X: 0, Y: 0, W: 100, H: 3})
	text := dumpScreenTextHomeTopbar(screen, 100, 3)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 rendered lines, got %d", len(lines))
	}
	if !strings.Contains(lines[1], "cwd /tmp/proj  /workspace save") {
		t.Fatalf("expected directory hint on single content line, got:\n%s", text)
	}
	if !strings.Contains(lines[1], "git feature/header") {
		t.Fatalf("expected git summary on single content line, got:\n%s", text)
	}
	if strings.Contains(text, "staged:") || strings.Contains(text, "modified:") || strings.Contains(text, "untracked:") || strings.Contains(text, "conflicts:") {
		t.Fatalf("git counts line should be absent, got:\n%s", text)
	}

	line := lines[1]
	if last := lastNonSpaceRuneIndex(line); last < len([]rune(line))-5 {
		t.Fatalf("expected git status to hug right edge, last non-space at %d in %q", last, line)
	}

	assertScreenTokenStyle(t, screen, 1, line, "feature/header", 0, styleForCurrentCellBackground(page.theme.Secondary))
	assertScreenTokenStyle(t, screen, 1, line, "+3", 1, styleForCurrentCellBackground(page.theme.Success))
	assertScreenTokenStyle(t, screen, 1, line, "~5", 1, styleForCurrentCellBackground(page.theme.Warning))
	assertScreenTokenStyle(t, screen, 1, line, "?7", 1, styleForCurrentCellBackground(page.theme.Accent))
	assertScreenTokenStyle(t, screen, 1, line, "!9", 1, styleForCurrentCellBackground(page.theme.Error))
	assertScreenTokenStyle(t, screen, 1, line, "↑2", 1, styleForCurrentCellBackground(page.theme.Secondary))
	assertScreenTokenStyle(t, screen, 1, line, "↓4", 1, styleForCurrentCellBackground(page.theme.Secondary))
}

func TestWorkspaceItemsRemainVisibleWithoutActiveWorkspace(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		Workspaces: []model.Workspace{
			{Name: "alpha", Icon: "a", Path: "/tmp/alpha"},
			{Name: "beta", Icon: "b", Path: "/tmp/beta"},
		},
		Directories: []model.DirectoryItem{{
			Name:         "random",
			Path:         "/tmp/random",
			ResolvedPath: "/tmp/random",
			IsWorkspace:  false,
		}},
	})

	items := page.workspaceItems()
	if len(items) != 2 {
		t.Fatalf("workspaceItems len = %d, want 2", len(items))
	}
	if items[0].Action != "workspace" || items[0].Index != 0 {
		t.Fatalf("first workspace item = %+v, want workspace index 0", items[0])
	}
	if items[1].Action != "workspace" || items[1].Index != 1 {
		t.Fatalf("second workspace item = %+v, want workspace index 1", items[1])
	}
	if !strings.Contains(items[0].Label, "alpha") || !strings.Contains(items[1].Label, "beta") {
		t.Fatalf("workspace labels = %#v, want alpha/beta", items)
	}
}

func TestDrawWorkspaceInfoBoxUsesExactCWDInsideSavedWorkspace(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		CWD: "/tmp/runway",
		Workspaces: []model.Workspace{{
			Name:        "swarm-go",
			Path:        "/tmp/swarm-go",
			Directories: []string{"/tmp/swarm-go", "/tmp/runway"},
			Active:      true,
		}},
		Directories: []model.DirectoryItem{
			{
				Name:         "runway",
				Path:         "/tmp/runway",
				ResolvedPath: "/tmp/runway",
				Branch:       "feature/runway",
				HasGit:       true,
				IsWorkspace:  false,
			},
			{
				Name:         "swarm-go",
				Path:         "/tmp/swarm-go",
				ResolvedPath: "/tmp/swarm-go",
				Branch:       "main",
				HasGit:       true,
				IsWorkspace:  true,
			},
		},
	})

	if got := page.primaryDirectory().ResolvedPath; got != "/tmp/runway" {
		t.Fatalf("primaryDirectory().ResolvedPath = %q, want /tmp/runway", got)
	}
	if page.primaryDirectory().IsWorkspace {
		t.Fatalf("primaryDirectory().IsWorkspace = true, want false for linked cwd")
	}

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 3)

	page.drawWorkspaceInfoBox(screen, Rect{X: 0, Y: 0, W: 100, H: 3})
	text := dumpScreenTextHomeTopbar(screen, 100, 3)

	if !strings.Contains(text, "directory:runway") {
		t.Fatalf("expected directory header for linked cwd, got:\n%s", text)
	}
	if !strings.Contains(text, "cwd /tmp/runway  /workspace save") {
		t.Fatalf("expected exact cwd line for linked cwd, got:\n%s", text)
	}
	if strings.Contains(text, "cwd /tmp/swarm-go") {
		t.Fatalf("unexpected workspace-root cwd in header, got:\n%s", text)
	}
}

func TestDrawWorkspaceInfoBoxDoesNotLeakUmbrellaLinkedDirectoriesIntoChildWorkspace(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		CWD: "/tmp/swarm-go",
		Workspaces: []model.Workspace{
			{
				Name:        "swarm-web",
				Path:        "/tmp/swarm-web",
				Directories: []string{"/tmp/swarm-web", "/tmp/swarm-go", "/tmp/swarm-web/ui"},
				Active:      false,
			},
			{
				Name:        "swarm-go",
				Path:        "/tmp/swarm-go",
				Directories: []string{"/tmp/swarm-go"},
				Active:      true,
			},
		},
		Directories: []model.DirectoryItem{
			{Name: "swarm-go", Path: "/tmp/swarm-go", ResolvedPath: "/tmp/swarm-go", Branch: "main", HasGit: true, IsWorkspace: true},
			{Name: "swarm-web", Path: "/tmp/swarm-web", ResolvedPath: "/tmp/swarm-web", Branch: "main", HasGit: true, IsWorkspace: true},
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(100, 4)

	page.drawWorkspaceInfoBox(screen, Rect{X: 0, Y: 0, W: 100, H: 4})
	text := dumpScreenTextHomeTopbar(screen, 100, 4)

	if strings.Contains(text, "linked:") {
		t.Fatalf("child standalone workspace should not show umbrella linked count, got:\n%s", text)
	}
	if strings.Contains(text, "Multi-root workspace") {
		t.Fatalf("child standalone workspace should not show umbrella multi-root hint, got:\n%s", text)
	}
	if !strings.Contains(text, "workspace:swarm-go") {
		t.Fatalf("expected child workspace header, got:\n%s", text)
	}
}

func assertScreenTokenStyle(t *testing.T, screen tcell.Screen, y int, line, token string, offset int, want tcell.Style) {
	t.Helper()
	x := runeIndexOf(line, token)
	if x < 0 {
		t.Fatalf("token %q missing from line %q", token, line)
	}
	_, _, style, _ := screen.GetContent(x+offset, y)
	got := styleForCurrentCellBackground(style)
	if !stylesEqualHomeTopbar(got, want) {
		t.Fatalf("token %q style = %v, want %v", token, got, want)
	}
}

func runeIndexOf(text, token string) int {
	textRunes := []rune(text)
	tokenRunes := []rune(token)
	if len(tokenRunes) == 0 || len(tokenRunes) > len(textRunes) {
		return -1
	}
	for i := 0; i <= len(textRunes)-len(tokenRunes); i++ {
		matched := true
		for j := range tokenRunes {
			if textRunes[i+j] != tokenRunes[j] {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	return -1
}

func lastNonSpaceRuneIndex(text string) int {
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] != ' ' {
			return i
		}
	}
	return -1
}

func dumpScreenTextHomeTopbar(screen tcell.Screen, width, height int) string {
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

func stylesEqualHomeTopbar(a, b tcell.Style) bool {
	afg, abg, aa := a.Decompose()
	bfg, bbg, ba := b.Decompose()
	return afg == bfg && abg == bbg && aa == ba
}
