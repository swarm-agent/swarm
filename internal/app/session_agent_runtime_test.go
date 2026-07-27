package app

import (
	"encoding/json"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

func TestResolveSessionEffectiveAgentPolicyOverridesStaleMetadata(t *testing.T) {
	exitPlan := false
	state := client.AgentState{Profiles: []client.AgentProfile{{
		Name: "reviewer", ExecutionSetting: "read", ExitPlanModeEnabled: &exitPlan,
	}}}
	summary := model.SessionSummary{Metadata: map[string]any{
		"requested_subagent": "stale-request",
		"target_name":        "stale-target",
		"agent_name":         "stale-agent",
	}}

	agent, execution, canExitPlan, known := resolveSessionEffectiveAgent(summary, client.SessionV3AgentModelPolicy{
		ResolvedAgent: "reviewer",
	}, state, "swarm", "readwrite", true, true)

	if agent != "reviewer" || execution != "read" || canExitPlan || !known {
		t.Fatalf("resolved runtime = (%q, %q, %v, %v), want reviewer/read/false/true", agent, execution, canExitPlan, known)
	}
}

func TestResolveSessionEffectiveAgentIgnoresRequestMetadataForPrimarySession(t *testing.T) {
	summary := model.SessionSummary{Metadata: map[string]any{
		"requested_subagent": "finder",
		"target_kind":        "subagent",
		"target_name":        "finder",
		"agent_name":         "old-agent",
	}}

	agent, execution, canExitPlan, known := resolveSessionEffectiveAgent(summary, client.SessionV3AgentModelPolicy{}, client.AgentState{}, "swarm", "readwrite", true, true)
	if agent != "swarm" || execution != "readwrite" || !canExitPlan || !known {
		t.Fatalf("resolved runtime = (%q, %q, %v, %v), want primary fallback", agent, execution, canExitPlan, known)
	}
}

func TestResolveSessionEffectiveAgentUsesGenuineChildLineage(t *testing.T) {
	summary := model.SessionSummary{Metadata: map[string]any{
		"parent_session_id":  "parent-1",
		"lineage_label":      "@finder",
		"requested_subagent": "stale-request",
	}}

	agent, _, _, _ := resolveSessionEffectiveAgent(summary, client.SessionV3AgentModelPolicy{}, client.AgentState{}, "swarm", "", true, true)
	if agent != "finder" {
		t.Fatalf("agent = %q, want finder", agent)
	}
}

func TestApplyTUISessionStoreToChatRefreshesModeAndEffectiveModel(t *testing.T) {
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SessionsByID: map[string]client.SessionSummary{"session-1": {ID: "session-1", SessionAPI: "v3", Mode: "auto"}},
		PreferencesBySession: map[string]client.ModelPreference{"session-1": {
			Provider: "codex", Model: "auto-model", Thinking: "medium", ServiceTier: "fast", ContextMode: "session",
		}},
		AgentModelPolicyBySession: map[string]client.SessionV3AgentModelPolicy{"session-1": {ContextWindow: 240000}},
		SessionOrder:              []string{"session-1"},
	})
	app := &App{
		tuiSessionStore: store,
		homeModel:       model.HomeModel{ActiveAgent: "swarm", ActiveAgentExitPlanMode: true, ActiveAgentRuntimeKnown: true},
	}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{
		SessionID: "session-1", SessionMode: "plan", ModelProvider: "codex", ModelName: "plan-model", ThinkingLevel: "xhigh",
		Meta: ui.ChatSessionMeta{Agent: "swarm"},
	})

	app.applyTUISessionStoreToChat("session-1")

	if got := app.chat.SessionMode(); got != "auto" {
		t.Fatalf("SessionMode = %q, want auto", got)
	}
	provider, modelName, thinking, tier, contextMode := app.chat.ModelState()
	if provider != "codex" || modelName != "auto-model" || thinking != "medium" || tier != "fast" || contextMode != "session" {
		t.Fatalf("ModelState = (%q, %q, %q, %q, %q), want hydrated auto preference", provider, modelName, thinking, tier, contextMode)
	}
}

func TestRealtimeAgentPolicyEventUpdatesOpenChatFooterRuntime(t *testing.T) {
	exitPlan := false
	store := newTUISessionStore()
	store.ResetFromWorkset(client.SessionV3Workset{
		SessionsByID: map[string]client.SessionSummary{"session-1": {ID: "session-1", SessionAPI: "v3"}},
		SessionOrder: []string{"session-1"},
	})
	app := &App{
		tuiSessionStore: store,
		homeModel:       model.HomeModel{ActiveAgent: "swarm", ActiveAgentExitPlanMode: true, ActiveAgentRuntimeKnown: true},
		agentState:      client.AgentState{Profiles: []client.AgentProfile{{Name: "reviewer", ExecutionSetting: "read", ExitPlanModeEnabled: &exitPlan}}},
	}
	app.chat = ui.NewChatPage(ui.ChatPageOptions{SessionID: "session-1", SessionMode: "auto", Meta: ui.ChatSessionMeta{Agent: "swarm"}})
	payload, err := json.Marshal(map[string]any{"agent_model_policy": client.SessionV3AgentModelPolicy{ResolvedAgent: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	store.ApplyRealtimeFrame(client.V3RealtimeFrame{Kind: "event", SessionID: "session-1", Event: &client.SessionV3Event{SessionID: "session-1", Seq: 1, EventType: "session.agent_model_policy.updated", Payload: payload}})

	app.applyTUISessionStoreToChat("session-1")
	meta := app.chat.Meta()
	if meta.Agent != "reviewer" || meta.AgentExecutionSetting != "read" || meta.AgentExitPlanMode || !meta.AgentRuntimeKnown {
		t.Fatalf("chat runtime = %#v", meta)
	}
}

func TestActiveAgentRuntimeUsesCanonicalRuntimeMode(t *testing.T) {
	legacyExitDisabled := false
	state := client.AgentState{
		ActivePrimary: "swarm",
		Profiles: []client.AgentProfile{
			{
				Name:                "swarm",
				RuntimeMode:         "plan_auto",
				ExecutionSetting:    "readwrite",
				ExitPlanModeEnabled: &legacyExitDisabled,
			},
		},
	}

	agent, execution, canExitPlan, known := activeAgentRuntime(state)
	if agent != "swarm" || execution != "" || !canExitPlan || !known {
		t.Fatalf("active runtime = (%q, %q, %v, %v), want swarm/empty/true/true", agent, execution, canExitPlan, known)
	}
}

func TestApplyActiveAgentModelsHydratesPlanAutoSplit(t *testing.T) {
	state := client.AgentState{
		ActivePrimary: "swarm",
		Profiles: []client.AgentProfile{{
			Name: "swarm", RuntimeMode: "plan_auto", ModelMode: "split", Provider: "codex",
			PlanModel: "plan-model", PlanThinking: "xhigh", PlanServiceTier: "flex",
			AutoProvider: "openrouter", AutoModel: "auto-model", AutoThinking: "medium", AutoServiceTier: "fast",
		}},
	}

	got := applyActiveAgentModels(model.HomeModel{}, state)
	if got.PlanModelProvider != "codex" || got.PlanModelName != "plan-model" || got.PlanThinkingLevel != "xhigh" || got.PlanServiceTier != "flex" {
		t.Fatalf("plan model = (%q, %q, %q, %q)", got.PlanModelProvider, got.PlanModelName, got.PlanThinkingLevel, got.PlanServiceTier)
	}
	if got.AutoModelProvider != "openrouter" || got.AutoModelName != "auto-model" || got.AutoThinkingLevel != "medium" || got.AutoServiceTier != "fast" {
		t.Fatalf("auto model = (%q, %q, %q, %q)", got.AutoModelProvider, got.AutoModelName, got.AutoThinkingLevel, got.AutoServiceTier)
	}
}

func TestApplyActiveAgentModelsHydratesPlanAutoSingle(t *testing.T) {
	state := client.AgentState{
		ActivePrimary: "swarm",
		Profiles: []client.AgentProfile{{
			Name: "swarm", RuntimeMode: "plan_auto", ModelMode: "single", Provider: "codex", Model: "single-model", Thinking: "high",
		}},
	}

	got := applyActiveAgentModels(model.HomeModel{ServiceTier: "priority", ContextMode: "long"}, state)
	if got.PlanModelProvider != "codex" || got.AutoModelProvider != "codex" || got.PlanModelName != "single-model" || got.AutoModelName != "single-model" {
		t.Fatalf("single effective models = (%q, %q, %q, %q)", got.PlanModelProvider, got.PlanModelName, got.AutoModelProvider, got.AutoModelName)
	}
	if got.PlanThinkingLevel != "high" || got.AutoThinkingLevel != "high" || got.PlanContextMode != "long" || got.AutoContextMode != "long" {
		t.Fatalf("single effective settings = (%q, %q, %q, %q)", got.PlanThinkingLevel, got.AutoThinkingLevel, got.PlanContextMode, got.AutoContextMode)
	}
}

func TestApplyActiveAgentModelsKeepsSavedProfileSelections(t *testing.T) {
	state := client.AgentState{ActivePrimary: "swarm", Profiles: []client.AgentProfile{{
		Name: "swarm", RuntimeMode: "plan_auto", ModelMode: "split", PlanModel: "agent-plan", AutoModel: "agent-auto",
	}}}
	want := model.HomeModel{
		ActiveModelProfile: model.ActiveModelProfile{Source: "saved"},
		PlanModelProvider:  "codex", PlanModelName: "profile-plan",
		AutoModelProvider: "openrouter", AutoModelName: "profile-auto",
	}
	got := applyActiveAgentModels(want, state)
	if got.PlanModelName != "profile-plan" || got.AutoModelName != "profile-auto" {
		t.Fatalf("saved profile selections overwritten: plan=%q auto=%q", got.PlanModelName, got.AutoModelName)
	}
}

func TestActiveAgentRuntimeUsesSingleRuntimeMode(t *testing.T) {
	legacyExitEnabled := true
	state := client.AgentState{
		ActivePrimary: "reviewer",
		Profiles: []client.AgentProfile{
			{
				Name:                "reviewer",
				RuntimeMode:         "read",
				ExitPlanModeEnabled: &legacyExitEnabled,
			},
		},
	}

	agent, execution, canExitPlan, known := activeAgentRuntime(state)
	if agent != "reviewer" || execution != "read" || canExitPlan || !known {
		t.Fatalf("active runtime = (%q, %q, %v, %v), want reviewer/read/false/true", agent, execution, canExitPlan, known)
	}
}
