package pebblestore

import "testing"

// Purpose: canonical reviewer settings must be independently changeable through
// the existing targeted update and reject malformed/cross-account writes.
// The real account store is the narrowest authority layer.
func TestReviewerModelSetting(t *testing.T) {
	db := openAgentModelSettingsTestStore(t)
	s := NewAgentModelSettingsStore(db)
	a := AgentModelAssignment{Provider: "p", Model: "coder", Thinking: "high"}
	_, err := s.PutForAccount(AgentModelSettingsRecord{AccountScopeID: "account", Swarm: SwarmAgentModelAssignments{Action: a, Plan: a}, SystemAgents: SystemAgentModelAssignments{Compact: a, Finder: a, Coder: a, Designer: a, Router: a}})
	if err != nil {
		t.Fatal(err)
	}
	review := AgentModelAssignment{Provider: "p", Model: "review", Thinking: "high"}
	updated, err := s.UpdateSystemAgentForAccount("account", "system-reviewer", review, 10)
	if err != nil || updated.SystemAgents.Reviewer != review || updated.SystemAgents.Coder != a {
		t.Fatal(updated, err)
	}
	if _, err := s.UpdateSystemAgentForAccount("account", "reviewer", AgentModelAssignment{Model: "bad"}, 20); err == nil {
		t.Fatal("malformed accepted")
	}
	if _, err := s.UpdateSystemAgentForAccount("foreign", "reviewer", review, 20); err == nil {
		t.Fatal("foreign accepted")
	}
	got, _, err := s.GetForAccount("account")
	if err != nil || got.SystemAgents.Reviewer != review || got.UpdatedAt != 10 {
		t.Fatal(got, err)
	}
}
