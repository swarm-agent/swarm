package app

import "github.com/gdamore/tcell/v2"

func (a *App) requestQuit() {
	if a == nil {
		return
	}
	if a.onboardingCancel != nil {
		a.onboardingCancel()
	}
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}
