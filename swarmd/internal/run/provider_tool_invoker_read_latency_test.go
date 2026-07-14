package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
)

func BenchmarkProviderManagedV3Read2000LinePipeline(b *testing.B) {
	workspace := b.TempDir()
	fixturePath := filepath.Join(workspace, "read-2000-lines.txt")
	if err := os.WriteFile(fixturePath, []byte(providerManagedReadLatencyFixture(2000)), 0o644); err != nil {
		b.Fatalf("write 2,000-line fixture: %v", err)
	}
	svc, sessionID, permissions, cleanup := newProviderManagedV3PermissionTestService(b, workspace)
	defer cleanup()
	permissions.SetBypassPermissions(true)

	args := mustProviderToolInvokerBenchmarkJSON(b, map[string]any{"path": "read-2000-lines.txt", "max_lines": 2000})
	var last providerManagedReadBenchmarkMeasurement
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		callID := fmt.Sprintf("call-read-2000-%d", iteration)
		var persistenceElapsed time.Duration
		invoker := svc.newProviderToolInvoker(providerToolInvokerConfig{
			sessionID:            sessionID,
			permissionSessionID:  sessionID,
			runID:                fmt.Sprintf("run-read-2000-%d", iteration),
			step:                 1,
			sessionMode:          sessionruntime.ModeAuto,
			workspacePath:        workspace,
			workspaceRoots:       []string{workspace},
			workspaceOriginPath:  workspace,
			workspaceOriginRoots: []string{workspace},
			workspaceName:        "workspace",
			applySessionMutation: func(input sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error) {
				started := time.Now()
				result, err := svc.sessions.ApplySessionMutation(input)
				persistenceElapsed += time.Since(started)
				return result, err
			},
			providerManagedV3: true,
		})
		started := time.Now()
		result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: callID, Name: "read", Arguments: args})
		if err != nil {
			b.Fatalf("execute provider-managed read: %v", err)
		}
		if result.Error != "" {
			b.Fatalf("read result error: %s", result.Error)
		}
		var raw struct {
			Bytes int `json:"bytes"`
			Count int `json:"count"`
		}
		if err := json.Unmarshal([]byte(result.Output), &raw); err != nil {
			b.Fatalf("decode read output: %v", err)
		}
		if raw.Count != 2000 {
			b.Fatalf("raw line count = %d, want 2000", raw.Count)
		}
		continuationStarted := time.Now()
		continuationItems := []map[string]any{
			{"type": "function_call", "call_id": callID, "name": "read", "arguments": args},
			{"type": "function_call_output", "call_id": callID, "output": result.TextForModel},
		}
		continuationJSON, err := json.Marshal(continuationItems)
		if err != nil {
			b.Fatalf("serialize continuation: %v", err)
		}
		last = providerManagedReadBenchmarkMeasurement{
			total:                 time.Since(started),
			toolRuntimeMS:         result.DurationMS,
			persistence:           persistenceElapsed,
			continuationSerialize: time.Since(continuationStarted),
			rawContentBytes:       raw.Bytes,
			structuredOutputBytes: len(result.Output),
			modelOutputBytes:      len(result.TextForModel),
			continuationBytes:     len(continuationJSON),
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(last.toolRuntimeMS), "tool_runtime_ms")
	b.ReportMetric(float64(last.persistence.Nanoseconds()), "persistence_ns")
	b.ReportMetric(float64(last.continuationSerialize.Nanoseconds()), "continuation_serialize_ns")
	b.ReportMetric(float64(last.total.Nanoseconds()), "total_pipeline_ns")
	b.ReportMetric(float64(last.rawContentBytes), "raw_content_bytes")
	b.ReportMetric(float64(last.structuredOutputBytes), "structured_output_bytes")
	b.ReportMetric(float64(last.modelOutputBytes), "model_output_bytes")
	b.ReportMetric(float64(last.continuationBytes), "continuation_bytes")
}

type providerManagedReadBenchmarkMeasurement struct {
	total                 time.Duration
	toolRuntimeMS         int64
	persistence           time.Duration
	continuationSerialize time.Duration
	rawContentBytes       int
	structuredOutputBytes int
	modelOutputBytes      int
	continuationBytes     int
}

func providerManagedReadLatencyFixture(lineCount int) string {
	var builder strings.Builder
	for line := 1; line <= lineCount; line++ {
		fmt.Fprintf(&builder, "line-%04d %s\n", line, "representative-payload")
	}
	return builder.String()
}

func mustProviderToolInvokerBenchmarkJSON(b *testing.B, value any) string {
	b.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		b.Fatalf("marshal benchmark JSON: %v", err)
	}
	return string(raw)
}
