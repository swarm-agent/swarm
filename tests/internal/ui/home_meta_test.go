package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"swarm-refactor/swarmtui/internal/model"
)

func TestHomeDrawMetaCompactIncludesWorkspaceGitCwdHint(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		Directories: []model.DirectoryItem{
			{
				Name:        "proj",
				Path:        "/tmp/proj",
				Branch:      "feature/meta",
				DirtyCount:  2,
				IsWorkspace: false,
			},
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 3)

	page.drawMeta(screen, Rect{X: 0, Y: 0, W: 120, H: 1}, layoutVariant{ShowDirectory: false})
	text := dumpScreenText(screen, 120, 1)

	if !strings.Contains(text, "workspace:proj") {
		t.Fatalf("meta missing workspace name: %q", text)
	}
	if !strings.Contains(text, "git:feature/meta") {
		t.Fatalf("meta missing git branch: %q", text)
	}
	if !strings.Contains(text, "cwd:/tmp/proj") {
		t.Fatalf("meta missing cwd: %q", text)
	}
	if !strings.Contains(text, "/workspace save") {
		t.Fatalf("meta missing /workspace save hint for non-workspace dir: %q", text)
	}
}

func TestHomeDrawMetaTwoLineKeepsCwdOnSecondLine(t *testing.T) {
	page := NewHomePage(model.HomeModel{
		Workspaces: []model.Workspace{
			{Name: "swarm", Active: true},
		},
		Directories: []model.DirectoryItem{
			{
				Name:        "swarm",
				Path:        "/workspace/swarm",
				Branch:      "main",
				DirtyCount:  0,
				IsWorkspace: true,
			},
		},
	})

	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(120, 4)

	page.drawMeta(screen, Rect{X: 0, Y: 0, W: 120, H: 2}, layoutVariant{ShowDirectory: true})
	text := dumpScreenText(screen, 120, 2)

	if !strings.Contains(text, "workspace:swarm") {
		t.Fatalf("line one missing workspace: %q", text)
	}
	if !strings.Contains(text, "git:main") {
		t.Fatalf("line one missing git: %q", text)
	}
	if !strings.Contains(text, "cwd:/workspace/swarm") {
		t.Fatalf("line two missing cwd: %q", text)
	}
	if strings.Contains(text, "  /workspace") {
		t.Fatalf("workspace hint should be absent for workspace dirs: %q", text)
	}
}
