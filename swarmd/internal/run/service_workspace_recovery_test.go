package run

import (
	"errors"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

// Purpose: authorizedRecoveryCandidates must gate file inspection on an exact
// account-owned durable claim. This callback-level test is the narrowest layer
// proving missing/foreign lanes never reach Git/content reads or attribution.
func TestRecoveryInventoryAuthorizesBeforeRead(t *testing.T) {
	reads := 0
	rows, err := authorizedRecoveryCandidates([]string{"missing", "foreign", "owned"}, func(path string) ([]pebblestore.WorktreeOwnership, error) {
		if path != "owned" {
			return nil, pebblestore.ErrWorktreeRecoveryConflict
		}
		return []pebblestore.WorktreeOwnership{{Path: path, OwnerSessionID: "owner", Revision: 7}}, nil
	}, func(path string) (worktreeruntime.RecoveryIdentity, error) {
		reads++
		if path != "owned" {
			t.Fatal("unauthorized content read")
		}
		return worktreeruntime.RecoveryIdentity{Path: path, HEAD: "head", Fingerprint: "fingerprint"}, nil
	})
	if err != nil || reads != 1 || len(rows) != 1 || rows[0].OwnerSessionID != "owner" || rows[0].OwnershipRevision != 7 || rows[0].Fingerprint != "fingerprint" {
		t.Fatalf("inventory = %+v, reads=%d, err=%v", rows, reads, err)
	}
}

// Purpose: storage failures and mismatched exact claims must not be interpreted
// as authorized inventory; the consumer must fail without opening the candidate.
func TestRecoveryInventoryRejectsFailedOrMismatchedClaims(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		rows, err := authorizedRecoveryCandidates([]string{"candidate"}, func(string) ([]pebblestore.WorktreeOwnership, error) {
			if mismatch {
				return []pebblestore.WorktreeOwnership{{Path: "different"}}, nil
			}
			return nil, errors.New("store unavailable")
		}, func(string) (worktreeruntime.RecoveryIdentity, error) {
			t.Fatal("read after failed authorization")
			return worktreeruntime.RecoveryIdentity{}, nil
		})
		if err == nil || rows != nil {
			t.Fatalf("failed authorization returned rows=%+v err=%v", rows, err)
		}
	}
}
