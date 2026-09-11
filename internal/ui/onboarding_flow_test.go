package ui

import (
	"errors"
	"github.com/gdamore/tcell/v2"
	"strings"
	"swarm-refactor/swarmtui/internal/model"
	"testing"
	"time"
)

// Requirement: RunPrerequisite must advance from one disclosed account action
// through password/SSH choices and delayed readiness without queued-key replay.
// The SSH form must stay hidden until Add is chosen; Skip must bypass it, both
// after fresh account/password creation and when resuming an undecided SSH stage.
// Threat: repeated Enter can mutate the next stage or retry a failed mutation.
// A real tcell event loop with channel-controlled callbacks is the narrowest
// hermetic boundary proving dispatch, progress, masking and handoff ordering.
type onboardingObservedScreen struct {
	tcell.SimulationScreen
	frames chan string
}

func (s *onboardingObservedScreen) Show() {
	s.SimulationScreen.Show()
	w, h := s.Size()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := s.GetContent(x, y)
			b.WriteRune(r)
		}
		b.WriteByte('\n')
	}
	select {
	case s.frames <- b.String():
	default:
	}
}
func (s *onboardingObservedScreen) waitFor(t *testing.T, text string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame := <-s.frames:
			if strings.Contains(frame, text) {
				return
			}
		case <-timer.C:
			t.Errorf("screen did not show %q", text)
			s.PostEventWait(tcell.NewEventKey(tcell.KeyEscape, 0, 0))
			return
		}
	}
}
func TestOnboardingFlowSSHStartsWithoutExtraEnter(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		skip   bool
		resume bool
	}{
		{"fresh-add", false, false},
		{"fresh-skip", true, false},
		{"resume-add", false, true},
		{"resume-skip", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			skip := scenario.skip
			s := &onboardingObservedScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), frames: make(chan string, 32)}
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			defer s.Fini()
			s.SetSize(100, 28)
			post := func(k tcell.Key, r rune) { s.PostEventWait(tcell.NewEventKey(k, r, 0)) }
			keyRequired, passwordDone := scenario.resume, scenario.resume
			begins, choices, completes, handoffs := 0, 0, 0
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- RunPrerequisite(s, PrerequisiteActions{InitialResume: scenario.resume, InitialCreated: scenario.resume, InitialUsername: "developer", Begin: func(name string) (bool, error) {
					begins++
					if name != "developer" {
						return false, errors.New("wrong account")
					}
					return true, nil
				}, PasswordDone: func() bool { return passwordDone }, SSHRequired: func() bool { return keyRequired }, ChooseSSH: func(key string, gotSkip bool) error {
					choices++
					if gotSkip != skip || (!skip && key != "ssh-ed25519 fixture") || (skip && key != "") {
						return errors.New("wrong key choice")
					}
					keyRequired = false
					return nil
				}, SSHGuidance: func(string) string { return "SHA256:fixture; login not verified" }, Status: func() string { return "Waiting for readiness" }, Complete: func(p []byte) error {
					completes++
					if len(p) != 0 {
						return errors.New("password replay")
					}
					if !passwordDone {
						passwordDone, keyRequired = true, true
						return nil
					}
					if keyRequired {
						return errors.New("installation before SSH choice")
					}
					close(entered)
					<-release
					return nil
				}, Handoff: func() error { handoffs++; return nil }})
			}()
			if !scenario.resume {
				s.waitFor(t, "Set up your user account.")
				for _, r := range "developer" {
					post(tcell.KeyRune, r)
				}
				post(tcell.KeyEnter, 0)
				s.waitFor(t, "Would you like to set a login password?")
				post(tcell.KeyTab, 0)
				post(tcell.KeyEnter, 0)
			}
			s.waitFor(t, "Would you like to add an SSH key?")
			if skip {
				post(tcell.KeyTab, 0)
			} else {
				post(tcell.KeyEnter, 0)
				s.waitFor(t, "Optional SSH public key")
				for _, r := range "ssh-ed25519 fixture" {
					post(tcell.KeyRune, r)
				}
			}
			post(tcell.KeyEnter, 0)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("SSH decision did not start installation")
			}
			post(tcell.KeyEnter, 0)
			post(tcell.KeyEscape, 0)
			select {
			case <-done:
				t.Fatal("left before readiness")
			default:
			}
			close(release)
			select {
			case err := <-done:
				wantBegins, wantCompletes := 1, 2
				if scenario.resume {
					wantBegins, wantCompletes = 0, 1
				}
				if err != nil || begins != wantBegins || choices != 1 || completes != wantCompletes || handoffs != 1 {
					t.Fatalf("begins=%d choices=%d completes=%d handoffs=%d err=%v", begins, choices, completes, handoffs, err)
				}
				if skip {
					for len(s.frames) > 0 {
						if strings.Contains(<-s.frames, "Optional SSH public key") {
							t.Fatal("skip displayed key entry")
						}
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handoff stalled")
			}
		})
	}
}

// Requirement: Identity Enter advances to the next field, final-field Enter
// saves once; selected workspace Enter verifies without implicit Git mutation.
// HomePage's event/action boundary proves the keyboard contract independently
// of API admission, which remains authoritative for filesystem and Git safety.
func TestOnboardingFlowIdentityAndWorkspace(t *testing.T) {
	p := NewHomePage(model.HomeModel{OnboardingRequired: true})
	p.ShowOnboardingLocked("")
	key := func(k tcell.Key) { p.handleOnboardingKey(tcell.NewEventKey(k, 0, 0)) }
	p.model.OnboardingUsername = "developer"
	p.onboarding.Focus = onboardingFocusUsername
	key(tcell.KeyEnter)
	if p.onboarding.Focus != onboardingFocusSwarmName || p.pendingHomeAction != nil {
		t.Fatal("first field submitted")
	}
	key(tcell.KeyEnter)
	if p.onboarding.Error == "" {
		t.Fatal("missing name unexplained")
	}
	p.model.OnboardingSwarmName = "Studio"
	key(tcell.KeyEnter)
	if p.pendingHomeAction == nil || p.pendingHomeAction.Kind != HomeActionSaveOnboarding {
		t.Fatal("final field did not save")
	}
	first := p.pendingHomeAction
	key(tcell.KeyEnter)
	if p.pendingHomeAction != first {
		t.Fatal("duplicate identity save")
	}
	p.pendingHomeAction = nil
	p.ShowOnboardingWorkspace("")
	p.SetOnboardingWorkspacePath("/project")
	key(tcell.KeyCtrlL)
	key(tcell.KeyEnter)
	if p.pendingHomeAction == nil || p.pendingHomeAction.Kind != HomeActionInspectOnboardingRepository || !p.onboarding.Pending {
		t.Fatal("selection did not verify")
	}
}

// Requirement: busy provider saves cannot be resubmitted/skipped and already
// connected selections advance without opening credential entry. The HomePage
// action boundary is the narrowest layer proving no duplicate auth dispatch.
func TestOnboardingFlowProviderBusyAndConnected(t *testing.T) {
	p := NewHomePage(model.HomeModel{OnboardingRequired: true})
	p.ShowOnboardingProvider("")
	p.authModal.Providers = []AuthModalProvider{{ID: "fixture", Ready: true, Runnable: true}}
	p.authModal.reconcileSelections()
	p.authModal.Loading = true
	p.handleOnboardingKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !p.OnboardingProviderActive() || p.authModal.Editor != nil {
		t.Fatal("busy provider choice advanced")
	}
	p.authModal.Loading = false
	p.handleOnboardingKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if !p.OnboardingWorkspaceActive() || p.authModal.Editor != nil {
		t.Fatal("connected choice asked for credentials")
	}
}

// Requirement: pasted newlines remain key data and never activate setup.
// This input-layer negative case prevents bracketed multi-key paste from
// becoming a submission, while shared authority still validates key syntax.
func TestOnboardingFlowPasteDoesNotSubmit(t *testing.T) {
	p := &PrerequisiteScreen{Phase: "ssh", pasting: true, publicKey: "ssh-ed25519 fixture"}
	if submit, cancel := p.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0)); submit || cancel || !strings.HasSuffix(p.publicKey, "\n") {
		t.Fatal("paste became an action or lost record boundary")
	}
	p.pasting = false
	if submit, _ := p.Key(tcell.NewEventKey(tcell.KeyEnter, 0, 0)); !submit {
		t.Fatal("deliberate Enter did not submit for validation")
	}
}
