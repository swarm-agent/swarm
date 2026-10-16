package sandbox

import (
	"fmt"
	"runtime"
	"strconv"
)

const gib = uint64(1 << 30)

// ResourceDefaults reserves a quarter of host RAM for the OS and daemon. Each
// admitted worker has a 3 GiB minimum admission budget, independent of CPUs.
// Explicit install settings may override worker admission or container memory.
func ResourceDefaults(_ int, memory uint64) (workers int, bytes uint64) {
	budget := memory / 4 * 3
	workers = max(1, int(budget/(3*gib)))
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
