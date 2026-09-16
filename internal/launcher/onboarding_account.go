package launcher

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"os/user"
	"time"
)

// OnboardingAccount binds password setup to the exact account created by this
// live setup. Existing accounts can be selected, but their passwords never reset.
// No password is persisted in Swarm or passed through argv, environment or logs.
type OnboardingAccount struct {
	account         *user.User
	created         bool
	passwordDone    bool
	passwordSkipped bool
	sshDone         bool
	recovery        *setupRecovery
}

func BeginOnboardingAccount(name string) (*OnboardingAccount, error) {
	ops := defaultInstallAccountOps()
	_, err := ops.lookup(name)
	var unknown user.UnknownUserError
	create := errors.As(err, &unknown)
	if err != nil && !create {
		return nil, err
	}
	ops.terminal, ops.password = nil, nil
	u, err := selectInstallationAccount(name, create, ops)
	if err != nil {
		return nil, err
	}
	return &OnboardingAccount{account: u, created: create}, nil
}

func (a *OnboardingAccount) Created() bool { return a != nil && a.created }

// Complete keeps a skipped password locked (useradd's default), never empty.
// Caller owns/clears the secret; errors contain no command output or secret.
func (a *OnboardingAccount) Complete(password []byte, install func() error) error {
	return a.complete(password, install, defaultInstallAccountOps(), setOnboardingPassword, lockOnboardingPassword)
}

func (a *OnboardingAccount) complete(password []byte, install func() error, ops installAccountOps, setPassword func(string, []byte) error, lockPassword func(string) error) error {
	if a == nil || a.account == nil {
		return errors.New("create or select your account first")
	}
	if err := ValidateOnboardingPassword(password); err != nil {
		return err
	}
	u, err := selectInstallationAccount(a.account.Username, false, ops)
	if err != nil {
		return err
	}
	if u.Uid != a.account.Uid || u.Gid != a.account.Gid || u.HomeDir != a.account.HomeDir {
		return errors.New("account changed during setup; no password changed")
	}
	if len(password) > 0 && !a.created {
		return errors.New("existing account passwords cannot be changed in onboarding")
	}
	if a.passwordDone && len(password) > 0 {
		return errors.New("password choice already completed; retry installation without a password")
	}
	if a.created && !a.passwordDone {
		if _, _, found, err := ops.existing(); err != nil || found {
			return errors.New("installation changed during setup; no password changed")
		}
		// Persist the no-replay boundary BEFORE the OS mutation. A crash in this
		// window requires an explicit OS password check, never a second mutation.
		if err := a.persistStage("password-started"); err != nil {
			return err
		}
		if len(password) > 0 {
			if err := setPassword(u.Username, password); err != nil {
				if saveErr := a.persistStage("password"); saveErr != nil {
					a.passwordDone = true
					return saveErr
				}
				return err
			}
		} else if err := lockPassword(u.Username); err != nil {
			if saveErr := a.persistStage("password"); saveErr != nil {
				a.passwordDone = true
				return saveErr
			}
			return err
		}
	}
	if !a.passwordDone {
		a.passwordSkipped = len(password) == 0
	}
	a.passwordDone = true
	if a.created && !a.sshDone {
		if err := a.persistStage("ssh"); err != nil {
			return err
		}
		return ErrOnboardingSSHRequired
	}
	if a.Stage() == "password" || a.Stage() == "password-started" {
		if err := a.persistStage("install"); err != nil {
			return err
		}
	}
	restore := selectAccountForInstallation(u)
	defer restore()
	return install()
}

func ValidateOnboardingPassword(password []byte) error {
	if len(password) > 1024 || bytes.ContainsAny(password, "\x00\r\n:") {
		return errors.New("password must be at most 1024 bytes and contain no colon, newline or NUL")
	}
	return nil
}

func lockOnboardingPassword(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Lock rather than delete the password, including after a failed install.
	if err := exec.CommandContext(ctx, "passwd", "--lock", "--", name).Run(); err != nil {
		return errors.New("could not disable password login; account retained, setup not completed")
	}
	return nil
}

func setOnboardingPassword(name string, password []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "chpasswd")
	return runOnboardingPassword(cmd, name, password)
}

func runOnboardingPassword(cmd *exec.Cmd, name string, password []byte) error {
	if !installAccountName.MatchString(name) || name == "root" || name == serviceAccountName {
		return errors.New("invalid password target")
	}
	if len(password) == 0 {
		return errors.New("empty password is not allowed; choose Skip instead")
	}
	if err := ValidateOnboardingPassword(password); err != nil {
		return err
	}
	payload := make([]byte, 0, len(name)+len(password)+2)
	payload = append(payload, name...)
	payload = append(payload, ':')
	payload = append(payload, password...)
	payload = append(payload, '\n')
	defer clear(payload)
	cmd.Stdin = bytes.NewReader(payload)
	// nil output goes to the null device, not the TUI or captured diagnostics.
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		return errors.New("password could not be set; account retained. Retry here or skip password setup")
	}
	return nil
}
