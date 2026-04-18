package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type perfMatrixWorkload struct {
	name       string
	callCount  int
	workers    int
	timeout    time.Duration
	minCallsPS float64
	minSpeedup float64
	maxP95     time.Duration
	requiresRG bool
	buildCalls func(tb testing.TB, workspace string, callCount int) []Call
}

type perfMatrixSummary struct {
	Calls        int
	ErrorCount   int
	Elapsed      time.Duration
	CallsPerSec  float64
	LatencyP50   time.Duration
	LatencyP95   time.Duration
	WorkerCount  int
	WorkloadName string
}

func TestToolRuntimeParallelPerformanceMatrix(t *testing.T) {
	workloads := []perfMatrixWorkload{
		{
			name:       "read",
			callCount:  180,
			workers:    12,
			timeout:    30 * time.Second,
			minCallsPS: 5000,
			maxP95:     450 * time.Millisecond,
			buildCalls: buildReadPerfCalls,
		},
		{
			name:       "write",
			callCount:  180,
			workers:    12,
			timeout:    30 * time.Second,
			minCallsPS: 4000,
			maxP95:     900 * time.Millisecond,
			buildCalls: buildWritePerfCalls,
		},
		{
			name:       "edit",
			callCount:  140,
			workers:    12,
			timeout:    35 * time.Second,
			minCallsPS: 1200,
			minSpeedup: 1.03,
			maxP95:     1300 * time.Millisecond,
			buildCalls: buildEditPerfCalls,
		},
		{
			name:       "bash",
			callCount:  120,
			workers:    8,
			timeout:    40 * time.Second,
			minCallsPS: 80,
			minSpeedup: 1.05,
			maxP95:     2 * time.Second,
			buildCalls: buildBashPerfCalls,
		},
		{
			name:       "glob",
			callCount:  64,
			workers:    12,
			timeout:    40 * time.Second,
			minCallsPS: 80,
			minSpeedup: 1.05,
			maxP95:     3 * time.Second,
			requiresRG: true,
			buildCalls: buildGlobPerfCalls,
		},
		{
			name:       "grep",
			callCount:  64,
			workers:    12,
			timeout:    40 * time.Second,
			minCallsPS: 80,
			minSpeedup: 1.05,
			maxP95:     3 * time.Second,
			requiresRG: true,
			buildCalls: buildGrepPerfCalls,
		},
		{
			name:       "list",
			callCount:  72,
			workers:    10,
			timeout:    35 * time.Second,
			minCallsPS: 100,
			minSpeedup: 1.03,
			maxP95:     3 * time.Second,
			buildCalls: buildListPerfCalls,
		},
	}

	for _, workload := range workloads {
		workload := workload
		t.Run(workload.name, func(t *testing.T) {
			if workload.requiresRG {
				if _, err := exec.LookPath("rg"); err != nil {
					t.Skip("ripgrep (rg) is required for glob/grep perf matrix")
				}
			}

			workspace := t.TempDir()
			calls := workload.buildCalls(t, workspace, workload.callCount)
			if len(calls) == 0 {
				t.Fatalf("workload %s produced no calls", workload.name)
			}

			serial := runPerfMatrixBatch(t, workload.name, workspace, 1, calls, workload.timeout)
			parallel := runPerfMatrixBatch(t, workload.name, workspace, workload.workers, calls, workload.timeout)

			t.Logf("tool perf matrix: workload=%s calls=%d serial(calls/sec=%.2f p50=%s p95=%s elapsed=%s) parallel(workers=%d calls/sec=%.2f p50=%s p95=%s elapsed=%s)",
				workload.name,
				workload.callCount,
				serial.CallsPerSec,
				serial.LatencyP50,
				serial.LatencyP95,
				serial.Elapsed,
				workload.workers,
				parallel.CallsPerSec,
				parallel.LatencyP50,
				parallel.LatencyP95,
				parallel.Elapsed,
			)

			if serial.ErrorCount > 0 {
				t.Fatalf("serial run for %s returned %d errors", workload.name, serial.ErrorCount)
			}
			if parallel.ErrorCount > 0 {
				t.Fatalf("parallel run for %s returned %d errors", workload.name, parallel.ErrorCount)
			}
			if parallel.LatencyP95 > workload.maxP95 {
				t.Fatalf("parallel p95 latency for %s exceeded budget: got %s want <= %s", workload.name, parallel.LatencyP95, workload.maxP95)
			}
			if workload.minCallsPS > 0 && parallel.CallsPerSec < workload.minCallsPS {
				t.Fatalf("parallel throughput for %s below floor: got %.2f calls/sec want >= %.2f", workload.name, parallel.CallsPerSec, workload.minCallsPS)
			}

			requiredThroughput := serial.CallsPerSec * workload.minSpeedup
			if requiredThroughput > 0 && parallel.CallsPerSec < requiredThroughput {
				t.Fatalf("parallel throughput for %s below speedup floor: got %.2f calls/sec want >= %.2f (serial %.2f * %.2fx)",
					workload.name, parallel.CallsPerSec, requiredThroughput, serial.CallsPerSec, workload.minSpeedup)
			}
		})
	}
}

func BenchmarkToolRuntimeExecuteBatchMatrix(b *testing.B) {
	workloads := []perfMatrixWorkload{
		{
			name:       "read",
			callCount:  60,
			timeout:    25 * time.Second,
			buildCalls: buildReadPerfCalls,
		},
		{
			name:       "write",
			callCount:  60,
			timeout:    25 * time.Second,
			buildCalls: buildWritePerfCalls,
		},
		{
			name:       "edit",
			callCount:  48,
			timeout:    30 * time.Second,
			buildCalls: buildEditPerfCalls,
		},
		{
			name:       "bash",
			callCount:  48,
			timeout:    35 * time.Second,
			buildCalls: buildBashPerfCalls,
		},
		{
			name:       "glob",
			callCount:  24,
			timeout:    35 * time.Second,
			requiresRG: true,
			buildCalls: buildGlobPerfCalls,
		},
		{
			name:       "grep",
			callCount:  24,
			timeout:    35 * time.Second,
			requiresRG: true,
			buildCalls: buildGrepPerfCalls,
		},
		{
			name:       "list",
			callCount:  24,
			timeout:    30 * time.Second,
			buildCalls: buildListPerfCalls,
		},
	}

	workerSets := []int{1, 8, 16}
	for _, workload := range workloads {
		workload := workload
		b.Run(workload.name, func(b *testing.B) {
			if workload.requiresRG {
				if _, err := exec.LookPath("rg"); err != nil {
					b.Skip("ripgrep (rg) is required for glob/grep benchmarks")
				}
			}
			workspace := b.TempDir()
			calls := workload.buildCalls(b, workspace, workload.callCount)
			if len(calls) == 0 {
				b.Fatalf("workload %s produced no calls", workload.name)
			}

			for _, workers := range workerSets {
				workers := workers
				b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
					rt := NewRuntime(workers)
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						ctx, cancel := context.WithTimeout(context.Background(), workload.timeout)
						results := rt.ExecuteBatch(ctx, workspace, calls)
						cancel()
						if countToolErrors(results) > 0 {
							b.Fatalf("tool execution returned errors")
						}
					}
				})
			}
		})
	}
}

func runPerfMatrixBatch(tb testing.TB, workloadName, workspace string, workers int, calls []Call, timeout time.Duration) perfMatrixSummary {
	tb.Helper()

	rt := NewRuntime(workers)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	startedAt := time.Now()
	results := rt.ExecuteBatch(ctx, workspace, calls)
	elapsed := time.Since(startedAt)

	latencies := make([]time.Duration, 0, len(results))
	for _, result := range results {
		latencies = append(latencies, time.Duration(result.DurationMS)*time.Millisecond)
	}

	callsPerSec := 0.0
	if elapsed > 0 {
		callsPerSec = float64(len(results)) / elapsed.Seconds()
	}

	return perfMatrixSummary{
		Calls:        len(results),
		ErrorCount:   countToolErrors(results),
		Elapsed:      elapsed,
		CallsPerSec:  callsPerSec,
		LatencyP50:   percentileDuration(latencies, 0.50),
		LatencyP95:   percentileDuration(latencies, 0.95),
		WorkerCount:  workers,
		WorkloadName: workloadName,
	}
}

func countToolErrors(results []Result) int {
	var count int
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" {
			count++
		}
	}
	return count
}

func buildReadPerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()

	fixtureDir := filepath.Join(workspace, "read_fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		tb.Fatalf("create read fixture dir: %v", err)
	}

	fileCount := callCount / 3
	if fileCount < 48 {
		fileCount = 48
	}
	readPayload := strings.Repeat("READ_PERF_LINE_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\n", 120)
	for i := 0; i < fileCount; i++ {
		path := filepath.Join(fixtureDir, fmt.Sprintf("file_%03d.txt", i))
		if err := os.WriteFile(path, []byte(readPayload), 0o644); err != nil {
			tb.Fatalf("write read fixture file: %v", err)
		}
	}

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		relPath := filepath.Join("read_fixture", fmt.Sprintf("file_%03d.txt", i%fileCount))
		calls = append(calls, Call{
			CallID:    fmt.Sprintf("read-%03d", i),
			Name:      "read",
			Arguments: mustArgsJSON(tb, map[string]any{"path": relPath, "max_lines": 200}),
		})
	}
	return calls
}

func buildWritePerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()

	if err := os.MkdirAll(filepath.Join(workspace, "write_fixture"), 0o755); err != nil {
		tb.Fatalf("create write fixture dir: %v", err)
	}
	content := strings.Repeat("write-perf-payload-", 64)

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		relPath := filepath.Join("write_fixture", fmt.Sprintf("out_%04d.txt", i))
		calls = append(calls, Call{
			CallID: fmt.Sprintf("write-%03d", i),
			Name:   "write",
			Arguments: mustArgsJSON(tb, map[string]any{
				"path":    relPath,
				"content": content,
			}),
		})
	}
	return calls
}

func buildEditPerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()

	fixtureDir := filepath.Join(workspace, "edit_fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		tb.Fatalf("create edit fixture dir: %v", err)
	}

	payload := "line one\nTOKEN\nline three\n"
	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		relPath := filepath.Join("edit_fixture", fmt.Sprintf("edit_%04d.txt", i))
		absPath := filepath.Join(workspace, relPath)
		if err := os.WriteFile(absPath, []byte(payload), 0o644); err != nil {
			tb.Fatalf("write edit fixture file: %v", err)
		}
		calls = append(calls, Call{
			CallID: fmt.Sprintf("edit-%03d", i),
			Name:   "edit",
			Arguments: mustArgsJSON(tb, map[string]any{
				"path":       relPath,
				"old_string": "TOKEN",
				"new_string": "TOKEN",
			}),
		})
	}
	return calls
}

func buildBashPerfCalls(tb testing.TB, _ string, callCount int) []Call {
	tb.Helper()

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		calls = append(calls, Call{
			CallID: fmt.Sprintf("bash-%03d", i),
			Name:   "bash",
			Arguments: mustArgsJSON(tb, map[string]any{
				"command":    fmt.Sprintf("printf 'tool-perf-%03d'", i),
				"timeout_ms": 4000,
			}),
		})
	}
	return calls
}

func buildGlobPerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()
	createSearchFixture(tb, workspace, 20, 22)

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		calls = append(calls, Call{
			CallID:    fmt.Sprintf("glob-%03d", i),
			Name:      "glob",
			Arguments: mustArgsJSON(tb, map[string]any{"pattern": "**/*.go", "max_results": 1800, "timeout_ms": 18000}),
		})
	}
	return calls
}

func buildGrepPerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()
	createSearchFixture(tb, workspace, 20, 22)

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		token := i % 5
		calls = append(calls, Call{
			CallID: fmt.Sprintf("grep-%03d", i),
			Name:   "grep",
			Arguments: mustArgsJSON(tb, map[string]any{
				"pattern":     fmt.Sprintf("HOT_PATH_TOKEN_%d", token),
				"include":     "*.go",
				"max_results": 1800,
				"timeout_ms":  18000,
			}),
		})
	}
	return calls
}

func buildListPerfCalls(tb testing.TB, workspace string, callCount int) []Call {
	tb.Helper()
	createSearchFixture(tb, workspace, 20, 22)

	calls := make([]Call, 0, callCount)
	for i := 0; i < callCount; i++ {
		mode := "flat"
		if i%3 == 0 {
			mode = "tree"
		}
		calls = append(calls, Call{
			CallID: fmt.Sprintf("list-%03d", i),
			Name:   "list",
			Arguments: mustArgsJSON(tb, map[string]any{
				"mode":        mode,
				"max_entries": 200,
				"max_depth":   4,
			}),
		})
	}
	return calls
}
