package agent

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func newTestService(t *testing.T) (*Service, *pebblestore.AgentStore) {
	t.Helper()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "agents.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	eventLog, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatalf("new event log: %v", err)
	}
	agents := pebblestore.NewAgentStore(store)
	return NewService(agents, eventLog), agents
}

func TestServicePublishesAgentMutationEvents(t *testing.T) {
	svc, _ := newTestService(t)
	published := make([]pebblestore.EventEnvelope, 0, 1)
	svc.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})
	enabled := true
	profile, version, event, err := svc.Upsert(UpsertInput{
		Name:        "publisher-probe",
		Mode:        ModeSubagent,
		Description: "publisher probe",
		Prompt:      "Probe event publishing.",
		Enabled:     &enabled,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if event == nil {
		t.Fatalf("Upsert() returned nil event")
	}
	if len(published) != 1 {
		t.Fatalf("published event count = %d, want 1", len(published))
	}
	if published[0].GlobalSeq != event.GlobalSeq {
		t.Fatalf("published seq = %d, returned seq = %d", published[0].GlobalSeq, event.GlobalSeq)
	}
	if published[0].Stream != "system:agent" {
		t.Fatalf("published stream = %q, want system:agent", published[0].Stream)
	}
	if published[0].EventType != "agent.profile.created" {
		t.Fatalf("published event type = %q, want agent.profile.created", published[0].EventType)
	}
	var payload struct {
		Profile pebblestore.AgentProfile `json:"profile"`
		State   State                    `json:"state"`
		Version int64                    `json:"version"`
	}
	if err := json.Unmarshal(published[0].Payload, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if payload.Profile.Name != profile.Name {
		t.Fatalf("payload profile name = %q, want %q", payload.Profile.Name, profile.Name)
	}
	if payload.Version != version || payload.State.Version != version {
		t.Fatalf("payload version=%d state.version=%d, want %d", payload.Version, payload.State.Version, version)
	}
	found := false
	for _, candidate := range payload.State.Profiles {
		if candidate.Name == profile.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("payload state missing profile %q", profile.Name)
	}
}

func TestServicePublishesCustomToolMutationEvents(t *testing.T) {
	svc, _ := newTestService(t)
	published := make([]pebblestore.EventEnvelope, 0, 2)
	svc.SetEventPublisher(func(event pebblestore.EventEnvelope) {
		published = append(published, event)
	})

	tool, err := svc.PutCustomTool(pebblestore.AgentCustomToolDefinition{
		Name:        "publish_probe",
		Kind:        "fixed_bash",
		Description: "Publish probe",
		Command:     "git status --short",
	})
	if err != nil {
		t.Fatalf("PutCustomTool() error = %v", err)
	}
	if len(published) != 1 {
		t.Fatalf("published event count after put = %d, want 1", len(published))
	}
	if published[0].Stream != "system:agent" || published[0].EventType != "agent.custom_tool.created" {
		t.Fatalf("published put event = %s %s, want system:agent agent.custom_tool.created", published[0].Stream, published[0].EventType)
	}
	var createPayload struct {
		CustomTool pebblestore.AgentCustomToolDefinition `json:"custom_tool"`
		State      State                                 `json:"state"`
	}
	if err := json.Unmarshal(published[0].Payload, &createPayload); err != nil {
		t.Fatalf("decode create payload: %v", err)
	}
	if createPayload.CustomTool.Name != tool.Name || len(createPayload.State.CustomTools) != 1 {
		t.Fatalf("create payload custom tool=%q state tools=%d, want %q and 1", createPayload.CustomTool.Name, len(createPayload.State.CustomTools), tool.Name)
	}

	deleted, err := svc.DeleteCustomTool(tool.Name)
	if err != nil {
		t.Fatalf("DeleteCustomTool() error = %v", err)
	}
	if !deleted {
		t.Fatalf("DeleteCustomTool() deleted = false, want true")
	}
	if len(published) != 2 {
		t.Fatalf("published event count after delete = %d, want 2", len(published))
	}
	if published[1].Stream != "system:agent" || published[1].EventType != "agent.custom_tool.deleted" {
		t.Fatalf("published delete event = %s %s, want system:agent agent.custom_tool.deleted", published[1].Stream, published[1].EventType)
	}
}

func TestResolveAgentAllowsEnabledNonPrimaryProfiles(t *testing.T) {
	svc, agents := newTestService(t)
	if err := agents.PutProfile(pebblestore.AgentProfile{
		Name:             "memory",
		Mode:             ModeSubagent,
		Description:      "Memory profile",
		Provider:         "codex",
		Model:            "gpt-5-codex",
		Thinking:         "high",
		Prompt:           "Remember things.",
		ExecutionSetting: pebblestore.AgentExecutionSettingRead,
		Enabled:          true,
	}); err != nil {
		t.Fatalf("put memory profile: %v", err)
	}

	profile, err := svc.ResolveAgent("memory")
	if err != nil {
		t.Fatalf("resolve any agent: %v", err)
	}
	if profile.Name != "memory" || profile.Mode != ModeSubagent {
		t.Fatalf("resolved profile = %+v", profile)
	}

	if _, err := svc.ResolvePrimary("memory"); err == nil {
		t.Fatalf("ResolvePrimary(memory) unexpectedly succeeded")
	}
}

func TestDeleteSwarmRequiresAnotherPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("swarm"); err == nil || !strings.Contains(err.Error(), "last primary") {
		t.Fatalf("Delete(swarm) with no other primary error = %v, want last primary", err)
	}

	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name:        "replacement",
		Mode:        ModePrimary,
		Description: "replacement primary",
		Prompt:      "Handle primary tasks.",
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("create replacement primary: %v", err)
	}

	result, _, _, err := svc.Delete("swarm")
	if err != nil {
		t.Fatalf("Delete(swarm) with replacement primary error = %v", err)
	}
	if result.Deleted != "swarm" {
		t.Fatalf("deleted = %q, want swarm", result.Deleted)
	}
	if result.ActivePrimary != "replacement" {
		t.Fatalf("active primary after deleting swarm = %q, want replacement", result.ActivePrimary)
	}
	if _, ok, err := svc.GetProfile("swarm"); err != nil || ok {
		t.Fatalf("GetProfile(swarm) after delete ok=%v err=%v, want missing", ok, err)
	}
}

func TestDeletePrimaryRequiresAnotherPrimaryForEveryPrimary(t *testing.T) {
	svc, _ := newTestService(t)
	enabled := true
	if _, _, _, err := svc.Upsert(UpsertInput{
		Name:        "solo",
		Mode:        ModePrimary,
		Description: "only primary",
		Prompt:      "Handle primary tasks.",
		Enabled:     &enabled,
	}); err != nil {
		t.Fatalf("create solo primary: %v", err)
	}
	if _, _, _, err := svc.ActivatePrimary("solo"); err != nil {
		t.Fatalf("activate solo primary: %v", err)
	}

	if _, _, _, err := svc.Delete("solo"); err == nil || !strings.Contains(err.Error(), "last primary") {
		t.Fatalf("Delete(solo) error = %v, want last primary", err)
	}
}

func TestMemoryRemainsProtectedFromDelete(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.EnsureDefaults(); err != nil {
		t.Fatalf("EnsureDefaults() error = %v", err)
	}

	if _, _, _, err := svc.Delete("memory"); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("Delete(memory) error = %v, want protected", err)
	}
}
