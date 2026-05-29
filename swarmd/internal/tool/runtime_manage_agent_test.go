package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
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
		CallID: "manage-agent-create-publishes",
		Name:   "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "create", "confirm": true, "agent": "evented", "content": map[string]any{
			"name":              "evented",
			"mode":              "subagent",
			"prompt":            "Handle events.",
			"execution_setting": "readwrite",
			"tool_contract":     map[string]any{"preset": "read_write"},
		}}),
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

func TestManageAgentCreateAcceptsBoundedToolContractOverrideWhenServiceConfigured(t *testing.T) {
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

func TestManageAgentInspectToolInventoryIncludesPresetGrants(t *testing.T) {
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
		CallID:    "manage-agent-inspect-inventory",
		Name:      "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "inspect"}),
	}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if errText := strings.TrimSpace(results[0].Error); errText != "" {
		t.Fatalf("unexpected manage-agent error: %s", errText)
	}
	decoded := decodeManageAgentResultJSON(t, results[0].Output)
	inventory, ok := decoded["tool_inventory"].(map[string]any)
	if !ok {
		t.Fatalf("tool_inventory is %T", decoded["tool_inventory"])
	}
	presets, ok := inventory["presets"].([]any)
	if !ok || len(presets) == 0 {
		t.Fatalf("presets = %T %v, want non-empty", inventory["presets"], inventory["presets"])
	}
	foundReadOnly := false
	for _, rawPreset := range presets {
		preset, ok := rawPreset.(map[string]any)
		if !ok || preset["id"] != "read_only" {
			continue
		}
		foundReadOnly = true
		enabled, ok := preset["enabled_tools"].([]any)
		if !ok || len(enabled) == 0 {
			t.Fatalf("read_only.enabled_tools = %T %v, want non-empty", preset["enabled_tools"], preset["enabled_tools"])
		}
	}
	if !foundReadOnly {
		t.Fatalf("read_only preset not found in %v", presets)
	}
	tools, ok := inventory["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %T %v, want non-empty", inventory["tools"], inventory["tools"])
	}
	firstTool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("first tool is %T", tools[0])
	}
	if strings.TrimSpace(asString(firstTool["contract_name"])) == "" {
		t.Fatalf("first tool missing contract_name: %v", firstTool)
	}
}

func TestManageAgentCreateRejectsInvalidToolContract(t *testing.T) {
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

	tests := []struct {
		name    string
		content map[string]any
		wantErr string
	}{
		{
			name: "missing preset",
			content: map[string]any{
				"name":              "missing-preset",
				"mode":              "subagent",
				"prompt":            "No preset.",
				"execution_setting": "read",
				"tool_contract":     map[string]any{"tools": map[string]any{"read": map[string]any{"enabled": true}}},
			},
			wantErr: "tool_contract requires preset",
		},
		{
			name: "unknown preset",
			content: map[string]any{
				"name":              "unknown-preset",
				"mode":              "subagent",
				"prompt":            "Unknown preset.",
				"execution_setting": "read",
				"tool_contract":     map[string]any{"preset": "everything"},
			},
			wantErr: "unsupported tool_contract.preset",
		},
		{
			name: "unknown tool",
			content: map[string]any{
				"name":              "unknown-tool",
				"mode":              "subagent",
				"prompt":            "Unknown tool.",
				"execution_setting": "read",
				"tool_contract":     map[string]any{"preset": "read_only", "tools": map[string]any{"root_shell": map[string]any{"enabled": true}}},
			},
			wantErr: "not in the advertised tool_inventory",
		},
		{
			name: "unbounded bash",
			content: map[string]any{
				"name":              "unbounded-bash",
				"mode":              "subagent",
				"prompt":            "Unbounded bash.",
				"execution_setting": "readwrite",
				"tool_contract":     map[string]any{"preset": "read_only", "tools": map[string]any{"bash": map[string]any{"enabled": true}}},
			},
			wantErr: "bash enabled=true requires explicit bash_prefixes",
		},
		{
			name: "inherit policy",
			content: map[string]any{
				"name":              "inherit-policy",
				"mode":              "subagent",
				"prompt":            "Inherit policy.",
				"execution_setting": "read",
				"tool_contract":     map[string]any{"preset": "read_only", "inherit_policy": true},
			},
			wantErr: "inherit_policy is not allowed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := rt.ExecuteBatch(context.Background(), workspace, []Call{{
				CallID:    "manage-agent-invalid-tool-contract",
				Name:      "manage-agent",
				Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "create", "confirm": true, "agent": tt.content["name"], "content": tt.content}),
			}})
			if len(results) != 1 {
				t.Fatalf("expected one result, got %d", len(results))
			}
			if errText := strings.TrimSpace(results[0].Error); !strings.Contains(errText, tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", errText, tt.wantErr)
			}
		})
	}
}

func TestManageAgentCreateRequiresPresetForNonPlanAgent(t *testing.T) {
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
		CallID: "manage-agent-create-missing-tool-contract",
		Name:   "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "create", "confirm": true, "agent": "missing-contract", "content": map[string]any{
			"name":              "missing-contract",
			"mode":              "subagent",
			"prompt":            "Missing contract.",
			"execution_setting": "read",
		}}),
	}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if errText := strings.TrimSpace(results[0].Error); !strings.Contains(errText, "requires content.tool_contract.preset") {
		t.Fatalf("error = %q, want missing preset error", errText)
	}
}

func TestManageAgentTranscriptReadsSessionMessages(t *testing.T) {
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
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	child, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		SessionID:     "child-session-1",
		Title:         "subagent report",
		WorkspacePath: workspace,
		WorkspaceName: "workspace",
		Preference: &pebblestore.ModelPreference{
			Provider: "test-provider",
			Model:    "test-model",
			Thinking: "medium",
		},
		Metadata: map[string]any{
			"parent_session_id":  "parent-session-1",
			"lineage_kind":       "delegated_subagent",
			"lineage_label":      "@explorer",
			"requested_subagent": "explorer",
			"subagent":           "explorer",
		},
	})
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}
	if _, _, _, err := sessions.AppendMessage(child.ID, "assistant", "full report body", map[string]any{"source": "subagent_final"}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	rt := NewRuntime(2)
	rt.SetManageWorktreeServices(sessions, nil, nil)
	results := rt.ExecuteBatch(context.Background(), workspace, []Call{{
		CallID:    "manage-agent-transcript",
		Name:      "manage-agent",
		Arguments: mustManageAgentArgsJSON(t, map[string]any{"action": "transcript", "session_id": child.ID}),
	}})
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if errText := strings.TrimSpace(results[0].Error); errText != "" {
		t.Fatalf("unexpected manage-agent error: %s", errText)
	}
	payload := decodeManageAgentResultJSON(t, results[0].Output)
	if payload["action"] != "transcript" || payload["session_id"] != child.ID {
		t.Fatalf("unexpected transcript payload: %#v", payload)
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages missing: %#v", payload["messages"])
	}
	last, ok := messages[len(messages)-1].(map[string]any)
	if !ok || last["content"] != "full report body" || last["role"] != "assistant" {
		t.Fatalf("unexpected transcript message: %#v", last)
	}
}
