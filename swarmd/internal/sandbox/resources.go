package sandbox

import "fmt"

// resolveResources applies no default caps: without an owner setting, the
// box admits every worker run and sandboxes have no memory cap. An explicit
// sandbox_memory or worker_max_concurrent_runs still applies.
func resolveResources(cfg *Config) error {
	if cfg.MaxWorkerRuns < 0 {
		return fmt.Errorf("worker_max_concurrent_runs must be non-negative")
	}
	return resolveCPU(cfg)
}

// MaxWorkerRuns is the owner's box-wide admission bound; 0 is no bound.
func (m *Manager) MaxWorkerRuns() int {
	if m == nil {
		return 0
	}
	return m.cfg.MaxWorkerRuns
}
