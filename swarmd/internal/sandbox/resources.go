package sandbox

import (
	"fmt"
	"runtime"
	"strconv"
)

const gib = uint64(1 << 30)

// ResourceDefaults reserves a quarter of host RAM for the OS and daemon. Each
// admitted worker has at least a 3 GiB budget; CPU concurrency is capped at one
// worker per two logical CPUs. Explicit install settings may override either.
func ResourceDefaults(cpus int, memory uint64) (workers int, bytes uint64) {
	workers = cpus / 2
	if workers < 1 {
		workers = 1
	}
	budget := memory / 4 * 3
	byRAM := int(budget / (3 * gib))
	if byRAM < 1 {
		byRAM = 1
	}
	if workers > byRAM {
		workers = byRAM
	}
	return workers, budget / uint64(workers)
}

func hostResources() (int, uint64, error) {
	ram, err := hostMemory()
	if err != nil {
		return 0, 0, fmt.Errorf("sandbox host memory detection: %w; configure sandbox_memory and worker_max_concurrent_runs", err)
	}
	return runtime.NumCPU(), ram, nil
}

func resolveResources(cfg *Config) error {
	if cfg.MaxWorkerRuns < 0 {
		return fmt.Errorf("worker_max_concurrent_runs must be non-negative")
	}
	if cfg.Memory != "" && cfg.MaxWorkerRuns > 0 {
		return resolveCPU(cfg)
	}
	cpus, ram, err := hostResources()
	if err != nil {
		return err
	}
	workers, _ := ResourceDefaults(cpus, ram)
	if cfg.MaxWorkerRuns <= 0 {
		cfg.MaxWorkerRuns = workers
	}
	if cfg.Memory == "" {
		cfg.Memory = strconv.FormatUint(ram/4*3/uint64(cfg.MaxWorkerRuns), 10)
	}
	return resolveCPU(cfg)
}

// MaxWorkerRuns is a box-wide admission bound, independent of account quotas.
func (m *Manager) MaxWorkerRuns() int {
	if m == nil {
		return 0
	}
	return m.cfg.MaxWorkerRuns
}
