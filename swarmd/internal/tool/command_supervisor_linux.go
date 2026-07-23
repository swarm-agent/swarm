//go:build linux

package tool

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type cgroupCommandHandle struct {
	cmd         *exec.Cmd
	path        string
	dir         *os.File
	memoryOOM   uint64
	pidsMax     uint64
	cpuThrottle uint64
}

func preparePlatformCommand(cmd *exec.Cmd, cfg commandSupervisorConfig) (platformCommandHandle, error) {
	root, err := delegatedCgroupRoot()
	if err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(root, "swarm-command-")
	if err != nil {
		return nil, fmt.Errorf("create child cgroup: %w", err)
	}
	cleanup := func() { _ = os.Remove(path) }
	if err := writeCgroupValue(path, "memory.max", strconv.FormatInt(cfg.MemoryBytes, 10)); err != nil {
		cleanup()
		return nil, err
	}
	if err := writeCgroupValue(path, "pids.max", strconv.FormatInt(cfg.PIDs, 10)); err != nil {
		cleanup()
		return nil, err
	}
	if err := writeCgroupValue(path, "cpu.max", fmt.Sprintf("%d %d", cfg.CPUQuotaUS, cfg.CPUPeriodUS)); err != nil {
		cleanup()
		return nil, err
	}
	dir, err := os.Open(path)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open child cgroup: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(dir.Fd())
	return &cgroupCommandHandle{
		cmd:         cmd,
		path:        path,
		dir:         dir,
		memoryOOM:   readCgroupEvent(path, "memory.events", "oom_kill"),
		pidsMax:     readCgroupEvent(path, "pids.events", "max"),
		cpuThrottle: readCgroupEvent(path, "cpu.stat", "nr_throttled"),
	}, nil
}

func delegatedCgroupRoot() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read current cgroup: %w", err)
	}
	var relative string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			relative = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if relative == "" {
		return "", errors.New("cgroup v2 unified hierarchy is unavailable")
	}
	root := filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+relative))
	enabled, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return "", fmt.Errorf("read delegated cgroup controller state: %w", err)
	}
	active := " " + strings.Join(strings.Fields(string(enabled)), " ") + " "
	for _, controller := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(active, " "+controller+" ") {
			return "", fmt.Errorf("required cgroup controller %s is not enabled for child cgroups", controller)
		}
	}
	return root, nil
}

func writeCgroupValue(path, name, value string) error {
	if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0o600); err != nil {
		return fmt.Errorf("configure cgroup %s: %w", name, err)
	}
	return nil
}

func (h *cgroupCommandHandle) containment() commandContainment {
	return commandContainment{Mode: "cgroup_v2", State: "contained", Guarantee: "hard_cpu_memory_pids_and_unit_kill"}
}

func (h *cgroupCommandHandle) kill() error {
	if err := os.WriteFile(filepath.Join(h.path, "cgroup.kill"), []byte("1"), 0o600); err == nil {
		return nil
	}
	return platformKillCommand(h.cmd)
}

func (h *cgroupCommandHandle) classifyTermination() string {
	memoryOOM := readCgroupEvent(h.path, "memory.events", "oom_kill")
	pidsMax := readCgroupEvent(h.path, "pids.events", "max")
	cpuThrottle := readCgroupEvent(h.path, "cpu.stat", "nr_throttled")
	if memoryOOM > h.memoryOOM {
		return "memory_limit"
	}
	if pidsMax > h.pidsMax {
		return "pids_limit"
	}
	if cpuThrottle > h.cpuThrottle {
		return "cpu_pressure"
	}
	return ""
}

func (h *cgroupCommandHandle) cleanup() {
	_ = h.kill()
	if h.dir != nil {
		_ = h.dir.Close()
	}
	_ = os.Remove(h.path)
}

func readCgroupEvent(path, file, key string) uint64 {
	input, err := os.Open(filepath.Join(path, file))
	if err != nil {
		return 0
	}
	defer input.Close()
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == key {
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			return value
		}
	}
	return 0
}

func platformKillCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	return cmd.Process.Kill()
}

func filesystemFreeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
