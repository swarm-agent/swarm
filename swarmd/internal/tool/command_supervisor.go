package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/appstorage"
)

const (
	commandTempMarker          = ".swarm-command-temp"
	defaultCommandMemoryBytes  = int64(2 << 30)
	defaultCommandPIDs         = int64(512)
	defaultCommandCPUQuotaUS   = int64(200000)
	defaultCommandCPUPeriodUS  = int64(100000)
	defaultCommandReserveBytes = uint64(256 << 20)
	defaultCommandPollInterval = 250 * time.Millisecond
	defaultCommandWaitDelay    = time.Second
	defaultCommandStaleAge     = 24 * time.Hour
)

type commandContainment struct {
	Mode           string `json:"mode"`
	State          string `json:"state"`
	Guarantee      string `json:"guarantee"`
	DegradedReason string `json:"degraded_reason,omitempty"`
}

type commandSupervisorResult struct {
	Err               error
	ExitCode          int
	TimedOut          bool
	TerminationReason string
	Containment       commandContainment
	TempGuarantee     string
	DiskGuarantee     string
	TempDir           string
}

type commandSupervisorConfig struct {
	Policy           string
	TempRoot         string
	MemoryBytes      int64
	PIDs             int64
	CPUQuotaUS       int64
	CPUPeriodUS      int64
	TempReserveBytes uint64
	WorkspaceReserve uint64
	PollInterval     time.Duration
	WaitDelay        time.Duration
	StaleAge         time.Duration
}

type platformCommandHandle interface {
	containment() commandContainment
	kill() error
	classifyTermination() string
	cleanup()
}

type commandSupervisor struct {
	config          commandSupervisorConfig
	now             func() time.Time
	preparePlatform func(*exec.Cmd, commandSupervisorConfig) (platformCommandHandle, error)
}

func newCommandSupervisor() *commandSupervisor {
	return &commandSupervisor{config: commandSupervisorConfigFromEnv(), now: time.Now}
}

func commandSupervisorConfigFromEnv() commandSupervisorConfig {
	policy := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMD_BASH_CONTAINMENT_POLICY")))
	if policy != "required" {
		// Installed service units set required explicitly; source builds and tests degrade visibly.
		policy = "degraded"
	}
	return commandSupervisorConfig{
		Policy:           policy,
		TempRoot:         strings.TrimSpace(os.Getenv("SWARMD_COMMAND_TEMP_ROOT")),
		MemoryBytes:      envInt64("SWARMD_BASH_MEMORY_MAX", defaultCommandMemoryBytes),
		PIDs:             envInt64("SWARMD_BASH_PIDS_MAX", defaultCommandPIDs),
		CPUQuotaUS:       envInt64("SWARMD_BASH_CPU_QUOTA_US", defaultCommandCPUQuotaUS),
		CPUPeriodUS:      envInt64("SWARMD_BASH_CPU_PERIOD_US", defaultCommandCPUPeriodUS),
		TempReserveBytes: envUint64("SWARMD_BASH_TEMP_RESERVE_BYTES", defaultCommandReserveBytes),
		WorkspaceReserve: envUint64("SWARMD_BASH_WORKSPACE_RESERVE_BYTES", defaultCommandReserveBytes),
		PollInterval:     envDurationMS("SWARMD_BASH_DISK_POLL_MS", defaultCommandPollInterval),
		WaitDelay:        envDurationMS("SWARMD_BASH_WAIT_DELAY_MS", defaultCommandWaitDelay),
		StaleAge:         envDurationMS("SWARMD_BASH_TEMP_STALE_MS", defaultCommandStaleAge),
	}
}

func (s *commandSupervisor) run(ctx context.Context, scope WorkspaceScope, command string, stdout, stderr interface{ Write([]byte) (int, error) }) commandSupervisorResult {
	result := commandSupervisorResult{ExitCode: -1, TempGuarantee: "private_directory_with_best_effort_reserve_monitor", DiskGuarantee: "best_effort_reserve_monitor"}
	if strings.TrimSpace(scope.PrimaryPath) == "" {
		result.Err = errors.New("bash workspace path is required")
		result.TerminationReason = "setup_error"
		return result
	}

	tempDir, err := s.createCommandTemp()
	if err != nil {
		result.Err = fmt.Errorf("create private command temp directory: %w", err)
		result.TerminationReason = "setup_error"
		return result
	}
	result.TempDir = tempDir
	defer s.cleanupCommandTemp(tempDir)

	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = scope.PrimaryPath
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = s.config.WaitDelay
	cmd.Env = commandEnvironment(os.Environ(), tempDir)
	prepareCommandForCancellation(cmd)

	prepare := s.preparePlatform
	if prepare == nil {
		prepare = preparePlatformCommand
	}
	handle, setupErr := prepare(cmd, s.config)
	if setupErr != nil {
		if s.config.Policy == "required" {
			result.Err = fmt.Errorf("mandatory command containment unavailable: %w", setupErr)
			result.TerminationReason = "containment_unavailable"
			result.Containment = commandContainment{Mode: "none", State: "failed_closed", Guarantee: "none", DegradedReason: setupErr.Error()}
			return result
		}
		handle = newFallbackPlatformCommand(cmd, setupErr)
	}
	result.Containment = handle.containment()
	defer handle.cleanup()

	if err := cmd.Start(); err != nil {
		result.Err = err
		result.TerminationReason = "start_error"
		return result
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	killed := false
	for {
		select {
		case err := <-waitCh:
			result.Err = err
			result.ExitCode = commandExitCode(err)
			if result.TerminationReason == "" {
				if err != nil {
					result.TerminationReason = handle.classifyTermination()
				}
				if result.TerminationReason == "" {
					if err == nil {
						result.TerminationReason = "completed"
					} else {
						result.TerminationReason = "exit_error"
					}
				}
			}
			return result
		case <-ctx.Done():
			if !killed {
				killed = true
				result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
				if result.TimedOut {
					result.TerminationReason = "timeout"
				} else {
					result.TerminationReason = "cancelled"
				}
				_ = handle.kill()
			}
		case <-ticker.C:
			if !killed {
				reason, checkErr := s.reserveViolation(scope.PrimaryPath, tempDir)
				if checkErr != nil {
					// A transient statfs failure cannot establish a reserve violation; retry on the next poll.
					continue
				}
				if reason != "" {
					killed = true
					result.TerminationReason = reason
					_ = handle.kill()
				}
			}
		}
	}
}

func (s *commandSupervisor) reserveViolation(workspace, tempDir string) (string, error) {
	if s.config.TempReserveBytes > 0 {
		free, err := filesystemFreeBytes(tempDir)
		if err != nil {
			return "", err
		}
		if free < s.config.TempReserveBytes {
			return "temp_disk_reserve", nil
		}
	}
	if s.config.WorkspaceReserve > 0 {
		free, err := filesystemFreeBytes(workspace)
		if err != nil {
			return "", err
		}
		if free < s.config.WorkspaceReserve {
			return "workspace_disk_reserve", nil
		}
	}
	return "", nil
}

func (s *commandSupervisor) createCommandTemp() (string, error) {
	root := s.config.TempRoot
	var err error
	if root == "" {
		root, err = appstorage.CacheDir("command-tmp")
		if err != nil {
			return "", err
		}
	} else if !filepath.IsAbs(root) {
		return "", errors.New("SWARMD_COMMAND_TEMP_ROOT must be absolute")
	} else if err := ensureCommandTempRoot(root); err != nil {
		return "", err
	}
	_ = s.sweepStaleCommandTemps(root)
	dir, err := os.MkdirTemp(root, "command-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	marker := []byte(strconv.FormatInt(s.now().UnixNano(), 10) + "\n")
	if err := os.WriteFile(filepath.Join(dir, commandTempMarker), marker, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func ensureCommandTempRoot(root string) error {
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("command temp root must be a directory, not a symlink")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("command temp root permissions are %04o, require 0700", info.Mode().Perm())
	}
	return nil
}

func (s *commandSupervisor) cleanupCommandTemp(dir string) {
	if commandTempOwned(dir) {
		_ = os.RemoveAll(dir)
	}
}

func (s *commandSupervisor) sweepStaleCommandTemps(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	cutoff := s.now().Add(-s.config.StaleAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "command-") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) && commandTempOwned(dir) {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

func commandTempOwned(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, commandTempMarker))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0
}

func commandEnvironment(env []string, tempDir string) []string {
	out := make([]string, 0, len(env)+3)
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if name == "TMPDIR" || name == "TMP" || name == "TEMP" {
			continue
		}
		out = append(out, item)
	}
	return append(out, "TMPDIR="+tempDir, "TMP="+tempDir, "TEMP="+tempDir)
}

type fallbackPlatformCommand struct {
	cmd    *exec.Cmd
	reason error
}

func newFallbackPlatformCommand(cmd *exec.Cmd, reason error) platformCommandHandle {
	return &fallbackPlatformCommand{cmd: cmd, reason: reason}
}

func (h *fallbackPlatformCommand) containment() commandContainment {
	reason := "platform cgroup containment is unavailable"
	if h.reason != nil {
		reason = h.reason.Error()
	}
	return commandContainment{Mode: "process_group", State: "degraded", Guarantee: "best_effort_process_tree", DegradedReason: reason}
}

func (h *fallbackPlatformCommand) kill() error                 { return platformKillCommand(h.cmd) }
func (h *fallbackPlatformCommand) classifyTermination() string { return "" }
func (h *fallbackPlatformCommand) cleanup()                    {}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envUint64(name string, fallback uint64) uint64 {
	value, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func envDurationMS(name string, fallback time.Duration) time.Duration {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
