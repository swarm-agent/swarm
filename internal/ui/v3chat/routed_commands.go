package v3chat

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NewCommand describes one /new invocation. An empty Prompt opens a local
// primer; a non-empty Prompt is ready for immediate routed submission.
type NewCommand struct {
	Prompt            string
	PlanModeRequested bool
}

// ParseNewCommand recognizes bare, prompt, and plan [prompt] forms. Legacy
// worktree/wp syntax maps to the same mandatory route without carrying intent.
func ParseNewCommand(input string) (NewCommand, bool) {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) < len("/new") || !strings.EqualFold(trimmed[:len("/new")], "/new") {
		return NewCommand{}, false
	}
	if len(trimmed) > len("/new") {
		next, _ := utf8.DecodeRuneInString(trimmed[len("/new"):])
		if !isCommandSpace(next) {
			return NewCommand{}, false
		}
	}
	body := strings.TrimSpace(trimmed[len("/new"):])
	if body == "" {
		return NewCommand{}, true
	}
	directive, prompt := splitCommandWord(body)
	switch strings.ToLower(directive) {
	case "worktree":
		return NewCommand{Prompt: prompt}, true
	case "plan":
		return NewCommand{Prompt: prompt, PlanModeRequested: true}, true
	case "wp":
		return NewCommand{Prompt: prompt, PlanModeRequested: true}, true
	default:
		return NewCommand{Prompt: body}, true
	}
}

// CommitCommand describes a manual commit message or the explicit AI Commit
// form backed by the existing suggestion and commit APIs.
type CommitCommand struct {
	Message string
	AI      bool
}

// ParseCommitCommand recognizes /commit <message> and exactly /commit ai.
func ParseCommitCommand(input string) (CommitCommand, bool, error) {
	command, argument := splitCommandWord(input)
	if !strings.EqualFold(command, "/commit") {
		return CommitCommand{}, false, nil
	}
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return CommitCommand{}, true, fmt.Errorf("usage: /commit <message>|ai")
	}
	if strings.EqualFold(argument, "ai") {
		return CommitCommand{AI: true}, true, nil
	}
	return CommitCommand{Message: argument}, true, nil
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
	return unicode.IsSpace(value)
}
