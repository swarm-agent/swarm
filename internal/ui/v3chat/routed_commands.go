package v3chat

import (
	"fmt"
	"strings"
)

// NewCommand describes one /new invocation. An empty Prompt opens a local
// primer; a non-empty Prompt is ready for immediate routed submission.
type NewCommand struct {
	Prompt                   string
	ManagedWorktreeRequested bool
	PlanModeRequested        bool
}

// ParseNewCommand implements the same /new forms as Desktop: bare, prompt,
// worktree [prompt], plan [prompt], and wp [prompt].
func ParseNewCommand(input string) (NewCommand, bool) {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < len("/new") || !strings.EqualFold(trimmed[:len("/new")], "/new") {
		return NewCommand{}, false
	}
	if len(trimmed) > len("/new") && !isCommandSpace(rune(trimmed[len("/new")])) {
		return NewCommand{}, false
	}
	body := strings.TrimSpace(trimmed[len("/new"):])
	if body == "" {
		return NewCommand{}, true
	}
	directive, prompt := splitCommandWord(body)
	switch strings.ToLower(directive) {
	case "worktree":
		return NewCommand{Prompt: prompt, ManagedWorktreeRequested: true}, true
	case "plan":
		return NewCommand{Prompt: prompt, PlanModeRequested: true}, true
	case "wp":
		return NewCommand{Prompt: prompt, ManagedWorktreeRequested: true, PlanModeRequested: true}, true
	default:
		return NewCommand{Prompt: body}, true
	}
}

// WorktreeCommand describes the local /worktree on|off and /wt on|off primer
// controls. /worktrees is intentionally not an alias for this operation.
type WorktreeCommand struct {
	Enabled bool
}

// ParseWorktreeCommand recognizes only the local singular command and /wt
// shorthand; /worktrees remains available to the unrelated settings flow.
func ParseWorktreeCommand(input string) (WorktreeCommand, bool, error) {
	trimmed := strings.TrimSpace(input)
	command, argument := splitCommandWord(trimmed)
	if !strings.EqualFold(command, "/worktree") && !strings.EqualFold(command, "/wt") {
		return WorktreeCommand{}, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(argument)) {
	case "on":
		return WorktreeCommand{Enabled: true}, true, nil
	case "off":
		return WorktreeCommand{Enabled: false}, true, nil
	default:
		return WorktreeCommand{}, true, fmt.Errorf("usage: %s on|off", strings.ToLower(command))
	}
}

func splitCommandWord(value string) (string, string) {
	value = strings.TrimSpace(value)
	for index, r := range value {
		if isCommandSpace(r) {
			return value[:index], strings.TrimSpace(value[index:])
		}
	}
	return value, ""
}

func isCommandSpace(value rune) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}
