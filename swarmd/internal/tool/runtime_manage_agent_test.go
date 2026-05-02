package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestManageAgentCreatePublishesAgentEvent(t *testing.T) {
	workspace := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(workspace, "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	published := make([]pebblestore.EventEnvelope, 0, 1)
	agents.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})
	rt := NewRuntime(2)
	rt.SetManageAgentService(agents)
	results := rt.ExecuteBatch(context.Background(), workspace, []Call{{
		CallID:    "manage-agent-create-publishes",
		Name:      "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "create", "confirm": true, "agent": "evented", "content": map[string]any{"name": "evented", "mode": "subagent", "prompt": "Handle events.", "execution_setting": "readwrite"}}),
	}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if errText := strings.TrimSpace(results[0].Error); errText != "" {
		t.Fatalf("unexpected manage-agent error: %s", errText)
	}
	if len(published) != 1 {
		t.Fatalf("published event count = %d, want 1", len(published))
	}
	if published[0].Stream != "system:agent" || published[0].EventType != "agent.profile.created" {
		t.Fatalf("published event = %s %s, want system:agent agent.profile.created", published[0].Stream, published[0].EventType)
	}
}

func TestManageAgentCreateAcceptsToolContractWhenServiceConfigured(t *testing.T) {
	workspace := t.TempDir()
	store, err := pebblestore.Open(filepath.Join(workspace, "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	rt := NewRuntime(2)
	rt.SetManageAgentService(agents)
	results := rt.ExecuteBatch(context.Background(), workspace, []Call{{
		CallID: "manage-agent-create-tool-contract",
		Name:   "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{
			"action":  "create",
			"confirm": true,
			"agent":   "scoped-reviewer",
			"content": map[string]any{
				"name":              "scoped-reviewer",
				"mode":              "subagent",
				"description":       "Scoped reviewer",
				"prompt":            "Review safely.",
				"execution_setting": "read",
				"tool_contract": map[string]any{
					"preset": "read_only",
					"tools": map[string]any{
						"bash": map[string]any{
							"enabled":       true,
							"bash_prefixes": []any{"git status", "git diff"},
						},
						"edit": map[string]any{"enabled": false},
					},
				},
			},
		}),
	}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if errText := strings.TrimSpace(results[0].Error); errText != "" {
		t.Fatalf("unexpected manage-agent error: %s", errText)
	}
	decoded := decodeManageAgentResultJSON(t, results[0].Output)
	agent, ok := decoded["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent payload is %T", decoded["agent"])
	}
	contract, ok := agent["tool_contract"].(map[string]any)
	if !ok {
		t.Fatalf("tool_contract payload is %T", agent["tool_contract"])
	}
	if got := contract["preset"]; got != "read_only" {
		t.Fatalf("preset = %v, want read_only", got)
	}
	tools, ok := contract["tools"].(map[string]any)
	if !ok {
		t.Fatalf("contract.tools is %T", contract["tools"])
	}
	bashTool, ok := tools["bash"].(map[string]any)
	if !ok {
		t.Fatalf("bash config is %T", tools["bash"])
	}
	if enabled, ok := bashTool["enabled"].(bool); !ok || !enabled {
		t.Fatalf("bash.enabled = %v, want true", bashTool["enabled"])
	}
	prefixes, ok := bashTool["bash_prefixes"].([]any)
	if !ok || len(prefixes) != 2 {
		t.Fatalf("bash_prefixes = %T %v, want two prefixes", bashTool["bash_prefixes"], bashTool["bash_prefixes"])
	}
}

func mustManageAgentArgsJSON(tb testing.TB, payload map[string]any) string {
	tb.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		tb.Fatalf("marshal args: %v", err)
	}
	return string(encoded)
}

func decodeManageAgentResultJSON(tb testing.TB, raw string) map[string]any {
	tb.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		tb.Fatalf("decode result payload: %v\npayload=%s", err, raw)
	}
	return decoded
}
