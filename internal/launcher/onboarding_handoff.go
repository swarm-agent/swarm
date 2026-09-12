package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"swarm-refactor/swarmtui/internal/client"
)

// SelectedAccount is the explicit OS boundary between privileged setup and the
// application. Never derive it from HOME, SUDO_USER, CWD or a browser request.
type SelectedAccount struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
	GID      string `json:"gid"`
	Home     string `json:"home"`
}

func selectedAccount(u *user.User) SelectedAccount {
	return SelectedAccount{u.Username, u.Uid, u.Gid, u.HomeDir}
}

func (a SelectedAccount) resolve(ops installAccountOps) (*user.User, error) {
	u, err := selectInstallationAccount(a.Username, false, ops)
	if err != nil {
		return nil, err
	}
	if selectedAccount(u) != a {
		return nil, errors.New("selected account changed; refusing setup handoff")
	}
	return u, nil
}

func selectedUserCommand(ctx context.Context, account SelectedAccount, path string, args, environ []string, ops installAccountOps, groups func(*user.User) ([]string, error)) (*exec.Cmd, error) {
	u, err := account.resolve(ops)
	if err != nil {
		return nil, err
	}
	ids, err := groups(u)
	if err != nil {
		return nil, fmt.Errorf("resolve selected account groups: %w", err)
	}
	uid, e1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, e2 := strconv.ParseUint(u.Gid, 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || gid == 0 {
		return nil, errors.New("invalid non-root handoff identity")
	}
	supplemental := []uint32{}
	for _, id := range ids {
		g, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return nil, errors.New("invalid selected account group")
		}
		supplemental = append(supplemental, uint32(g))
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = u.HomeDir
	cmd.Env = []string{"HOME=" + u.HomeDir, "USER=" + u.Username, "LOGNAME=" + u.Username, "PWD=" + u.HomeDir, "PATH=/usr/local/bin:/usr/bin:/bin", "SHELL=/bin/bash"}
	// Carry terminal rendering preferences only. In particular no root auth, XDG,
	// SSH agent, loader, Git, Swarm path overrides or sudo context crosses here.
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TERM" || key == "COLORTERM" || key == "LANG" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: supplemental}}
	return cmd, nil
}

func (a *OnboardingAccount) SelectedAccount() SelectedAccount { return selectedAccount(a.account) }

func (a *OnboardingAccount) RunApplication(ctx context.Context, desktop bool, probe bool) error {
	args := []string{"run"}
	if desktop {
		args = []string{"open"}
	}
	if probe {
		args = []string{"setup-ready"}
		if desktop {
			args = append(args, "--desktop")
		}
	}
	cmd, err := selectedUserCommand(ctx, a.SelectedAccount(), filepath.Join(systemBinDir(), "swarm"), args, os.Environ(), defaultInstallAccountOps(), func(u *user.User) ([]string, error) { return u.GroupIds() })
	if err != nil {
		return err
	}
	if !probe {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("selected-user application %s failed: %w", args[0], err)
	}
	return nil
}

// ProbeSetupApplication runs only after dropping to the selected account. Unix
// peer authentication must admit onboarding before setup is allowed to close.
// Identity creation is deliberately left to the application onboarding screen.
func ProbeSetupApplication(ctx context.Context, profile Profile, desktop bool) error {
	if os.Geteuid() == 0 {
		return errors.New("setup readiness requires the selected non-root account")
	}
	socket := LocalTransportSocketPath(profile)
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return errors.New("local authenticated transport is not ready")
	}
	// Pin the transport so disappearance of the socket cannot fall back to HTTP.
	api := client.NewLocalTransport(socket)
	if err := probeSetupIdentity(ctx, api); err != nil {
		return err
	}
	if desktop {
		return probeSetupDesktop(ctx, profile)
	}
	return nil
}

func probeSetupIdentity(ctx context.Context, api *client.API) error {
	status, err := api.GetOnboardingStatus(ctx)
	if err != nil {
		return err
	}
	if !status.OK {
		return errors.New("application onboarding is not ready")
	}
	if status.Identity.Bootstrapped {
		if err := api.EnsureLocalAuth(ctx); err != nil {
			return err
		}
	}
	return nil
}

func probeSetupDesktop(ctx context.Context, profile Profile) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DesktopURL(profile, 0), nil)
	if err != nil {
		return err
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("Desktop is not ready")
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return err
}

// PrepareApplication separates installation from readiness so a deliberate retry
// cannot reinstall a successfully activated runtime or replay account choices.
func (a *OnboardingAccount) PrepareApplication(install, ready func() error) error {
	return prepareSetupApplication(a.Stage(), install, ready, a.MarkStage)
}

func prepareSetupApplication(stage string, install, ready func() error, mark func(string) error) error {
	switch stage {
	case "password", "install":
		if err := install(); err != nil {
			return err
		}
		if err := mark("readiness"); err != nil {
			return err
		}
	case "readiness", "handoff":
	default:
		return errors.New("account choices must finish before application startup")
	}
	if err := ready(); err != nil {
		return err
	}
	return mark("handoff")
}

func (a *OnboardingAccount) WaitApplicationReady(ctx context.Context, desktop bool, progress func(string)) error {
	return waitSetupReadiness(ctx, time.Second, func(ctx context.Context) error {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return a.RunApplication(attempt, desktop, true)
	}, func() string { return installedServiceActiveState() }, progress)
}

func waitSetupReadiness(ctx context.Context, interval time.Duration, probe func(context.Context) error, state func() string, progress func(string)) error {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("application readiness deadline reached (last probe: %v): %w", last, err)
		}
		last = probe(ctx)
		if last == nil {
			return nil
		}
		current := state()
		message := "Waiting for authenticated application readiness (service=" + current + ")"
		if progress != nil {
			progress(message)
		}
		if current == "failed" || current == "inactive" || current == "not-installed" {
			return fmt.Errorf("application not ready: service=%s; retry setup deliberately after correcting the service: %w", current, last)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
