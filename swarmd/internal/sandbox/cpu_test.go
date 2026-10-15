package sandbox

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Purpose: acquireCPUSlot, the Manager.Exec admission boundary, must bound
// concurrent processes and announce queueing without starting command budgets.
// Real CPU-consuming shell arithmetic exercises admission, not a sleep workload.
func TestCPUSlotsRealProcesses(t *testing.T) {
	const slots = 2
	m := &Manager{cfg: Config{CPUSlots: slots}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var active, peak, waited atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < slots+3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := m.acquireCPUSlot(ctx, func(p []byte) {
				if strings.Contains(string(p), "waiting for a CPU slot") {
					waited.Add(1)
				}
			})
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			commandCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			defer stop()
			if err := exec.CommandContext(commandCtx, "sh", "-c", "i=0; while [ $i -lt 1000000 ]; do i=$((i+1)); done").Run(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != slots || waited.Load() < 3 {
		t.Fatalf("peak=%d waiting=%d", peak.Load(), waited.Load())
	}
	if len(m.cpuSlots) != 0 {
		t.Fatal("slot leaked")
	}
}

// Purpose: CPUDefaults and container argv must isolate daemon Git from agent
// quotas while preserving confinement. This unit layer proves exact engine args.
func TestCPUIsolationArgs(t *testing.T) {
	cpus, slots := CPUDefaults(22, 8)
	if cpus != "2.0625" || slots != 8 {
		t.Fatalf("%s %d", cpus, slots)
	}
	m := newTestManager(t, ModeRequired, readyRunner(), Config{CPUs: "2", CPUSlots: 2, CgroupParent: "swarm-sandbox.slice"})
	root := t.TempDir()
	image, _, err := m.sandboxImage(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, daemon := range []bool{false, true} {
		name := scopeContainerName(Scope{Root: root, daemonGit: daemon})
		args := strings.Join(m.scopeRunArgs(name, root, "spec", []string{root}, SecretSetup{}, image, ""), " ")
		if strings.Contains(args, "--cpus 2") == daemon || strings.Contains(args, "--cgroup-parent swarm-sandbox.slice") == daemon {
			t.Fatal(args)
		}
		if !strings.Contains(args, "--cap-drop ALL") || !strings.Contains(args, "no-new-privileges") {
			t.Fatal(args)
		}
	}
}

// Purpose: cancellation must release queued admission without leaking a permit;
// resolveCPU must reject invalid overrides instead of disabling CPU protection.
func TestCPUSlotCancellationAndValidation(t *testing.T) {
	m := &Manager{cfg: Config{CPUSlots: 1}}
	release, _ := m.acquireCPUSlot(context.Background(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.acquireCPUSlot(ctx, nil); err == nil {
		t.Fatal("cancelled wait admitted")
	}
	release()
	for _, value := range []string{"-1", "NaN", "Inf", "garbage", "0"} {
		if err := resolveCPU(&Config{CPUs: value}); err == nil {
			t.Fatal(value)
		}
	}
}
