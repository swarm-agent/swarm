package app

import (
	"fmt"
	"strings"

	"swarm-refactor/swarmtui/internal/ui"
)

func buildFlagTaskPrompt(problem, sessionID string, devMode bool) (string, error) {
	if !devMode {
		return "", fmt.Errorf("/flag is available only in dev mode")
	}
	sessionID = strings.TrimSpace(sessionID)
	problem = strings.TrimSpace(problem)
	if sessionID == "" {
		return "", fmt.Errorf("/flag requires an existing session to investigate.")
	}
	if problem == "" {
		return "", fmt.Errorf("Enter a problem after /flag.")
	}
	return strings.Join([]string{
		"Investigate a developer flag from a prior Swarm session.",
		"",
		"Prior session ID: " + sessionID,
		"Reported problem:",
		problem,
		"",
		"First dump and inspect that session through the canonical development session-dump path. Use ./scripts/session-dump-via-api.sh with the matching loopback Desktop session URL; never inspect Pebble directly. Use the dump as evidence, then search the current workspace for the code paths responsible for the reported problem. Diagnose the likely cause, make a safe scoped correction when the evidence supports one, and report validation plus relevant filepaths. If the dump is unavailable, report the exact blocker instead of bypassing the canonical path.",
	}, "\n"), nil
}

func (a *App) handleFlagCommand(args []string) {
	sessionID := ""
	if a.route == "v3chat" && a.v3Chat != nil {
		sessionID = a.v3Chat.SessionID()
	} else if a.route == "chat" && a.chat != nil {
		sessionID = a.chat.SessionID()
	}
	prompt, err := buildFlagTaskPrompt(strings.Join(args, " "), sessionID, a.startupDevMode())
	if err != nil {
		a.home.SetStatus(err.Error())
		a.showToast(ui.ToastError, err.Error())
		return
	}
	// Pass one opaque argument so reported /task flags remain problem text.
	a.handleTaskCommand([]string{prompt})
}
