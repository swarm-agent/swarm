package launcher

import (
	"fmt"

	"swarm-refactor/swarmtui/pkg/startupconfig"
)

// OnboardingDesktopURL resolves only the installed system configuration after
// selected-user readiness succeeds. Privileged setup is not a user session:
// loading a runtime profile here would incorrectly require its HOME/XDG state.
// The selected-user readiness probe retains the full runtime/legacy checks.
func OnboardingDesktopURL() (string, error) {
	path, err := startupconfig.ResolvePath()
	if err != nil {
		return "", err
	}
	cfg, err := startupconfig.Load(path)
	if err != nil {
		return "", err
	}
	if !cfg.Exists {
		return "", fmt.Errorf("installed startup config is missing")
	}
	return DesktopURL(Profile{Startup: cfg, DesktopPort: cfg.DesktopPort}, 0), nil
}
