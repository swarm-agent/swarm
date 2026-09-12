package app

import (
	"context"
	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/ui"
	"time"
)

// Keep terminal dispatch available while review/baseline waits on the daemon.
func (a *App) runOnboardingReview(kind ui.HomeActionKind, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	a.onboardingCancel = cancel
	var request client.OnboardingBaseline
	if kind == ui.HomeActionBaselineOnboardingRepository {
		request = a.home.OnboardingBaselineRequest()
	}
	go func() {
		defer cancel()
		result := onboardingWorkspaceResult{path: path}
		if kind == ui.HomeActionBaselineOnboardingRepository {
			state, err := a.api.PrepareOnboardingBaseline(ctx, request)
			result.repository = &state
			result.err = err
			result.prepared = err == nil
		} else {
			review, err := a.api.ReviewOnboardingRepository(ctx, path)
			result.review = &review
			result.err = err
		}
		select {
		case a.onboardingWorkspaceCh <- result:
		case <-ctx.Done():
			return
		}
		if a.screen != nil {
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(interruptOnboardingReady))
		}
	}()
}
