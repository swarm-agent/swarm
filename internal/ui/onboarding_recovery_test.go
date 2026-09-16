package ui

import (
	"errors"
	"github.com/gdamore/tcell/v2"
	"strings"
	"testing"
	"time"
)

// Requirement: interrupted setup resumes without account/password mutation and
// readiness must finish before handoff. RunPrerequisite with a real simulation
// screen and channel-driven fake service is the narrowest deterministic UI proof.
func TestPrerequisiteResumeReadiness(t *testing.T) {
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(80, 24)
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	handoffs := 0
	go func() {
		result <- RunPrerequisite(s, PrerequisiteActions{InitialResume: true, InitialCreated: true, InitialUsername: "developer", PasswordDone: func() bool { return true }, Status: func() string { return "Waiting for authenticated readiness" }, Begin: func(string) (bool, error) { return false, errors.New("must not recreate account") }, Complete: func(p []byte) error {
			if len(p) != 0 {
				return errors.New("password replay")
			}
			close(entered)
			<-release
			return nil
		}, Handoff: func() error { handoffs++; return nil }})
	}()
	s.PostEventWait(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("resume did not start")
	}
	select {
	case <-result:
		t.Fatal("exited before readiness")
	default:
	}
	close(release)
	select {
	case err := <-result:
		if err != nil || handoffs != 1 {
			t.Fatalf("handoff=%d err=%v", handoffs, err)
		}
	case <-time.After(time.Second):
		t.Fatal("no handoff")
	}
}

// Requirement: retry is explicit and never routes back into password choice;
// busy input cannot trigger another install. Key/render model proves this locally.
func TestPrerequisiteRetryState(t *testing.T) {
	p := &PrerequisiteScreen{Phase: "retry", Username: "developer", Progress: "Readiness deadline reached"}
	if submit, _ := p.Key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); !submit || p.Phase != "retry" {
		t.Fatal("retry not explicit")
	}
	p.Busy = true
	if submit, _ := p.Key(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); submit {
		t.Fatal("busy retry admitted")
	}
	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(80, 24)
	p.Draw(s)
	var text strings.Builder
	for y := 0; y < 24; y++ {
		for x := 0; x < 80; x++ {
			r, _, _, _ := s.GetContent(x, y)
			text.WriteRune(r)
		}
	}
	if !strings.Contains(text.String(), "Readiness deadline reached") {
		t.Fatal("lost stage progress")
	}
}
