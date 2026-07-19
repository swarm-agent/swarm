package app

import "strings"

// handleCodexCommand opens the account-scoped ChatGPT usage surface. Codex
// model selection and priority belong to /agents and /profiles; /codex does not
// mutate model preferences.
func (a *App) handleCodexCommand(args []string) {
	if a == nil || a.home == nil {
		return
	}
	refresh := len(args) == 0
	if len(args) == 1 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "", "status", "refresh":
			refresh = true
		case "help":
			a.home.SetCommandOverlay([]string{
				"/codex           open Codex account usage",
				"/codex refresh   refresh usage and reset credits",
				"Use /auth to connect ChatGPT OAuth when account usage is unavailable.",
			})
			a.home.SetStatus("Codex account usage help")
			return
		default:
			a.home.ClearCommandOverlay()
			a.home.SetStatus("usage: /codex [refresh]")
			return
		}
	} else if len(args) > 1 {
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /codex [refresh]")
		return
	}
	a.openCodexUsageModal()
	if refresh {
		a.refreshHomeCodexAccount()
	}
}
