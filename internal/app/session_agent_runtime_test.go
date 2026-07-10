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
		"requested_subagent": "explorer",
		"target_kind":        "subagent",
		"target_name":        "explorer",
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
		"lineage_label":      "@explorer",
		"requested_subagent": "stale-request",
	}}

	agent, _, _, _ := resolveSessionEffectiveAgent(summary, client.SessionV3AgentModelPolicy{}, client.AgentState{}, "swarm", "", true, true)
	if agent != "explorer" {
		t.Fatalf("agent = %q, want explorer", agent)
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
