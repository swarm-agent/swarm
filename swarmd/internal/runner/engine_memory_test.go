package runner

import (
	"strconv"
	"strings"
	"testing"
)

// Purpose: landing checks must share 75% of host RAM by runner slots and check
// count, not agent admission defaults, while retaining all host CPUs. Testing
// SandboxEngine.sandboxConfig directly is the narrowest deterministic boundary
// for the config passed to NewManager, without requiring Docker or host RAM.
func TestEngineMemory(t *testing.T) {
	const gib = uint64(1 << 30)
	for _, tt := range []struct {
		name                   string
		ram                    uint64
		slots, checks, workers int
		memory                 uint64
	}{
		{"four slots", 43 * gib, 4, 2, 8, 43 * gib * 3 / 4 / 8},
		{"two slots", 43 * gib, 2, 2, 4, 43 * gib * 3 / 4 / 4},
		{"floor", 8 * gib, 4, 2, 8, 2 * gib},
		{"defaults", 43 * gib, 0, 0, 1, 43 * gib * 3 / 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			eng := &SandboxEngine{Slots: tt.slots}
			cfg := eng.sandboxConfig(tt.ram, 22, tt.checks, 123, 456, "landing")
			if cfg.Memory != strconv.FormatUint(tt.memory, 10) || cfg.MaxWorkerRuns != tt.workers {
				t.Fatalf("memory=%s workers=%d; want %d, %d", cfg.Memory, cfg.MaxWorkerRuns, tt.memory, tt.workers)
			}
			if cfg.CPUs != "22" {
				t.Fatalf("CPUs=%q; want whole-host 22", cfg.CPUs)
			}
		})
	}
}

// Purpose: host-memory detection must reject missing, malformed, zero, and
// overflowing MemTotal rather than silently sizing checks from a fake budget.
// The reader boundary proves parsing independently of the machine running tests.
func TestEngineMemoryMeminfo(t *testing.T) {
	for _, input := range []string{"", "MemTotal: nope kB\n", "MemTotal: 0 kB\n", "MemTotal: 18446744073709551615 kB\n", "MemTotal: 42 MB\n"} {
		if ram, err := runnerMemoryFromMeminfo(strings.NewReader(input)); err == nil || ram != 0 {
			t.Fatalf("input %q: ram=%d err=%v", input, ram, err)
		}
	}
	if ram, err := runnerMemoryFromMeminfo(strings.NewReader("MemFree: 1 kB\nMemTotal: 45088768 kB\n")); err != nil || ram != 43*(1<<30) {
		t.Fatalf("ram=%d err=%v", ram, err)
	}
}
