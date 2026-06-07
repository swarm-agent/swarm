package run

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
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

func TestParseTaskCallArgumentsRequiresPerLaunchAgentAndAssignment(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "missing assignment",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{"subagent_type": "explorer"},
				},
			},
			want: "task launches[0] requires meta_prompt or role assignment",
		},
		{
			name: "missing agent",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{"meta_prompt": "map the relevant files"},
				},
			},
			want: "task launches[0] requires subagent_type, agent, or purpose",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTaskCallArguments(mustJSON(t, tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestParseTaskCallArgumentsRejectsLaunchTimeTrustFields(t *testing.T) {
	tests := []struct {
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
				"launches": []any{
					map[string]any{
						"subagent_type":     "explorer",
						"meta_prompt":       "map the relevant files",
						"execution_setting": "readwrite",
					},
				},
			},
		},
		{
			name: "per launch tool contract",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{
						"subagent_type": "explorer",
						"meta_prompt":   "map the relevant files",
						"tool_contract": map[string]any{"preset": "all"},
					},
				},
			},
		},
		{
			name: "per launch tool scope",
			args: map[string]any{
				"prompt": "inspect the repo",
				"launches": []any{
					map[string]any{
						"subagent_type": "explorer",
						"meta_prompt":   "map the relevant files",
						"tool_scope":    map[string]any{"allow_tools": []any{"bash"}},
					},
				},
			},
		},
	}
	for _, tc := range tests {
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

func TestTaskAssignmentLabelPreservesMoreTitleContext(t *testing.T) {
	label := taskAssignmentLabel("", "Write a quick poem about the sea with a bright moon and quiet tide", "", "memory")
	want := "Write a quick poem about the sea with a bright moon and quiet tide"
	if label != want {
		t.Fatalf("label = %q, want %q", label, want)
	}
	if strings.Contains(label, "...") {
		t.Fatalf("label should preserve 12-word title without ellipsis: %q", label)
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

func TestParseTaskCallArgumentsRejectsMalformedParameterMarkupBeforeLaunchValidation(t *testing.T) {
	_, err := parseTaskCallArguments(mustJSON(t, map[string]any{
		"description": "Test 2 — Valid multi-subagent launch",
		"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
		"launches": []any{
			map[string]any{"subagent_type": "explorer", "meta_prompt": "Map top-level directories."},
		},
	}))
	if err == nil || !strings.Contains(err.Error(), "malformed XML markup in tool call") {
		t.Fatalf("expected malformed XML markup error, got %v", err)
	}
	if strings.Contains(err.Error(), "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %v", err)
	}
}

func TestPermissionArgumentsForCallRejectsMalformedTaskLaunch(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	_, err := svc.permissionArgumentsForCall(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "Test 2 — Valid multi-subagent launch",
			"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
			},
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "malformed XML markup in tool call") {
		t.Fatalf("expected malformed XML markup error, got %v", err)
	}
}

func TestPermissionArgumentsForCallFormatsValidTaskLaunchManifest(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	formatted, err := svc.permissionArgumentsForCall(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "repo map",
			"prompt":      "inspect the repo",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "map backend files"},
				map[string]any{"purpose": "purpose-review", "role": "review frontend files"},
			},
		}),
	})
	if err != nil {
		t.Fatalf("format permission arguments: %v", err)
	}
	var manifest taskLaunchManifest
	if err := json.Unmarshal([]byte(formatted), &manifest); err != nil {
		t.Fatalf("formatted manifest json invalid: %v", err)
	}
	if manifest.PathID != taskLaunchPermissionPathID {
		t.Fatalf("path id = %q, want %q", manifest.PathID, taskLaunchPermissionPathID)
	}
	if manifest.LaunchCount != 2 || len(manifest.Launches) != 2 {
		t.Fatalf("launch count = %d len=%d, want 2", manifest.LaunchCount, len(manifest.Launches))
	}
	if manifest.Launches[0].MetaPrompt != "map backend files" || manifest.Launches[1].MetaPrompt != "review frontend files" {
		t.Fatalf("unexpected launch assignments: %#v", manifest.Launches)
	}
	if manifest.Launches[0].ResolvedAgentName == "" || manifest.Launches[0].SubagentProvider == "" || manifest.Launches[0].SubagentModel == "" || manifest.Launches[0].ResolvedTools == nil {
		t.Fatalf("first launch missing resolved display data: %#v", manifest.Launches[0])
	}
}

func TestGateToolCallsRejectsMalformedTaskLaunchBeforeApproval(t *testing.T) {
	svc, parentSessionID, permissions, cleanup := newTaskLaunchPermissionServiceWithPermissions(t)
	defer cleanup()

	results, approvedCalls, _, approvedMask, _, err := svc.gateToolCalls(context.Background(), parentSessionID, "run-test", 1, sessionruntime.ModeAuto, []tool.Call{{
		CallID: "call-bad-task",
		Name:   "task",
		Arguments: mustJSON(t, map[string]any{
			"description": "Test 2 — Valid multi-subagent launch",
			"prompt":      "Execute your assigned meta_prompt/role. </怡parameter>",
			"launches": []any{
				map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
			},
		}),
	}}, nil, nil)
	if err != nil {
		t.Fatalf("gate tool calls returned unexpected error: %v", err)
	}
	if len(approvedCalls) != 0 || len(approvedMask) != 1 || approvedMask[0] {
		t.Fatalf("malformed task launch was approved: calls=%d mask=%v", len(approvedCalls), approvedMask)
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "invalid tool arguments") || !strings.Contains(results[0].Error, "malformed XML markup in tool call") {
		t.Fatalf("expected parser-level result error, got %#v", results)
	}
	if strings.Contains(results[0].Error, "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %#v", results)
	}
	pending, err := permissions.ListPending(parentSessionID, 10)
	if err != nil {
		t.Fatalf("list pending permissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending permission records, got %#v", pending)
	}
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

func TestBuildTaskLaunchPermissionPayloadIncludesResolvedToolSummary(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	manifest, err := svc.buildTaskLaunchPermissionPayload(parentSessionID, sessionruntime.ModeAuto, tool.Call{
		Name: "task",
		Arguments: mustJSON(t, map[string]any{
			"description":   "repo map",
			"prompt":        "inspect the repo",
			"subagent_type": "reviewer",
			"meta_prompt":   "map backend files",
		}),
	})
	if err != nil {
		t.Fatalf("build permission payload: %v", err)
	}
	if manifest.ResolvedTools == nil {
		t.Fatalf("manifest resolved tools is nil")
	}
	if manifest.ResolvedTools.Preset != "bash_git_only" {
		t.Fatalf("manifest resolved tool preset = %q, want bash_git_only", manifest.ResolvedTools.Preset)
	}
	if len(manifest.Launches) != 1 || manifest.Launches[0].ResolvedTools == nil {
		t.Fatalf("launch resolved tools missing: %#v", manifest.Launches)
	}
	tools := manifest.Launches[0].ResolvedTools
	if tools.Preset != "bash_git_only" {
		t.Fatalf("preset = %q, want bash_git_only", tools.Preset)
	}
	if tools.RuntimeMode != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("runtime mode = %q, want readwrite", tools.RuntimeMode)
	}
	if tools.EffectiveExecutionMode != pebblestore.AgentExecutionSettingReadWrite {
		t.Fatalf("effective execution mode = %q, want readwrite", tools.EffectiveExecutionMode)
	}
	for _, want := range []string{"read", "search", "list", "skill_use"} {
		if !stringSliceContains(tools.AllowedTools, want) {
			t.Fatalf("allowed tools %v missing %q", tools.AllowedTools, want)
		}
	}
	for _, want := range []string{"bash", "task", "plan_manage", "exit_plan_mode"} {
		if !stringSliceContains(tools.DisabledTools, want) {
			t.Fatalf("disabled tools %v missing %q", tools.DisabledTools, want)
		}
	}
	for _, want := range []string{"git status", "git diff --"} {
		if !stringSliceContains(tools.BashPrefixes, want) {
			t.Fatalf("bash prefixes %v missing %q", tools.BashPrefixes, want)
		}
	}
	if stringSliceContains(tools.AllowedTools, "bash") {
		t.Fatalf("allowed tools include bash despite task launch disabled overlay: %v", tools.AllowedTools)
	}
}

func TestPrepareDelegatedSubagentLaunchCreatesCanonicalV3ChildSession(t *testing.T) {
	svc, _, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()

	const accountScopeID = "account-cp7"
	const userID = "user-cp7"
	if _, _, _, err := svc.agents.UpsertForAccount(accountScopeID, agentruntime.UpsertInput{
		Name:                "reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Review specialist",
		Provider:            "static",
		Model:               "review-model",
		Prompt:              "Review carefully.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		Enabled:             pebblestore.BoolPtr(true),
	}); err != nil {
		t.Fatalf("create account-scoped reviewer: %v", err)
	}
	if _, _, _, err := svc.agents.SetActiveSubagentForAccount(accountScopeID, "purpose-review", "reviewer"); err != nil {
		t.Fatalf("set account-scoped active subagent: %v", err)
	}
	parent, _, err := svc.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{
		UserID:         userID,
		AccountScopeID: accountScopeID,
		Title:          "Parent",
		WorkspacePath:  t.TempDir(),
		WorkspaceName:  "workspace",
		Mode:           sessionruntime.ModeAuto,
		Preference: &pebblestore.ModelPreference{
			Provider: "parent-provider",
			Model:    "parent-model",
			Thinking: "high",
		},
	})
	if err != nil {
		t.Fatalf("create account-scoped parent session: %v", err)
	}

	launch, err := svc.prepareDelegatedSubagentLaunch(parent, sessionruntime.ModePlan, taskLaunchPrepared{
		LaunchIndex:       7,
		RequestedSubagent: "purpose-review",
		MetaPrompt:        "Map backend files",
	}, "repo map", "")
	if err != nil {
		t.Fatalf("prepare delegated launch: %v", err)
	}

	childID := strings.TrimSpace(launch.ChildSession.ID)
	if childID == "" {
		t.Fatalf("child session id is empty")
	}
	if childID == parent.ID {
		t.Fatalf("child session reused parent id %q", childID)
	}
	if strings.Contains(childID, "/") || strings.Contains(childID, ":") {
		t.Fatalf("child session id %q is not a raw canonical session id", childID)
	}
	child, ok, err := svc.sessions.GetSession(childID)
	if err != nil {
		t.Fatalf("load child session: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q was not persisted", childID)
	}
	if child.ID != launch.ChildSession.ID {
		t.Fatalf("persisted child id = %q, launch id = %q", child.ID, launch.ChildSession.ID)
	}
	if child.Mode != sessionruntime.ModeAuto || launch.ChildMode != sessionruntime.ModeAuto {
		t.Fatalf("child mode = %q launch mode = %q, want auto", child.Mode, launch.ChildMode)
	}
	if child.AccountScopeID != parent.AccountScopeID {
		t.Fatalf("child account scope = %q, want parent account scope %q", child.AccountScopeID, parent.AccountScopeID)
	}
	if child.UserID != parent.UserID {
		t.Fatalf("child user id = %q, want parent user id %q", child.UserID, parent.UserID)
	}
	if child.WorkspacePath != parent.WorkspacePath || child.WorkspaceName != parent.WorkspaceName {
		t.Fatalf("child workspace = %q/%q, want parent workspace %q/%q", child.WorkspacePath, child.WorkspaceName, parent.WorkspacePath, parent.WorkspaceName)
	}
	if child.Preference.Provider != "static" || child.Preference.Model != "review-model" {
		t.Fatalf("child preference = %q/%q, want static/review-model", child.Preference.Provider, child.Preference.Model)
	}

	metadata := child.Metadata
	checks := map[string]string{
		"parent_session_id":  parent.ID,
		"parent_title":       parent.Title,
		"lineage_kind":       "delegated_subagent",
		"lineage_label":      "@reviewer",
		"launch_source":      "task",
		"requested_subagent": "purpose-review",
		"subagent":           "reviewer",
		"assignment_label":   "Map backend files",
		"subagent_provider":  "static",
		"subagent_model":     "review-model",
		"runtime_state":      "standby",
	}
	for key, want := range checks {
		if got := strings.TrimSpace(metadataStringForTest(metadata, key)); got != want {
			t.Fatalf("metadata[%q] = %q, want %q; metadata=%#v", key, got, want, metadata)
		}
	}
	if got, ok := metadata["title_locked"].(bool); !ok || !got {
		t.Fatalf("metadata[title_locked] = %#v, want true", metadata["title_locked"])
	}
	if got, ok := metadata["title_pending"].(bool); !ok || got {
		t.Fatalf("metadata[title_pending] = %#v, want false", metadata["title_pending"])
	}
	if got := metadataStringForTest(metadata, "launch_index"); got != "7" {
		t.Fatalf("metadata[launch_index] = %q, want 7", got)
	}
	if got := strings.TrimSpace(metadataStringForTest(metadata, "workspace_id")); got == "" {
		t.Fatalf("metadata[workspace_id] is empty")
	}

	hydrated, ok, err := svc.sessions.HydrateSessionSnapshot(childID, 500, 500)
	if err != nil {
		t.Fatalf("hydrate child session through V3 snapshot path: %v", err)
	}
	if !ok {
		t.Fatalf("child session %q not found through V3 snapshot path", childID)
	}
	if hydrated.Session.ID != childID {
		t.Fatalf("hydrated session id = %q, want %q", hydrated.Session.ID, childID)
	}
	if got := strings.TrimSpace(metadataStringForTest(hydrated.Session.Metadata, "parent_session_id")); got != parent.ID {
		t.Fatalf("hydrated parent_session_id = %q, want %q", got, parent.ID)
	}
	if len(hydrated.Events) != 1 || hydrated.Events[0].EventType != "session.created" || hydrated.Events[0].SessionID != childID {
		t.Fatalf("hydrated V3 events = %#v, want single child session.created", hydrated.Events)
	}
	var createdPayload struct {
		Session *pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(hydrated.Events[0].Payload, &createdPayload); err != nil {
		t.Fatalf("decode V3 session.created payload: %v", err)
	}
	if createdPayload.Session == nil || createdPayload.Session.ID != childID {
		t.Fatalf("V3 session.created payload session = %#v, want child %q", createdPayload.Session, childID)
	}
	if got := strings.TrimSpace(metadataStringForTest(createdPayload.Session.Metadata, "parent_session_id")); got != parent.ID {
		t.Fatalf("V3 session.created parent_session_id = %q, want %q", got, parent.ID)
	}
	if got := strings.TrimSpace(metadataStringForTest(createdPayload.Session.Metadata, "lineage_kind")); got != "delegated_subagent" {
		t.Fatalf("V3 session.created lineage_kind = %q, want delegated_subagent", got)
	}
}

func metadataStringForTest(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return typed
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
		Name:                "reviewer",
		Mode:                agentruntime.ModeSubagent,
		Description:         "Review specialist",
		Provider:            "static",
		Model:               "review-model",
		Prompt:              "Review carefully.",
		RuntimeMode:         pebblestore.AgentRuntimeModeReadWrite,
		ExecutionSetting:    pebblestore.AgentExecutionSettingReadWrite,
		ExitPlanModeEnabled: pebblestore.BoolPtr(false),
		ToolContract: &pebblestore.AgentToolContract{
			Preset: "bash_git_only",
			Tools: map[string]pebblestore.AgentToolConfig{
				"bash": {BashPrefixes: []string{"git status", "git diff --"}},
			},
		},
		Enabled: pebblestore.BoolPtr(true),
	}); err != nil {
		cleanup()
		t.Fatalf("create reviewer: %v", err)
	}
	if _, _, _, err := agents.Upsert(agentruntime.UpsertInput{
		Name:             "disabled-subagent",
		Mode:             agentruntime.ModeSubagent,
		Prompt:           "Disabled.",
		RuntimeMode:      pebblestore.AgentRuntimeModeRead,
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
	return NewService(sessions, nil, nil, tool.NewRuntime(1), nil, agents, nil, events), parent.ID, cleanup
}

func TestGateToolCallsRejectsMalformedXMLToolArgumentsWithoutPermissionService(t *testing.T) {
	svc, parentSessionID, cleanup := newTaskLaunchPermissionTestService(t)
	defer cleanup()
	if svc.permissions != nil {
		t.Fatalf("test helper unexpectedly configured permissions")
	}
	arguments := mustJSON(t, map[string]any{
		"prompt": "Execute assigned work. </怡parameter>",
		"launches": []any{
			map[string]any{"subagent_type": "reviewer", "meta_prompt": "Map top-level directories."},
		},
	})

	results, approvedCalls, _, approvedMask, _, err := svc.gateToolCalls(context.Background(), parentSessionID, "run-test", 1, sessionruntime.ModeAuto, []tool.Call{{
		CallID:    "call-bad-task-xml",
		Name:      "task",
		Arguments: arguments,
	}}, nil, nil)
	if err != nil {
		t.Fatalf("gate tool calls returned unexpected error: %v", err)
	}
	if len(approvedCalls) != 0 || len(approvedMask) != 1 || approvedMask[0] {
		t.Fatalf("malformed XML task call was approved: calls=%d mask=%v", len(approvedCalls), approvedMask)
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "invalid tool arguments") || !strings.Contains(results[0].Error, "malformed XML markup in tool call") {
		t.Fatalf("expected parser-level XML error, got %#v", results)
	}
	if strings.Contains(results[0].Error, "requires subagent_type, agent, or purpose") {
		t.Fatalf("expected parser-level error before launch validation, got %#v", results)
	}
}

func newTaskLaunchPermissionServiceWithPermissions(t *testing.T) (*Service, string, *permission.Service, func()) {
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
		Prompt:           "Review carefully.",
		RuntimeMode:      pebblestore.AgentRuntimeModeRead,
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		Enabled:          pebblestore.BoolPtr(true),
	}); err != nil {
		cleanup()
		t.Fatalf("create reviewer: %v", err)
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
	permissions := permission.NewService(pebblestore.NewPermissionStore(store), events, nil)
	svc := NewService(sessions, nil, nil, tool.NewRuntime(1), permissions, agents, nil, events)
	return svc, parent.ID, permissions, cleanup
}
