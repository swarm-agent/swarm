package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
	"swarm/packages/swarmd/internal/tool"
)

type runStreamLoadConfig struct {
	Name           string
	Sessions       int
	Dirs           int
	FilesPerDir    int
	ReadCalls      int
	SearchPairs    int
	RuntimeWorkers int
	MaxResults     int
	Timeout        time.Duration
}

func (c runStreamLoadConfig) CallsPerSession() int {
	return c.ReadCalls + c.SearchPairs*2
}

func TestRunStreamRealtimeConcurrentLoad(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep (rg) is required for grep/glob tests")
	}

	workloads := []runStreamLoadConfig{
		{
			Name:           "sessions_16",
			Sessions:       16,
			Dirs:           14,
			FilesPerDir:    20,
			ReadCalls:      6,
			SearchPairs:    6,
			RuntimeWorkers: 12,
			MaxResults:     1200,
			Timeout:        70 * time.Second,
		},
		{
			Name:           "sessions_40",
			Sessions:       40,
			Dirs:           16,
			FilesPerDir:    24,
			ReadCalls:      8,
			SearchPairs:    8,
			RuntimeWorkers: 16,
			MaxResults:     1500,
			Timeout:        90 * time.Second,
		},
	}
	if testing.Short() {
		workloads = workloads[:1]
	}

	for _, workload := range workloads {
		workload := workload
		t.Run(workload.Name, func(t *testing.T) {
			runner, sessionIDs := buildRunStreamLoadRunner(t, workload)

			server := NewServer(
				"test",
				nil,
				nil,
				nil,
				runner,
				&sessionruntime.Service{},
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				stream.NewHub(nil),
			)
			httpServer := httptest.NewServer(server.Handler())
			defer httpServer.Close()

			ctx, cancel := context.WithTimeout(context.Background(), workload.Timeout)
			defer cancel()

			results := make(chan runStreamClientResult, workload.Sessions)
			var wg sync.WaitGroup
			startedAt := time.Now()

			for _, sessionID := range sessionIDs {
				sessionID := sessionID
				wg.Add(1)
				go func() {
					defer wg.Done()
					results <- executeRunStreamRequest(ctx, httpServer.URL, sessionID, workload.CallsPerSession())
				}()
			}

			wg.Wait()
			close(results)
			totalElapsed := time.Since(startedAt)

			var (
				totalEvents        int
				totalToolStarted   int
				totalToolCompleted int
				firstEventLat      []time.Duration
				streamDuration     []time.Duration
				failed             int
			)
			for result := range results {
				if result.Err != nil {
					failed++
					t.Errorf("session %s failed: %v", result.SessionID, result.Err)
					continue
				}
				totalEvents += result.EventCount
				totalToolStarted += result.ToolStarted
				totalToolCompleted += result.ToolCompleted
				firstEventLat = append(firstEventLat, result.FirstEventLatency)
				streamDuration = append(streamDuration, result.StreamDuration)
			}
			if failed > 0 {
				t.Fatalf("run stream load had %d failing sessions", failed)
			}

			if totalToolStarted != workload.Sessions*workload.CallsPerSession() {
				t.Fatalf("expected %d tool.started events, got %d", workload.Sessions*workload.CallsPerSession(), totalToolStarted)
			}
			if totalToolCompleted != workload.Sessions*workload.CallsPerSession() {
				t.Fatalf("expected %d tool.completed events, got %d", workload.Sessions*workload.CallsPerSession(), totalToolCompleted)
			}

			p50First := percentileDuration(firstEventLat, 0.50)
			p95First := percentileDuration(firstEventLat, 0.95)
			p50Stream := percentileDuration(streamDuration, 0.50)
			p95Stream := percentileDuration(streamDuration, 0.95)
			eventsPerSecond := 0.0
			if totalElapsed > 0 {
				eventsPerSecond = float64(totalEvents) / totalElapsed.Seconds()
			}
			t.Logf(
				"run/stream realtime load completed: sessions=%d calls_per_session=%d total_events=%d elapsed=%s events_per_sec=%.2f first_event_p50=%s first_event_p95=%s stream_p50=%s stream_p95=%s",
				workload.Sessions,
				workload.CallsPerSession(),
				totalEvents,
				totalElapsed,
				eventsPerSecond,
				p50First,
				p95First,
				p50Stream,
				p95Stream,
			)
		})
	}
}

type runStreamLoadRunner struct {
	toolRuntime *tool.Runtime
	workspaces  map[string]string
	dirs        int
	filesPerDir int
	readCalls   int
	searchPairs int
	maxResults  int
}

func buildRunStreamLoadRunner(tb testing.TB, cfg runStreamLoadConfig) (*runStreamLoadRunner, []string) {
	tb.Helper()

	workspaces := make(map[string]string, cfg.Sessions)
	sessionIDs := make([]string, 0, cfg.Sessions)
	for i := 0; i < cfg.Sessions; i++ {
		sessionID := fmt.Sprintf("load-session-%02d", i)
		workspace := tb.TempDir()
		createRunStreamFixture(tb, workspace, cfg.Dirs, cfg.FilesPerDir)
		workspaces[sessionID] = workspace
		sessionIDs = append(sessionIDs, sessionID)
	}

	return &runStreamLoadRunner{
		toolRuntime: tool.NewRuntime(cfg.RuntimeWorkers),
		workspaces:  workspaces,
		dirs:        cfg.Dirs,
		filesPerDir: cfg.FilesPerDir,
		readCalls:   cfg.ReadCalls,
		searchPairs: cfg.SearchPairs,
		maxResults:  cfg.MaxResults,
	}, sessionIDs
}

func (r *runStreamLoadRunner) RunTurn(ctx context.Context, sessionID string, options runruntime.RunOptions) (runruntime.RunResult, error) {
	return r.RunTurnStreaming(ctx, sessionID, options, nil)
}

func (r *runStreamLoadRunner) RunTurnStreaming(ctx context.Context, sessionID string, _ runruntime.RunOptions, onEvent runruntime.StreamHandler) (runruntime.RunResult, error) {
	workspace, ok := r.workspaces[sessionID]
	if !ok {
		return runruntime.RunResult{}, fmt.Errorf("unknown session %q", sessionID)
	}

	emit := func(event runruntime.StreamEvent) {
		if onEvent != nil {
			onEvent(event)
		}
	}

	emit(runruntime.StreamEvent{Type: runruntime.StreamEventTurnStarted, SessionID: sessionID})
	emit(runruntime.StreamEvent{Type: runruntime.StreamEventStepStarted, SessionID: sessionID, Step: 1})
	emit(runruntime.StreamEvent{Type: runruntime.StreamEventAssistantDelta, SessionID: sessionID, Step: 1, Delta: "Analyzing workspace..."})

	toolCalls := r.buildToolCalls(sessionID)
	for i := range toolCalls {
		call := toolCalls[i]
		emit(runruntime.StreamEvent{
			Type:      runruntime.StreamEventToolStarted,
			SessionID: sessionID,
			Step:      1,
			ToolName:  call.Name,
			CallID:    call.CallID,
			Arguments: call.Arguments,
		})
	}

	results := r.toolRuntime.ExecuteBatchStreaming(ctx, workspace, toolCalls, func(_ int, call tool.Call, result tool.Result) {
		callID := strings.TrimSpace(result.CallID)
		if callID == "" {
			callID = strings.TrimSpace(call.CallID)
		}
		name := strings.TrimSpace(result.Name)
		if name == "" {
			name = strings.TrimSpace(call.Name)
		}
		emit(runruntime.StreamEvent{
			Type:       runruntime.StreamEventToolCompleted,
			SessionID:  sessionID,
			Step:       1,
			ToolName:   name,
			CallID:     callID,
			Output:     summarizeLoadToolOutput(name, result.Output),
			Error:      strings.TrimSpace(result.Error),
			DurationMS: result.DurationMS,
		})
	})

	for i := range results {
		if errText := strings.TrimSpace(results[i].Error); errText != "" {
			return runruntime.RunResult{}, fmt.Errorf("tool %s failed for %s: %s", results[i].Name, sessionID, errText)
		}
	}

	emit(runruntime.StreamEvent{Type: runruntime.StreamEventAssistantDelta, SessionID: sessionID, Step: 1, Delta: "Done."})

	return runruntime.RunResult{
		SessionID:     sessionID,
		Model:         "load-test",
		Thinking:      "low",
		Steps:         1,
		ToolCallCount: len(toolCalls),
		AssistantMessage: pebblestore.MessageSnapshot{
			Role:    "assistant",
			Content: "load complete",
		},
	}, nil
}

func (r *runStreamLoadRunner) buildToolCalls(sessionID string) []tool.Call {
	index := sessionIndexFromID(sessionID)
	calls := make([]tool.Call, 0, r.readCalls+r.searchPairs*2)

	for i := 0; i < r.readCalls; i++ {
		dir := (index + i) % r.dirs
		file := (index*5 + i*7) % r.filesPerDir
		path := fmt.Sprintf("pkg_%02d/file_%03d.go", dir, file)
		calls = append(calls, tool.Call{
			CallID:    fmt.Sprintf("%s-read-%02d", sessionID, i),
			Name:      "read",
			Arguments: mustMarshalJSON(map[string]any{"path": path, "max_lines": 240}),
		})
	}

	for i := 0; i < r.searchPairs; i++ {
		calls = append(calls, tool.Call{
			CallID:    fmt.Sprintf("%s-glob-%02d", sessionID, i),
			Name:      "glob",
			Arguments: mustMarshalJSON(map[string]any{"pattern": "**/*.go", "max_results": r.maxResults, "timeout_ms": 12000}),
		})
		token := (index + i) % 5
		calls = append(calls, tool.Call{
			CallID: fmt.Sprintf("%s-grep-%02d", sessionID, i),
			Name:   "grep",
			Arguments: mustMarshalJSON(map[string]any{
				"pattern":     fmt.Sprintf("HOT_PATH_TOKEN_%d", token),
				"include":     "*.go",
				"max_results": r.maxResults,
				"timeout_ms":  12000,
			}),
		})
	}

	return calls
}

type runStreamClientResult struct {
	SessionID         string
	EventCount        int
	ToolStarted       int
	ToolCompleted     int
	FirstEventLatency time.Duration
	StreamDuration    time.Duration
	Err               error
}

func executeRunStreamRequest(ctx context.Context, baseURL, sessionID string, expectedToolCalls int) runStreamClientResult {
	result := runStreamClientResult{SessionID: sessionID}
	startedAt := time.Now()

	payload, err := json.Marshal(map[string]any{
		"prompt":       "Run load test tools",
		"instructions": "Execute tool workload",
	})
	if err != nil {
		result.Err = err
		return result
	}

	endpoint := fmt.Sprintf("%s/v1/sessions/%s/run/stream", strings.TrimRight(baseURL, "/"), sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		result.Err = err
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		result.Err = err
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		result.Err = fmt.Errorf("stream status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		return result
	}
	if ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))); !strings.Contains(ct, "application/x-ndjson") {
		result.Err = fmt.Errorf("unexpected content-type %q", ct)
		return result
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var (
		seenCompleted bool
		seenFirst     bool
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		result.EventCount++
		if !seenFirst {
			seenFirst = true
			result.FirstEventLatency = time.Since(startedAt)
		}

		var envelope struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			result.Err = fmt.Errorf("decode stream event: %w", err)
			return result
		}
		switch strings.TrimSpace(envelope.Type) {
		case runruntime.StreamEventToolStarted:
			result.ToolStarted++
		case runruntime.StreamEventToolCompleted:
			result.ToolCompleted++
		case "turn.error":
			result.Err = fmt.Errorf("turn.error from server: %s", strings.TrimSpace(envelope.Error))
			return result
		case "turn.completed":
			seenCompleted = true
		}
	}
	if err := scanner.Err(); err != nil {
		result.Err = fmt.Errorf("read stream failed: %w", err)
		return result
	}
	result.StreamDuration = time.Since(startedAt)

	if !seenCompleted {
		result.Err = errors.New("stream closed without turn.completed event")
		return result
	}
	if result.ToolStarted != expectedToolCalls {
		result.Err = fmt.Errorf("expected %d tool.started events, got %d", expectedToolCalls, result.ToolStarted)
		return result
	}
	if result.ToolCompleted != expectedToolCalls {
		result.Err = fmt.Errorf("expected %d tool.completed events, got %d", expectedToolCalls, result.ToolCompleted)
		return result
	}
	return result
}

func summarizeLoadToolOutput(name, raw string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return ""
	}
	switch name {
	case "read":
		return fmt.Sprintf("read bytes=%d", jsonInt(decoded, "bytes"))
	case "glob":
		return fmt.Sprintf("glob count=%d", jsonInt(decoded, "count"))
	case "grep":
		return fmt.Sprintf("grep count=%d", jsonInt(decoded, "count"))
	default:
		return ""
	}
}

func jsonInt(payload map[string]any, key string) int {
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

func sessionIndexFromID(sessionID string) int {
	idx := strings.LastIndex(sessionID, "-")
	if idx < 0 || idx+1 >= len(sessionID) {
		return 0
	}
	n, err := strconv.Atoi(sessionID[idx+1:])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func mustMarshalJSON(payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func createRunStreamFixture(tb testing.TB, root string, dirs, filesPerDir int) {
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
