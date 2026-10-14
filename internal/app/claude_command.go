package app

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

// handleClaudeCommand shows Claude Code readiness, the Claude plan and the
// latest five-hour and weekly usage Claude Code reported to the daemon. It
// reads only; Claude model selection belongs to /agents and /favorites.
func (a *App) handleClaudeCommand(args []string) {
	if a == nil || a.home == nil {
		return
	}
	if len(args) > 1 || (len(args) == 1 && !isClaudeRefreshArg(args[0])) {
		if len(args) == 1 && strings.EqualFold(strings.TrimSpace(args[0]), "help") {
			a.home.SetCommandOverlay([]string{
				"/claude           Claude Code status, plan and usage limits",
				"/claude refresh   read them again",
				"Usage limits appear after a Claude Code turn has run since the daemon started.",
			})
			a.home.SetStatus("Claude Code usage help")
			return
		}
		a.home.ClearCommandOverlay()
		a.home.SetStatus("usage: /claude [refresh]")
		return
	}
	if a.api == nil {
		a.home.SetStatus("Claude Code usage API is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	usage, err := a.api.GetClaudeCodeUsage(ctx)
	if err != nil {
		a.home.ClearCommandOverlay()
		a.home.SetStatus(fmt.Sprintf("Claude Code usage unavailable: %v", err))
		return
	}
	a.home.SetCommandOverlay(claudeUsageLines(usage, time.Now()))
	a.home.SetStatus("Claude Code usage")
}

func isClaudeRefreshArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "status", "refresh":
		return true
	}
	return false
}

func claudeUsageLines(usage client.ClaudeCodeUsage, now time.Time) []string {
	cli := usage.CLI
	head := "Claude Code"
	if cli.Version != "" {
		head += " " + cli.Version
	}
	switch {
	case cli.Ready:
		head += " · signed in"
		if cli.AuthMethod != "" {
			head += " (" + cli.AuthMethod + ")"
		}
	case cli.Reason != "":
		head += " · not ready: " + cli.Reason
	default:
		head += " · not ready"
	}
	if cli.SubscriptionType != "" {
		head += " · plan " + cli.SubscriptionType
	}
	lines := []string{head}
	limits := usage.RateLimits
	if limits == nil {
		return append(lines, "Usage limits: no reading yet; they appear after a Claude Code turn runs.")
	}
	for _, w := range limits.Windows {
		line := fmt.Sprintf("%-9s %3d%% used", claudeWindowLabel(w.Name), int(math.Round(w.Utilization*100)))
		if w.ResetsAt > 0 {
			line += " · resets " + claudeResetLabel(time.Unix(w.ResetsAt, 0), now)
		}
		lines = append(lines, line)
	}
	state := "Status " + limits.Status
	if limits.IsUsingOverage {
		state += " · using extra usage"
	} else if limits.OverageStatus != "" {
		state += " · extra usage " + limits.OverageStatus
	}
	if !limits.ObservedAt.IsZero() {
		state += " · as of " + claudeAgo(now.Sub(limits.ObservedAt))
	}
	return append(lines, state)
}

func claudeWindowLabel(name string) string {
	switch name {
	case "five_hour":
		return "5-hour"
	case "seven_day":
		return "7-day"
	}
	return strings.ReplaceAll(name, "_", " ")
}

func claudeResetLabel(at, now time.Time) string {
	at = at.Local()
	if at.YearDay() == now.Local().YearDay() && at.Year() == now.Local().Year() {
		return at.Format("15:04")
	}
	return at.Format("Mon 15:04")
}

func claudeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
}
