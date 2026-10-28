package run

import (
	"context"
	"testing"

	"swarm/packages/swarmd/internal/permission"
)

// Requirement: only a worker_noninteractive refusal of a real tool call ends
// a worker run that asked to fail fast. ask_user (an input boundary), ordinary
// denials, and runs that did not ask stay ordinary tool results.
// Threat: a worker that asks a question, or hits an explicit deny rule, is
// failed instead of continuing with its own judgement; or a non-worker run is
// ended by a refusal it never opted into.
// Owner: recordUnattendedDenial, read by executeProviderManagedToolCall. A
// unit test is the narrowest layer: the recorder is a pure context value.
func TestRecordUnattendedDenialOnlyEndsOnRealToolRefusals(t *testing.T) {
	refusal := permission.AuthorizationResult{Decision: permission.AuthorizationDeny, Source: permission.WorkerNonInteractiveSource, Reason: "bash denied: unattended worker jobs cannot request interactive permission (bash)"}

	recorder := &unattendedDenialRecorder{}
	ctx := withUnattendedDenialRecorder(context.Background(), recorder)
	recordUnattendedDenial(ctx, "ask_user", refusal)
	recordUnattendedDenial(ctx, "bash", permission.AuthorizationResult{Decision: permission.AuthorizationDeny, Source: "rule", Reason: "denied by deny tool: bash"})
	if got := recorder.recorded(); got != nil {
		t.Fatalf("ask_user or an explicit deny ended the run: %v", got)
	}
	recordUnattendedDenial(ctx, "bash", refusal)
	recordUnattendedDenial(ctx, "write", permission.AuthorizationResult{Decision: permission.AuthorizationDeny, Source: permission.WorkerNonInteractiveSource, Reason: "second"})
	if got := recorder.recorded(); got == nil || got.Tool != "bash" || got.Error() != refusal.Reason {
		t.Fatalf("recorded %+v, want the first bash refusal", got)
	}

	// A run that did not ask to fail fast records nothing and does not panic.
	recordUnattendedDenial(context.Background(), "bash", refusal)
	var none *unattendedDenialRecorder
	if none.recorded() != nil {
		t.Fatal("nil recorder reported a denial")
	}
}
