package sandbox

import (
	"fmt"
	"math"
	"strconv"
)

// Agent CPU quotas are opt-in; shares control contention without throttling idle hosts.
func resolveCPU(cfg *Config) error {
	if cfg.CPUs == "" {
		return nil
	}
	n, err := strconv.ParseFloat(cfg.CPUs, 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return fmt.Errorf("invalid sandbox_cpus %q", cfg.CPUs)
	}
	return nil
}
