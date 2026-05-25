package app

import "github.com/gdamore/tcell/v2"

func (a *App) requestQuit() {
	if a == nil {
		return
	}
	a.quitRequested = true
	if a.screen != nil {
		a.screen.PostEventWait(tcell.NewEventInterrupt(interruptQuit))
	}
}
