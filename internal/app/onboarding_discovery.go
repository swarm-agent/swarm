package app

import (
	"context"
	"time"

	"github.com/gdamore/tcell/v2"
)

// Discovery is read-only and daemon-local. Keep the terminal responsive and use
// the existing result channel so only the UI thread updates onboarding state.
func (a *App) discoverOnboardingRepositories() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a.onboardingCancel = cancel
	go func() {
		defer cancel()
		entries, err := a.api.DiscoverWorkspaces(ctx, 100, nil)
		result := onboardingWorkspaceResult{repositories: entries, discovered: true, err: err}
		// One operation owns the buffered onboarding channel while Pending is
		// set. Deliver timeout errors too, so the UI cannot remain stuck busy.
		a.onboardingWorkspaceCh <- result
		if a.screen != nil {
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(interruptOnboardingReady))
		}
	}()
}
