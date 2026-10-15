package sandbox

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strconv"
)

// CPUDefaults bounds executing commands separately from model-waiting jobs.
// The two-CPU floor is not an aggregate reservation; slots and cgroup weights
// provide contention control even when worker admission exceeds CPU capacity.
func CPUDefaults(cpus, workers int) (string, int) {
	if workers < 1 {
		workers = 1
	}
	return strconv.FormatFloat(math.Max(2, float64(cpus)*0.75/float64(workers)), 'f', -1, 64), max(1, cpus*3/8)
}

func resolveCPU(cfg *Config) error {
	if cfg.CPUSlots < 0 {
		return fmt.Errorf("sandbox_cpu_slots must be non-negative")
	}
	cpus, slots := CPUDefaults(runtime.NumCPU(), cfg.MaxWorkerRuns)
	if cfg.CPUs == "" {
		cfg.CPUs = cpus
	}
	n, err := strconv.ParseFloat(cfg.CPUs, 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return fmt.Errorf("invalid sandbox_cpus %q", cfg.CPUs)
	}
	if cfg.CPUSlots == 0 {
		cfg.CPUSlots = slots
	}
	return nil
}

// Waiting uses the run cancellation context, not ExecRequest.Timeout. The
// supervisor starts the command timeout only after admission and acquisition.
func (m *Manager) acquireCPUSlot(ctx context.Context, output func([]byte)) (func(), error) {
	m.mu.Lock()
	if m.cpuSlots == nil {
		slots := m.cfg.CPUSlots
		if slots < 1 {
			_, slots = CPUDefaults(runtime.NumCPU(), m.cfg.MaxWorkerRuns)
		}
		m.cpuSlots = make(chan struct{}, slots)
	}
	slots := m.cpuSlots
	m.mu.Unlock()
	select {
	case slots <- struct{}{}:
	default:
		if output != nil {
			output([]byte("waiting for a CPU slot\n"))
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return func() { <-slots }, nil
}
