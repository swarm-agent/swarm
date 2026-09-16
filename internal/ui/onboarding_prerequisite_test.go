package ui

import (
	"github.com/gdamore/tcell/v2"
	"strings"
	"swarm-refactor/swarmtui/internal/model"
	"testing"
)

// Requirement: the disclosed account action submits once without arming a
// second confirmation; invalid input and Exit cannot invoke the operation.
// The production key model and tcell simulation are the narrowest UI layer.
func TestPrerequisiteConsentAndCancel(t *testing.T) {
	p := &PrerequisiteScreen{}
	enter := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if submit, _ := p.Key(enter); submit || p.Error == "" {
		t.Fatal("empty accepted")
	}
	p.Username = "developer"
	if submit, _ := p.Key(enter); !submit {
		t.Fatal("first Enter did not submit the disclosed action")
	}
	if submit, cancel := p.Key(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); submit || !cancel {
		t.Fatal("cancel failed")
	}
	if submit, _ := p.Key(enter); !submit {
		t.Fatal("confirmed continuation missing")
	}
}

func TestPrerequisiteScreen(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {50, 22}} {
		s := tcell.NewSimulationScreen("UTF-8")
		if err := s.Init(); err != nil {
			t.Fatal(err)
		}
		s.SetSize(size[0], size[1])
		p := &PrerequisiteScreen{}
		p.Draw(s)
		s.Show()
		var text strings.Builder
		for y := 0; y < size[1]; y++ {
			for x := 0; x < size[0]; x++ {
				r, _, _, _ := s.GetContent(x, y)
				text.WriteRune(r)
			}
			text.WriteByte('\n')
		}
		for _, want := range []string{"BEFORE STEP 1", "Username", "Create account and continue"} {
			if !strings.Contains(text.String(), want) {
				t.Fatalf("%v missing %s", size, want)
			}
		}
		if strings.Contains(text.String(), "[1]") {
			t.Fatal("legacy menu rendered")
		}
		s.Fini()
	}
}

// Requirement: optional passwords stay masked inside RunPrerequisite, mismatches
// cannot submit, and cancel/back erase buffers. Simulated keys/rendering exercise
// the production model rather than inspecting source strings.
func TestPrerequisiteOptionalPassword(t *testing.T) {
	enter := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	p := &PrerequisiteScreen{Username: "developer", Phase: "choice"}
	if submit, _ := p.Key(enter); submit || p.Phase != "password" {
		t.Fatal("password choice skipped entry")
	}
	for _, r := range "test-only-password" {
		p.Key(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	p.Key(enter)
	if submit, _ := p.Key(enter); submit {
		t.Fatal("mismatch submitted")
	}
	for _, r := range "test-only-password" {
		p.Key(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
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
	if strings.Contains(text.String(), "test-only-password") || !strings.Contains(text.String(), "••••") {
		t.Fatal("password not masked")
	}
	if submit, _ := p.Key(enter); !submit {
		t.Fatal("matching password rejected")
	}
	retained := p.password
	p.Key(tcell.NewEventKey(tcell.KeyCtrlB, 0, tcell.ModNone))
	if p.Phase != "choice" || len(p.password) != 0 {
		t.Fatal("back did not clear")
	}
	for _, b := range retained {
		if b != 0 {
			t.Fatal("secret buffer retained")
		}
	}
	p.Focus = 1
	if submit, _ := p.Key(enter); !submit || len(p.password) != 0 {
		t.Fatal("skip requires password")
	}
}

// Requirement: real runner keeps its terminal active across creation, optional
// password submission and Skip, and never provisions on Exit.
func TestPrerequisiteRunnerInApp(t *testing.T) {
	for _, mode := range []string{"password", "skip", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := &onboardingObservedScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), frames: make(chan string, 32)}
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			defer s.Fini()
			s.SetSize(80, 24)
			begins, finishes := 0, 0
			post := func(key tcell.Key, r rune) { s.PostEventWait(tcell.NewEventKey(key, r, tcell.ModNone)) }
			go func() {
				for _, r := range "developer" {
					post(tcell.KeyRune, r)
				}
				post(tcell.KeyEnter, 0)
				s.waitFor(t, "Your account is ready")
				switch mode {
				case "cancel":
					post(tcell.KeyEscape, 0)
				case "skip":
					post(tcell.KeyTab, 0)
					post(tcell.KeyEnter, 0)
				default:
					post(tcell.KeyEnter, 0)
					for _, r := range "test-only-password" {
						post(tcell.KeyRune, r)
					}
					post(tcell.KeyEnter, 0)
					for _, r := range "test-only-password" {
						post(tcell.KeyRune, r)
					}
					post(tcell.KeyEnter, 0)
				}
			}()
			err := RunPrerequisite(s, PrerequisiteActions{Begin: func(name string) (bool, error) {
				begins++
				if name != "developer" {
					t.Fatal("wrong name")
				}
				return true, nil
			}, Complete: func(p []byte) error {
				finishes++
				want := ""
				if mode == "password" {
					want = "test-only-password"
				}
				if string(p) != want {
					t.Fatal("wrong password handoff")
				}
				return nil
			}})
			want := 1
			if mode == "cancel" {
				want = 0
			}
			if err != nil || begins != 1 || finishes != want {
				t.Fatalf("begin=%d complete=%d err=%v", begins, finishes, err)
			}
		})
	}
}

// Requirement: a prefilled name leaves Identity focused on the Swarm name,
// without skipping Identity or requiring the person to retype the OS username.
func TestPrerequisiteIdentityPrefillFocus(t *testing.T) {
	p := NewHomePage(model.HomeModel{OnboardingUsername: "developer", OnboardingRequired: true})
	p.ShowOnboardingLocked("")
	if p.onboarding.Phase != onboardingPhaseIdentity || p.onboarding.Focus != onboardingFocusSwarmName || p.model.OnboardingUsername != "developer" {
		t.Fatal("prefill did not focus Swarm name")
	}
}
