package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestSkillChangeModalDrawsOnNarrowScreen(t *testing.T) {
	page := NewChatPage(ChatPageOptions{
		SessionID:      "session-1",
		ShowHeader:     true,
		AuthConfigured: true,
		SessionMode:    "plan",
	})

	page.upsertPendingPermission(ChatPermissionRecord{
		ID:            "perm_skill_narrow",
		SessionID:     "session-1",
		ToolName:      "manage-skill",
		Requirement:   "skill_change",
		ToolArguments: `{"action":"update","change":{"operation":"update","path":".agents/skills/demo/SKILL.md","before":"# Demo\nold","after":"# Demo\nnew"},"skill":{"canonical_name":"demo","name":"Demo"},"summary":"update demo skill"}`,
		Status:        "pending",
	})
	if !page.skillChangeModalActive() {
		t.Fatalf("expected skill change modal to be visible")
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
	if !strings.Contains(text, "Review Skill Change") {
		t.Fatalf("expected skill change modal header on narrow screen, got:\n%s", text)
	}
	if !strings.Contains(text, "Enter") {
		t.Fatalf("expected skill change modal controls on narrow screen, got:\n%s", text)
	}
}
