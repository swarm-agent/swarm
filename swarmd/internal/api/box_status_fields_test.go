package api

import "testing"

// Purpose: manage_fleet status on a linked machine carries the same fields as
// the home machine's status: the outbox when it is on or failing, and
// activity_cursor from the signal feed. Threat: the parity defect where the
// linked status lacked both, so an AI could not follow a linked machine's
// activity or see its failing export. Authority: boxStatusFeedFields, used by
// controlMCPStatus (home, swarm_status) and ReadFleetMachineStatus (linked);
// the pure function is the narrowest layer for the derivation rules, and
// TestLinkedMachineReadsLikeMCP proves the linked read over a real link.
func TestBoxStatusFeedFields(t *testing.T) {
	out := map[string]any{}
	boxStatusFeedFields(out, map[string]any{"enabled": true, "pending": float64(2)}, map[string]any{"latest_seq": float64(42)})
	if out["activity_cursor"] != "42" || out["outbox"] == nil {
		t.Fatalf("on outbox and feed: %v", out)
	}
	out = map[string]any{}
	boxStatusFeedFields(out, map[string]any{"enabled": false, "failing": true}, nil)
	if out["outbox"] == nil || out["activity_cursor"] != nil {
		t.Fatalf("failing outbox, no feed: %v", out)
	}
	// An outbox that is off and not failing, and a feed without a readable
	// position, add nothing.
	out = map[string]any{}
	boxStatusFeedFields(out, map[string]any{"enabled": false}, map[string]any{"latest_seq": "7"})
	if len(out) != 0 {
		t.Fatalf("off outbox, unreadable feed: %v", out)
	}
	boxStatusFeedFields(out, nil, map[string]any{"latest_seq": float64(-1)})
	if len(out) != 0 {
		t.Fatalf("negative feed position: %v", out)
	}
}
