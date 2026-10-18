package runtime

import (
	"fmt"

	"swarm/packages/swarmd/internal/config"
)

// NewHeadless composes only the canonical backend and authenticated API
// transports. It never installs Desktop handlers, cookies or client listeners.
// Reject invalid callers before creating directories, locks or stores.
func NewHeadless(cfg config.Config) (*Daemon, error) {
	if cfg.DesktopPort != 0 {
		return nil, fmt.Errorf("Headless does not support a Desktop listener")
	}
	mode, err := config.HeadlessSandboxMode(cfg.SandboxMode)
	if err != nil {
		return nil, err
	}
	cfg.SandboxMode = mode
	if cfg.SandboxBackend == "" {
		cfg.SandboxBackend = "bubblewrap"
	}
	if cfg.SandboxBackend != "bubblewrap" && cfg.SandboxBackend != "docker" {
		return nil, fmt.Errorf("sandbox-backend must be bubblewrap or docker")
	}
	return newDaemon(cfg, nil)
}
