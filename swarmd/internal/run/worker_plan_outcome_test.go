package run

import (
	"strings"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

func TestWorkerPlanStopped(t *testing.T) {
	cases := []struct {
		name string
		doc  *store.SessionPlanDocument
		want string
	}{
		{"no plan", nil, ""},
		{"all completed", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "a", Status: "completed"}}}, ""},
		{"more to come", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "a", Status: "completed"}, {ID: "b", Status: "pending"}}}, ""},
		{"blocked step", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "hello-and-stop", Title: "Say hello, then stop", Status: "blocked", Notes: "manage_workers is unavailable"}}}, `step "Say hello, then stop" (blocked): manage_workers is unavailable`},
		{"failed step", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "a", Status: "failed"}}}, `step "a" (failed)`},
		// The VM's stuck run: the checkpoint still in progress, the execution blocked.
		{"blocked execution", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "hello-and-stop", Status: "in_progress"}}, ExecutionState: &store.SessionPlanExecutionState{Status: "blocked", LastCheckpointID: "hello-and-stop"}}, `step "hello-and-stop" (blocked)`},
		{"running execution", &store.SessionPlanDocument{Checkpoints: []store.SessionPlanCheckpoint{{ID: "a", Status: "in_progress"}}, ExecutionState: &store.SessionPlanExecutionState{Status: "running"}}, ""},
	}
	for _, c := range cases {
		got := workerPlanStopped(c.doc)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Fatalf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
