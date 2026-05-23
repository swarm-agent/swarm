package run

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

func TestParseTaskCallArgumentsRequiresExplicitLaunchAssignment(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt":        "inspect the repo",
		"subagent_type": "explorer",
	}))
	if err == nil || !strings.Contains(err.Error(), "requires meta_prompt or role assignment") {
		t.Fatalf("expected missing assignment error, got %v", err)
	}
}

func TestParseTaskCallArgumentsRequiresExplicitLaunchAgent(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"prompt":      "inspect the repo",
		"meta_prompt": "map the relevant files",
	}))
	if err == nil || !strings.Contains(err.Error(), "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected missing subagent error, got %v", err)
	}
}

func TestParseTaskCallArgumentsRejectsLaunchTimeTrustFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{
			name: "top level allow_bash",
			args: map[string]any{
				"prompt":        "inspect the repo",
				"subagent_type": "explorer",
				"meta_prompt":   "map the relevant files",
				"allow_bash":    true,
			},
		},
		{
			name: "per launch execution setting",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{map[string]any{
					"subagent_type":     "explorer",
					"meta_prompt":       "map the relevant files",
					"execution_setting": "readwrite",
				}},
			},
		},
		{
			name: "per launch tool contract",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{map[string]any{
					"subagent_type": "explorer",
					"meta_prompt":   "map the relevant files",
					"tool_contract": map[string]any{"preset": "all"},
				}},
			},
		},
		{
			name: "per launch tool scope",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{map[string]any{
					"subagent_type": "explorer",
					"meta_prompt":   "map the relevant files",
					"tool_scope":    map[string]any{"allow_tools": []any{"bash"}},
				}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTaskCallArguments(mustJSON(t, tc.args))
			if err == nil || !strings.Contains(err.Error(), "cannot set launch-time trust, execution, or tool field") {
				t.Fatalf("expected trust-field rejection, got %v", err)
			}
		})
	}
}

func TestParseTaskCallArgumentsValidLaunches(t *testing.T) {
	parsed, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"description": "repo map",
		"prompt":      "inspect the repo",
		"launches": []any{
			map[string]any{"subagent_type": "explorer", "meta_prompt": "map backend files"},
			map[string]any{"agent": "parallel", "role": "map frontend files"},
		},
	}))
	if err != nil {
		t.Fatalf("parse valid launches: %v", err)
	}
	if len(parsed.Launches) != 2 {
		t.Fatalf("launch count = %d, want 2", len(parsed.Launches))
	}
	if parsed.Launches[0].RequestedSubagentType != "explorer" || parsed.Launches[0].MetaPrompt != "map backend files" {
		t.Fatalf("unexpected first launch: %#v", parsed.Launches[0])
	}
	if parsed.Launches[1].RequestedSubagentType != "parallel" || parsed.Launches[1].MetaPrompt != "map frontend files" {
		t.Fatalf("unexpected second launch: %#v", parsed.Launches[1])
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(encoded)
}

func TestBuildTaskLaunchPermissionPayloadRequiresResolvableSavedAgent(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "missing-agent",
			"meta_prompt":   "map backend files",
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve subagent \"missing-agent\"") {
		t.Fatalf("expected missing saved-agent resolution error, got %v", err)
	}
}

func TestBuildTaskLaunchPermissionPayloadRejectsDisabledSavedAgent(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "disabled-subagent",
			"meta_prompt":   "map backend files",
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "agent \"disabled-subagent\" is disabled") {
		t.Fatalf("expected disabled saved-agent error, got %v", err)
	}
}

func TestBuildTaskLaunchPermissionPayloadUsesResolvedSavedAgentPreference(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "purpose-review",
			"meta_prompt":   "map backend files",
		}),
	})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if manifest.ResolvedAgentName != "reviewer" {
		t.Fatalf("resolved agent = %q, want reviewer", manifest.ResolvedAgentName)
	}
	if manifest.ResolvedAgentError != "" {
		t.Fatalf("resolved agent error = %q, want empty", manifest.ResolvedAgentError)
	}
	if len(manifest.Launches) != 1 {
		t.Fatalf("launch count = %d, want 1", len(manifest.Launches))
	}
	row := manifest.Launches[0]
	if row.RequestedSubagentType != "purpose-review" || row.ResolvedAgentName != "reviewer" {
		t.Fatalf("unexpected launch resolution: %#v", row)
	}
	if row.ResolvedAgentError != "" {
		t.Fatalf("row resolved agent error = %q, want empty", row.ResolvedAgentError)
	}
	if row.SubagentProvider != "static" || row.SubagentModel != "review-model" {
		t.Fatalf("row preference = %q/%q, want static/review-model", row.SubagentProvider, row.SubagentModel)
	}
	if got := row.Capabilities["allow_bash"]; got != false {
		t.Fatalf("allow_bash capability = %#v, want false", got)
	}
}

func newTaskLaunchPermissionTestService(t *testing.T) (*Service, string, func()) {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "state.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cleanup := func() { _ = store.Close() }

	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		cleanup()
		t.Fatalf("open event log: %v", err)
	}
	agents := agentruntime.NewService(pebblestore.NewAgentStore(store), events)
	if err := agents.EnsureDefaults(); err != nil {
		cleanup()
		t.Fatalf("ensure agent defaults: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:             "reviewer",
		Mode:             agentruntime.ModeSubagent,
		Description:      "Review specialist",
		Provider:         "static",
		Model:            "review-model",
		Prompt:           "Review carefully.",
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		Enabled:          pebblestore.BoolPtr(true),
	}); err != nil {
		cleanup()
		t.Fatalf("create reviewer: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:             "disabled-subagent",
		Mode:             agentruntime.ModeSubagent,
		Prompt:           "Disabled.",
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		Enabled:          pebblestore.BoolPtr(false),
	}); err != nil {
		cleanup()
		t.Fatalf("create disabled subagent: %v", err)
	}
	if _, _, _, err := agents.SetActiveSubagent("purpose-review", "reviewer"); err != nil {
		cleanup()
		t.Fatalf("set active subagent mapping: %v", err)
	}

	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	parent, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		Title:         "Parent",
		WorkspacePath: t.TempDir(),
		WorkspaceName: "workspace",
		Mode:          sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "parent-provider",
			Model:    "parent-model",
			Thinking: "high",
		},
	})
	if err != nil {
		cleanup()
		t.Fatalf("create parent session: %v", err)
	}
	return NewService(sessions, nil, nil, nil, nil, agents, nil, events), parent.ID, cleanup
}
