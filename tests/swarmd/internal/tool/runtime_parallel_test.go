package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecuteBatchStreamingMixedSearchTools(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is required for grep/glob tests")
	}

	workspace := t.TempDir()
	createSearchFixture(t, workspace, 18, 22)

	calls := make([]Call, 0, 48)
	for i := 0; i < 24; i++ {
		calls = append(calls, Call{
			CallID:    fmt.Sprintf("glob-%02d", i),
			Name:      "glob",
			Arguments: mustArgsJSON(t, map[string]any{"pattern": "**/*.go", "max_results": 1200, "timeout_ms": 15000}),
		})
		calls = append(calls, Call{
			CallID: fmt.Sprintf("grep-%02d", i),
			Name:   "grep",
			Arguments: mustArgsJSON(t, map[string]any{
				"pattern":     "HOT_PATH_TOKEN_[0-4]",
				"include":     "*.go",
				"max_results": 1200,
				"timeout_ms":  15000,
			}),
		})
	}

	rt := NewRuntime(16)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var callbackCount atomic.Int64
	start := time.Now()
	results := rt.ExecuteBatchStreaming(ctx, workspace, calls, func(_ int, _ Call, _ Result) {
		callbackCount.Add(1)
	})
	elapsed := time.Since(start)

	if len(results) != len(calls) {
		t.Fatalf("expected %d results, got %d", len(calls), len(results))
	}
	if got := int(callbackCount.Load()); got != len(calls) {
		t.Fatalf("expected %d callbacks, got %d", len(calls), got)
	}

	var grepCountTotal int
	var globCountTotal int
	for _, result := range results {
		if strings.TrimSpace(result.Error) != "" {
			t.Fatalf("tool %s failed: %s", result.Name, result.Error)
		}

		decoded := decodeResultJSON(t, result.Output)
		switch strings.ToLower(strings.TrimSpace(result.Name)) {
		case "grep":
			grepCountTotal += mapIntValue(decoded, "count")
		case "glob":
			globCountTotal += mapIntValue(decoded, "count")
		}
	}

	if grepCountTotal <= 0 {
		t.Fatalf("expected grep to find matches, total count was %d", grepCountTotal)
	}
	if globCountTotal <= 0 {
		t.Fatalf("expected glob to find files, total count was %d", globCountTotal)
	}

	t.Logf("mixed search workload completed: calls=%d elapsed=%s callbacks=%d grep_matches=%d glob_files=%d",
		len(calls), elapsed, callbackCount.Load(), grepCountTotal, globCountTotal)
}

func TestExecuteBatchStreamingWithProgressBashOutput(t *testing.T) {
	workspace := t.TempDir()
	rt := NewRuntime(2)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var mu sync.Mutex
	deltas := make([]string, 0, 8)
	results := rt.ExecuteBatchStreamingWithProgress(ctx, workspace, []Call{
		{
			CallID: "bash-progress",
			Name:   "bash",
			Arguments: mustArgsJSON(t, map[string]any{
				"command":    "for i in 1 2 3; do echo line-$i; sleep 0.03; done",
				"timeout_ms": 4000,
			}),
		},
	}, func(_ int, _ Call, progress Progress) {
		if strings.ToLower(strings.TrimSpace(progress.Stage)) != "output" {
			return
		}
		if strings.TrimSpace(progress.Output) == "" {
			return
		}
		mu.Lock()
		deltas = append(deltas, progress.Output)
		mu.Unlock()
	}, nil)

	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if strings.TrimSpace(results[0].Error) != "" {
		t.Fatalf("unexpected bash error: %s", results[0].Error)
	}

	mu.Lock()
	deltaCount := len(deltas)
	joined := strings.Join(deltas, "\n")
	mu.Unlock()
	if deltaCount == 0 {
		t.Fatalf("expected at least one bash progress delta")
	}
	if !strings.Contains(joined, "line-1") {
		t.Fatalf("expected streamed delta to contain command output, got %q", joined)
	}
}

func BenchmarkExecuteBatchStreamingMixedSearchTools(b *testing.B) {
	if _, err := exec.LookPath("rg"); err != nil {
		b.Skip("ripgrep (rg) is required for grep/glob benchmark")
	}

	workspace := b.TempDir()
	createSearchFixture(b, workspace, 24, 24)

	calls := make([]Call, 0, 24)
	for i := 0; i < 12; i++ {
		calls = append(calls,
			Call{
				CallID:    fmt.Sprintf("bench-glob-%02d", i),
				Name:      "glob",
				Arguments: `{"pattern":"**/*.go","max_results":1000,"timeout_ms":12000}`,
			},
			Call{
				CallID:    fmt.Sprintf("bench-grep-%02d", i),
				Name:      "grep",
				Arguments: `{"pattern":"HOT_PATH_TOKEN_[0-4]","include":"*.go","max_results":1000,"timeout_ms":12000}`,
			},
		)
	}

	bench := func(b *testing.B, workers int) {
		rt := NewRuntime(workers)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			_ = rt.ExecuteBatch(ctx, workspace, calls)
			cancel()
		}
	}

	b.Run("workers_1", func(b *testing.B) { bench(b, 1) })
	b.Run("workers_8", func(b *testing.B) { bench(b, 8) })
	b.Run("workers_16", func(b *testing.B) { bench(b, 16) })
}

type concurrentSessionConfig struct {
	SessionCount   int
	Dirs           int
	FilesPerDir    int
	ReadCalls      int
	SearchPairs    int
	RuntimeWorkers int
	MaxResults     int
	ReadMaxLines   int
	Timeout        time.Duration
}

type concurrentSessionResult struct {
	Calls       int
	Callbacks   int
	Elapsed     time.Duration
	ReadBytes   int
	GlobMatches int
	GrepMatches int
}

type concurrentSessionSummary struct {
	SessionCount     int
	TotalCalls       int
	TotalCallbacks   int
	TotalReadBytes   int
	TotalGlobMatches int
	TotalGrepMatches int
	TotalElapsed     time.Duration
	CallsPerSecond   float64
	SessionDurations []time.Duration
}

func TestExecuteBatchStreamingReadGlobGrepConcurrentSessions(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is required for grep/glob tests")
	}

	summary, err := runConcurrentSessionWorkload(t, concurrentSessionConfig{
		SessionCount:   10,
		Dirs:           14,
		FilesPerDir:    20,
		ReadCalls:      6,
		SearchPairs:    6,
		RuntimeWorkers: 10,
		MaxResults:     1200,
		ReadMaxLines:   200,
		Timeout:        70 * time.Second,
	})
	if err != nil {
		t.Fatalf("multi-session workload failed: %v", err)
	}

	p50 := percentileDuration(summary.SessionDurations, 0.50)
	p95 := percentileDuration(summary.SessionDurations, 0.95)
	t.Logf("multi-session workload completed: sessions=%d calls=%d callbacks=%d elapsed=%s throughput=%.2f calls/sec p50=%s p95=%s read_bytes=%d glob_matches=%d grep_matches=%d",
		summary.SessionCount, summary.TotalCalls, summary.TotalCallbacks, summary.TotalElapsed, summary.CallsPerSecond, p50, p95,
		summary.TotalReadBytes, summary.TotalGlobMatches, summary.TotalGrepMatches)

	if summary.TotalCalls <= 0 || summary.TotalCallbacks <= 0 {
		t.Fatalf("expected non-zero calls/callbacks, got calls=%d callbacks=%d", summary.TotalCalls, summary.TotalCallbacks)
	}
	if summary.TotalCalls != summary.TotalCallbacks {
		t.Fatalf("expected callbacks to match calls, got calls=%d callbacks=%d", summary.TotalCalls, summary.TotalCallbacks)
	}
}

func TestExecuteBatchStreamingReadGlobGrepMassiveConcurrentSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping massive concurrent workload in short mode")
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is required for grep/glob tests")
	}

	workloads := []concurrentSessionConfig{
		{
			SessionCount:   24,
			Dirs:           18,
			FilesPerDir:    24,
			ReadCalls:      8,
			SearchPairs:    8,
			RuntimeWorkers: 12,
			MaxResults:     1500,
			ReadMaxLines:   240,
			Timeout:        90 * time.Second,
		},
		{
			SessionCount:   36,
			Dirs:           20,
			FilesPerDir:    24,
			ReadCalls:      8,
			SearchPairs:    8,
			RuntimeWorkers: 14,
			MaxResults:     1500,
			ReadMaxLines:   240,
			Timeout:        90 * time.Second,
		},
	}

	for _, workload := range workloads {
		workload := workload
		t.Run(fmt.Sprintf("sessions_%d_workers_%d", workload.SessionCount, workload.RuntimeWorkers), func(t *testing.T) {
			summary, err := runConcurrentSessionWorkload(t, workload)
			if err != nil {
				t.Fatalf("massive workload failed: %v", err)
			}

			p50 := percentileDuration(summary.SessionDurations, 0.50)
			p95 := percentileDuration(summary.SessionDurations, 0.95)
			t.Logf("massive workload completed: sessions=%d calls=%d callbacks=%d elapsed=%s throughput=%.2f calls/sec p50=%s p95=%s read_bytes=%d glob_matches=%d grep_matches=%d",
				summary.SessionCount, summary.TotalCalls, summary.TotalCallbacks, summary.TotalElapsed, summary.CallsPerSecond, p50, p95,
				summary.TotalReadBytes, summary.TotalGlobMatches, summary.TotalGrepMatches)

			if summary.TotalCalls != summary.TotalCallbacks {
				t.Fatalf("expected callbacks to match calls, got calls=%d callbacks=%d", summary.TotalCalls, summary.TotalCallbacks)
			}
		})
	}
}

func runConcurrentSessionWorkload(tb testing.TB, cfg concurrentSessionConfig) (concurrentSessionSummary, error) {
	tb.Helper()

	if cfg.SessionCount <= 0 {
		return concurrentSessionSummary{}, fmt.Errorf("session_count must be > 0")
	}
	if cfg.Dirs <= 0 {
		cfg.Dirs = 10
	}
	if cfg.FilesPerDir <= 0 {
		cfg.FilesPerDir = 16
	}
	if cfg.ReadCalls <= 0 {
		cfg.ReadCalls = 4
	}
	if cfg.SearchPairs <= 0 {
		cfg.SearchPairs = 4
	}
	if cfg.RuntimeWorkers <= 0 {
		cfg.RuntimeWorkers = 8
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 1000
	}
	if cfg.ReadMaxLines <= 0 {
		cfg.ReadMaxLines = 200
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}

	workspaces := make([]string, cfg.SessionCount)
	callsPerSession := make([][]Call, cfg.SessionCount)
	for i := 0; i < cfg.SessionCount; i++ {
		workspace := tb.TempDir()
		createSearchFixture(tb, workspace, cfg.Dirs, cfg.FilesPerDir)
		workspaces[i] = workspace
		callsPerSession[i] = buildConcurrentSessionCalls(tb, i, cfg)
	}

	rt := NewRuntime(cfg.RuntimeWorkers)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	resultsCh := make(chan concurrentSessionResult, cfg.SessionCount)
	errCh := make(chan error, cfg.SessionCount)
	var wg sync.WaitGroup

	startedAt := time.Now()

	for sessionIndex := 0; sessionIndex < cfg.SessionCount; sessionIndex++ {
		workspace := workspaces[sessionIndex]
		calls := callsPerSession[sessionIndex]
		wg.Add(1)

		go func(session int, workspacePath string, sessionCalls []Call) {
			defer wg.Done()

			sessionStartedAt := time.Now()
			var callbackCount atomic.Int64
			executed := rt.ExecuteBatchStreaming(ctx, workspacePath, sessionCalls, func(_ int, _ Call, _ Result) {
				callbackCount.Add(1)
			})
			sessionElapsed := time.Since(sessionStartedAt)

			if len(executed) != len(sessionCalls) {
				errCh <- fmt.Errorf("session %d: expected %d results, got %d", session, len(sessionCalls), len(executed))
				cancel()
				return
			}
			if got := int(callbackCount.Load()); got != len(sessionCalls) {
				errCh <- fmt.Errorf("session %d: expected %d callbacks, got %d", session, len(sessionCalls), got)
				cancel()
				return
			}

			summary := concurrentSessionResult{
				Calls:     len(sessionCalls),
				Callbacks: int(callbackCount.Load()),
				Elapsed:   sessionElapsed,
			}

			for _, result := range executed {
				if errText := strings.TrimSpace(result.Error); errText != "" {
					errCh <- fmt.Errorf("session %d: tool=%s call_id=%s failed: %s", session, result.Name, result.CallID, errText)
					cancel()
					return
				}
				decoded, err := decodeResultJSONSafe(result.Output)
				if err != nil {
					errCh <- fmt.Errorf("session %d: decode %s output failed: %w", session, result.Name, err)
					cancel()
					return
				}
				switch strings.ToLower(strings.TrimSpace(result.Name)) {
				case "read":
					bytesRead := mapIntValue(decoded, "bytes")
					if bytesRead <= 0 {
						errCh <- fmt.Errorf("session %d: read returned no bytes for call_id=%s", session, result.CallID)
						cancel()
						return
					}
					summary.ReadBytes += bytesRead
				case "glob":
					matches := mapIntValue(decoded, "count")
					if matches <= 0 {
						errCh <- fmt.Errorf("session %d: glob returned no matches for call_id=%s", session, result.CallID)
						cancel()
						return
					}
					summary.GlobMatches += matches
				case "grep":
					matches := mapIntValue(decoded, "count")
					if matches <= 0 {
						errCh <- fmt.Errorf("session %d: grep returned no matches for call_id=%s", session, result.CallID)
						cancel()
						return
					}
					summary.GrepMatches += matches
				}
			}

			resultsCh <- summary
		}(sessionIndex, workspace, calls)
	}

	wg.Wait()
	close(resultsCh)
	close(errCh)

	final := concurrentSessionSummary{SessionCount: cfg.SessionCount}
	for result := range resultsCh {
		final.TotalCalls += result.Calls
		final.TotalCallbacks += result.Callbacks
		final.TotalReadBytes += result.ReadBytes
		final.TotalGlobMatches += result.GlobMatches
		final.TotalGrepMatches += result.GrepMatches
		final.SessionDurations = append(final.SessionDurations, result.Elapsed)
	}
	final.TotalElapsed = time.Since(startedAt)
	if final.TotalElapsed > 0 {
		final.CallsPerSecond = float64(final.TotalCalls) / final.TotalElapsed.Seconds()
	}

	for err := range errCh {
		if err != nil {
			return final, err
		}
	}
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return final, fmt.Errorf("workload timed out: %w", err)
	}
	if len(final.SessionDurations) != cfg.SessionCount {
		return final, fmt.Errorf("expected %d session summaries, got %d", cfg.SessionCount, len(final.SessionDurations))
	}
	return final, nil
}

func buildConcurrentSessionCalls(tb testing.TB, sessionIndex int, cfg concurrentSessionConfig) []Call {
	tb.Helper()

	calls := make([]Call, 0, cfg.ReadCalls+cfg.SearchPairs*2)
	searchTimeoutMS := int((cfg.Timeout / 2).Milliseconds())
	if searchTimeoutMS <= 0 {
		searchTimeoutMS = 15000
	}
	if searchTimeoutMS > 45000 {
		searchTimeoutMS = 45000
	}

	for i := 0; i < cfg.ReadCalls; i++ {
		dir := (sessionIndex + i) % cfg.Dirs
		file := (sessionIndex*7 + i*5) % cfg.FilesPerDir
		relPath := fmt.Sprintf("pkg_%02d/file_%03d.go", dir, file)
		calls = append(calls, Call{
			CallID:    fmt.Sprintf("s%02d-read-%02d", sessionIndex, i),
			Name:      "read",
			Arguments: mustArgsJSON(tb, map[string]any{"path": relPath, "max_lines": cfg.ReadMaxLines}),
		})
	}

	for i := 0; i < cfg.SearchPairs; i++ {
		calls = append(calls, Call{
			CallID:    fmt.Sprintf("s%02d-glob-%02d", sessionIndex, i),
			Name:      "glob",
			Arguments: mustArgsJSON(tb, map[string]any{"pattern": "**/*.go", "max_results": cfg.MaxResults, "timeout_ms": searchTimeoutMS}),
		})

		tokenIndex := (sessionIndex + i) % 5
		calls = append(calls, Call{
			CallID: fmt.Sprintf("s%02d-grep-%02d", sessionIndex, i),
			Name:   "grep",
			Arguments: mustArgsJSON(tb, map[string]any{
				"pattern":     fmt.Sprintf("HOT_PATH_TOKEN_%d", tokenIndex),
				"include":     "*.go",
				"max_results": cfg.MaxResults,
				"timeout_ms":  searchTimeoutMS,
			}),
		})
	}

	return calls
}

func decodeResultJSONSafe(raw string) (map[string]any, error) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return decoded, nil
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if percentile <= 0 {
		percentile = 0
	}
	if percentile >= 1 {
		percentile = 1
	}
	cloned := append([]time.Duration(nil), values...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i] < cloned[j] })
	index := int(float64(len(cloned)-1)*percentile + 0.5)
	if index < 0 {
		index = 0
	}
	if index >= len(cloned) {
		index = len(cloned) - 1
	}
	return cloned[index]
}

func createSearchFixture(tb testing.TB, root string, dirs, filesPerDir int) {
	tb.Helper()

	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg_%02d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatalf("create fixture dir: %v", err)
		}
		for f := 0; f < filesPerDir; f++ {
			path := filepath.Join(dir, fmt.Sprintf("file_%03d.go", f))
			token := fmt.Sprintf("HOT_PATH_TOKEN_%d", (d+f)%5)
			content := strings.Builder{}
			content.WriteString("package fixture\n\n")
			content.WriteString("func Fixture() string {\n")
			content.WriteString(fmt.Sprintf("\treturn %q\n", token))
			content.WriteString("}\n")
			content.WriteString(fmt.Sprintf("// marker: %s\n", token))
			if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
				tb.Fatalf("write fixture file: %v", err)
			}
		}
	}
}

func mustArgsJSON(tb testing.TB, payload map[string]any) string {
	tb.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		tb.Fatalf("marshal args: %v", err)
	}
	return string(encoded)
}

func decodeResultJSON(tb testing.TB, raw string) map[string]any {
	tb.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		tb.Fatalf("decode result payload: %v\npayload=%s", err, raw)
	}
	return decoded
}

func mapIntValue(payload map[string]any, key string) int {
	value, ok := payload[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	default:
		return 0
	}
}
