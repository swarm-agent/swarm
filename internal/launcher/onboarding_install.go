package launcher

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// InstallForOnboarding runs the existing explicit installer as a bounded child.
// It never invokes a shell or executes a daemon-owned binary with root authority.
func (a *OnboardingAccount) InstallForOnboarding(ctx context.Context, artifact string) error {
	if _, err := a.SelectedAccount().resolve(defaultInstallAccountOps()); err != nil {
		return err
	}
	root, err := firstInstallArtifact(filepath.Join(artifact, "linux-amd64", "root", "swarm"))
	if err != nil {
		return err
	}
	if root == "" {
		return fmt.Errorf("invalid setup artifact")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(root, "linux-amd64", "root", "swarmsetup"), "--artifact-root", root, "--onboarding-install", "--service")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bin", "LANG=C.UTF-8"}
	cmd.Dir = string(filepath.Separator)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	// No inherited terminal or logs: actionable failure stage is retained by setup.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("installation deadline reached; partial work retained: %w", ctx.Err())
		}
		return fmt.Errorf("runtime/service installation failed; account and password retained: %w", err)
	}
	return nil
}

// SelectRecordedOnboardingInstall revalidates the durable identity inside the
// installer child itself; a username-only argv must not authorize a changed UID.
func SelectRecordedOnboardingInstall(artifact string) (func(), error) {
	a, err := ResumeOnboardingAccount(artifact)
	if err != nil {
		return nil, err
	}
	if a == nil || a.Stage() != "install" {
		return nil, fmt.Errorf("no matching installation-stage recovery")
	}
	return selectAccountForInstallation(a.account), nil
}
