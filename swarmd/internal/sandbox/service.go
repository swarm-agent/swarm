package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	strictProbeTimeout = 6 * time.Second
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Status struct {
	Enabled      bool     `json:"enabled"`
	UpdatedAt    int64    `json:"updated_at"`
	Ready        bool     `json:"ready"`
	Summary      string   `json:"summary"`
	Checks       []Check  `json:"checks"`
	Remediation  []string `json:"remediation"`
	SetupCommand string   `json:"setup_command"`
}

type ErrNotReady struct {
	Status Status
}

func (e *ErrNotReady) Error() string {
	if e == nil {
		return "sandbox prerequisites are not ready"
	}
	summary := strings.TrimSpace(e.Status.Summary)
	if summary == "" {
		summary = "sandbox prerequisites are not ready"
	}
	return summary
}

type Service struct {
	store  *pebblestore.SandboxStore
	events *pebblestore.EventLog
}

func NewService(store *pebblestore.SandboxStore, events *pebblestore.EventLog) *Service {
	return &Service{store: store, events: events}
}

func (s *Service) IsEnabled() (bool, error) {
	state, _, err := s.store.GetGlobalState()
	if err != nil {
		return false, fmt.Errorf("read sandbox state: %w", err)
	}
	return state.Enabled, nil
}

func (s *Service) GetStatus() (Status, error) {
	state, _, err := s.store.GetGlobalState()
	if err != nil {
		return Status{}, fmt.Errorf("read sandbox state: %w", err)
	}
	status := preflight(state.Enabled, state.UpdatedAt)
	return status, nil
}

func (s *Service) Preflight() (Status, error) {
	state, _, err := s.store.GetGlobalState()
	if err != nil {
		return Status{}, fmt.Errorf("read sandbox state: %w", err)
	}
	return preflight(state.Enabled, state.UpdatedAt), nil
}

func (s *Service) SetEnabled(enabled bool) (Status, *pebblestore.EventEnvelope, error) {
	current, _, err := s.store.GetGlobalState()
	if err != nil {
		return Status{}, nil, fmt.Errorf("read sandbox state: %w", err)
	}
	if enabled {
		status := preflight(current.Enabled, current.UpdatedAt)
		if !status.Ready {
			return status, nil, &ErrNotReady{Status: status}
		}
	}

	record, err := s.store.SetGlobalState(enabled)
	if err != nil {
		return Status{}, nil, fmt.Errorf("persist sandbox state: %w", err)
	}

	status := preflight(record.Enabled, record.UpdatedAt)
	if s.events == nil {
		return status, nil, nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return Status{}, nil, fmt.Errorf("marshal sandbox event payload: %w", err)
	}
	env, err := s.events.Append("system:sandbox", "sandbox.state.updated", "global", payload, "", "")
	if err != nil {
		return Status{}, nil, err
	}
	return status, &env, nil
}

func SetupCommandText() string {
	lines := remediationLines()
	var b strings.Builder
	b.WriteString("# bash/zsh\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n# fish\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nIf your terminal captures mouse input, hold Shift while selecting text to copy.\n")
	return strings.TrimSpace(b.String())
}

func remediationLines() []string {
	return []string{
		"# Install bubblewrap (choose one package manager command):",
		"sudo apt-get update && sudo apt-get install -y bubblewrap",
		"sudo dnf install -y bubblewrap",
		"sudo pacman -Sy --noconfirm bubblewrap",
		"",
		"# Verify strict sandbox networking support:",
		"bwrap --new-session --die-with-parent --unshare-pid --unshare-net --ro-bind / / --proc /proc --dev /dev -- /bin/sh -lc 'true'",
		"",
		"# Re-check in Swarm:",
		"/sandbox",
	}
}

func preflight(enabled bool, updatedAt int64) Status {
	checks := make([]Check, 0, 4)
	remediation := remediationLines()
	setupCommand := SetupCommandText()

	bwrapPath, lookErr := exec.LookPath("bwrap")
	if lookErr != nil {
		checks = append(checks, Check{
			Name:   "bwrap_binary",
			OK:     false,
			Detail: "bubblewrap (bwrap) was not found in PATH",
		})
		return Status{
			Enabled:      enabled,
			UpdatedAt:    updatedAt,
			Ready:        false,
			Summary:      "sandbox unavailable: bubblewrap (bwrap) is not installed",
			Checks:       checks,
			Remediation:  remediation,
			SetupCommand: setupCommand,
		}
	}
	checks = append(checks, Check{
		Name:   "bwrap_binary",
		OK:     true,
		Detail: bwrapPath,
	})

	probeArgs := []string{
		"--new-session",
		"--die-with-parent",
		"--unshare-pid",
		"--unshare-net",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		"--", "/bin/sh", "-lc", "true",
	}

	ctxTimeout := strictProbeTimeout
	ctx := contextWithTimeout(ctxTimeout)
	defer ctx.cancel()

	cmd := exec.CommandContext(ctx.ctx, bwrapPath, probeArgs...)
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	probeErr := cmd.Run()
	if probeErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(probeErr.Error())
		}
		checks = append(checks, Check{
			Name:   "strict_network_namespace",
			OK:     false,
			Detail: detail,
		})
		return Status{
			Enabled:      enabled,
			UpdatedAt:    updatedAt,
			Ready:        false,
			Summary:      "sandbox unavailable: strict network namespace probe failed",
			Checks:       checks,
			Remediation:  remediation,
			SetupCommand: setupCommand,
		}
	}
	checks = append(checks, Check{
		Name:   "strict_network_namespace",
		OK:     true,
		Detail: "bwrap --unshare-net probe passed",
	})

	return Status{
		Enabled:      enabled,
		UpdatedAt:    updatedAt,
		Ready:        true,
		Summary:      "sandbox prerequisites ready",
		Checks:       checks,
		Remediation:  remediation,
		SetupCommand: setupCommand,
	}
}

type timedContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func contextWithTimeout(timeout time.Duration) timedContext {
	if timeout <= 0 {
		timeout = strictProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return timedContext{ctx: ctx, cancel: cancel}
}
