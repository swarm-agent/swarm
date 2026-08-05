package ui

import (
	"strings"
	"time"
)

const homeTipRotationInterval = 12 * time.Second

var homeTips = []string{
	"Ask Swarm for three theme variants, then apply your favorite.",
	"Apply a theme to one workspace without changing your global theme.",
	"Turn a trusted project script into an Action with prompted inputs.",
	"Ask Swarm to organize your Actions; launch them from the Action menu.",
	"Turn recurring project conventions into a reusable Workspace Skill.",
	"Attach a Skill before prompting to load project-specific rules.",
	"Drop a Todo into chat to turn it into an active work session.",
	"Ask Swarm to update several workspace Todos in one pass.",
	"Link related directories when work crosses repository boundaries.",
	"Open risky or long-running work in an isolated worktree session.",
	"Run several Finders in parallel to audit different subsystems.",
	"Send independent implementation scopes to Coders in parallel.",
	"Ask Designer for multiple UI variants when the direction is unclear.",
	"Keep every design variant until you choose one to promote.",
	"Integrate selected Coder branches as one all-or-nothing batch.",
	"Find review worktrees that are missing from your current checkout.",
	"Search past sessions for an error, symbol, or earlier decision.",
	"Archive several finished sessions together after review.",
	"Set an auto-archive delay for reviewed sessions in Settings.",
	"Move between Desktop and TUI without abandoning the current session.",
	"Use Plan mode to lock scope and checkpoints before implementation.",
	"Add another checkpoint when work needs its own review boundary.",
	"Change direction explicitly; Swarm can revise the active checkpoint.",
	"Assign different models to Swarm, Finder, Coder, and Designer.",
	"Use a fast model for Auto mode and a reasoning model for Plan mode.",
	"Save allow rules for trusted tools and deny rules for risky patterns.",
	"Drag files, snippets, or images into chat as working context.",
	"TUI: press Ctrl+P to inspect the full plan and checkpoint status.",
	"TUI: press Ctrl+W to switch the active workspace directory.",
	"TUI: press Ctrl+X to browse and resume recent sessions.",
	"Type /tips to hide or show these tips.",
}

// HomeTips returns the launch-tip catalog in its display order.
func HomeTips() []string {
	return append([]string(nil), homeTips...)
}

func homeTipLines(tip string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	lines := wrapVoiceModalText("Tip: "+strings.TrimSpace(tip), width)
	if len(lines) <= maxLines {
		return lines
	}
	allLines := lines
	lines = append([]string(nil), allLines[:maxLines]...)
	lines[maxLines-1] = clampEllipsis(strings.Join(allLines[maxLines-1:], " "), width)
	return lines
}
