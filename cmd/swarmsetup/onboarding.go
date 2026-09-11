package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/ui"
)

func runPrerequisiteOnboarding(root string, desktop bool) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("initial device setup requires administrator privileges")
	}
	unlock, err := launcher.LockOnboarding()
	if err != nil {
		return err
	}
	defer unlock()
	account, err := launcher.ResumeOnboardingAccount(root)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	progress := "Ready for account setup"
	stage := func(message string) { mu.Lock(); progress = message; mu.Unlock() }
	destination := ""
	actions := ui.PrerequisiteActions{
		Status:       func() string { mu.Lock(); defer mu.Unlock(); return progress },
		PasswordDone: func() bool { return account != nil && account.PasswordDone() },
		SSHRequired:  func() bool { return account != nil && account.SSHRequired() },
		ChooseSSH:    func(key string, skip bool) error { return account.ChooseSSH(key, skip) },
		SSHGuidance:  func(address string) string { return account.SSHGuidance(address) },
		Begin: func(name string) (bool, error) {
			if account != nil {
				return false, fmt.Errorf("account already selected for this setup")
			}
			if err := launcher.PreflightInstallation(true); err != nil {
				return false, err
			}
			selected, err := launcher.BeginRecoverableOnboarding(name, root)
			if err != nil {
				return false, err
			}
			account = selected
			return account.Created(), nil
		},
		Complete: func(password []byte) error {
			if account == nil {
				return fmt.Errorf("select an account first")
			}
			err := account.Complete(password, func() error {
				if err := account.PrepareApplication(func() error {
					stage("Installing runtime and starting service…")
					installCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					return account.InstallForOnboarding(installCtx, root)
				}, func() error {
					stage("Waiting for authenticated application readiness…")
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					return account.WaitApplicationReady(ctx, desktop, stage)
				}); err != nil {
					return err
				}
				if desktop {
					profile, err := launcher.LoadRuntimeProfile("main", nil)
					if err != nil {
						return err
					}
					destination = launcher.DesktopURL(profile, 0)
				}
				return nil
			})
			if errors.Is(err, launcher.ErrOnboardingSSHRequired) {
				stage("Password choice saved. Add an SSH public key or skip.")
				return nil
			}
			if err != nil {
				stage("Setup stopped: " + err.Error())
			}
			return err
		},
		Destination: func() string { return destination },
	}
	if account != nil {
		if account.Stage() == "password-started" {
			stage("Password operation was interrupted; its result is uncertain. Resume preserves it without replay. Verify OS login separately.")
		}
		actions.InitialUsername = account.SelectedAccount().Username
		actions.InitialCreated = account.Created()
		actions.InitialResume = true
	}
	if desktop {
		// Navigation reuses the already-open setup browser; never start a browser with
		// inherited root credentials/environment as the selected user's application.
		actions.Handoff = func() error { return account.MarkStage("done") }
		return runDesktopPrerequisite(actions)
	}
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err = screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	actions.Handoff = func() error {
		if err := screen.Suspend(); err != nil {
			return err
		}
		err := account.RunApplication(context.Background(), false, false)
		resumeErr := screen.Resume()
		if err != nil {
			return err
		}
		if resumeErr != nil {
			return resumeErr
		}
		return account.MarkStage("done")
	}
	return ui.RunPrerequisite(screen, actions)
}
