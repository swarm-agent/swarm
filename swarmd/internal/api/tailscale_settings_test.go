package api

import (
	"context"

	"swarm/packages/swarmd/internal/tailscale"
)

type tailscaleSettingsDetectorStub struct {
	snapshot tailscale.Snapshot
	err      error
	modes    []tailscale.RefreshMode
	invalid  int
}

func (d *tailscaleSettingsDetectorStub) Snapshot(_ context.Context, mode tailscale.RefreshMode) (tailscale.Snapshot, error) {
	d.modes = append(d.modes, mode)
	return d.snapshot, d.err
}

func (d *tailscaleSettingsDetectorStub) Invalidate() { d.invalid++ }

func (d *tailscaleSettingsDetectorStub) EffectiveTarget() string {
	return "http://127.0.0.1:8118"
}

func (d *tailscaleSettingsDetectorStub) RemediationCommand() string {
	return "tailscale serve --bg " + d.EffectiveTarget()
}
