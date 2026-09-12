package pebblestore

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// Requirement: canonical sidechat mutations borrow an authenticated parent lane
// without becoming owners. Threat: a metadata exemption steals a lane, ignores
// reservations, or publishes a rejected child. ApplyV3SessionMutation is the
// narrowest layer proving both the ownership and no-partial-publication contract.
func TestSidechatWorktreeBorrowing(t *testing.T) {
	for _, scenario := range []string{"plan", "ai", "foreign", "stale", "owner-spoof", "nested", "reserved", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewSessionStore(openV3SessionEventTestStore(t))
			path := filepath.Join(t.TempDir(), "lane")
			createRecoverySession(t, s, "owner", path)
			parent, _, _ := s.GetSession("owner")
			parent.Metadata = map[string]any{"swarm_v3_worktree_owner_session_id": "owner"}
			if scenario == "nested" {
				parent.Metadata["lineage_kind"] = "system_sidechat"
			}
			if err := s.UpdateSession(parent); err != nil {
				t.Fatal(err)
			}
			before, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "reserved" {
				claim := before[0]
				claim.ClaimantSessionID = "claimant"
				if err := s.store.PutJSON(worktreeOwnershipKey(path), claim); err != nil {
					t.Fatal(err)
				}
				before[0] = claim
			}
			child := parent
			child.ID = "sidechat"
			child.Metadata = map[string]any{"lineage_kind": "system_sidechat", "system_sidechat_kind": "plan", "parent_session_id": "owner", "swarm_v3_worktree_owner_session_id": "owner"}
			switch scenario {
			case "ai":
				child.Metadata["system_sidechat_kind"] = "ai"
			case "foreign":
				child.AccountScopeID = "foreign"
			case "stale":
				child.WorktreeBranch = "agent/stale"
			case "owner-spoof":
				child.Metadata["swarm_v3_worktree_owner_session_id"] = child.ID
			case "ordinary":
				delete(child.Metadata, "lineage_kind")
			}
			input := V3SessionMutationInput{SessionID: child.ID, UserID: child.UserID, AccountScopeID: child.AccountScopeID, PayloadHash: "child", IdempotencyKey: "create", Kind: V3SessionMutationCreateSession, Session: &child}
			_, err = s.ApplyV3SessionMutation(input)
			valid := scenario == "plan" || scenario == "ai"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				input.Kind, input.IdempotencyKey = V3SessionMutationUpdateMetadata, "rebind"
				if _, err := s.ApplyV3SessionMutation(input); err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, ErrWorktreeRecoveryConflict) {
					t.Fatalf("expected rejection: %v", err)
				}
				if _, exists, err := s.GetSession(child.ID); err != nil || exists {
					t.Fatalf("rejected child published: %t %v", exists, err)
				}
				events, err := s.ListV3SessionEvents(child.ID, 0, 10)
				if err != nil || len(events) != 0 {
					t.Fatalf("rejected events: %v %v", events, err)
				}
			}
			after, err := s.InspectWorktreeOwnership("account", "user", []string{path})
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("ownership changed: %v %v", after, err)
			}
		})
	}
}
