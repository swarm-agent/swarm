package model

import (
	"testing"
	"time"
)

// Purpose: a model its provider shut down (catalog status retired, or its
// expiration date reached) is never offered, while a deprecated model still
// before its date and an undated one are.
func TestRetiredSnapshotModel(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC).UnixMilli()
	model := func(status, expiration string) swarmSnapshotModel {
		var m swarmSnapshotModel
		m.AvailabilityStatus, m.Lifecycle.ExpirationDate = status, expiration
		return m
	}
	for _, tc := range []struct {
		status, expiration string
		retired            bool
	}{
		{"retired", "", true},
		{"deprecated", "2026-10-22", true},
		{"deprecated", "2026-10-28", true},
		{"deprecated", "2026-11-17", false},
		{"available", "", false},
		{"", "garbage", false},
	} {
		if got := retiredSnapshotModel(model(tc.status, tc.expiration), now); got != tc.retired {
			t.Errorf("status %q expiration %q: retired=%v, want %v", tc.status, tc.expiration, got, tc.retired)
		}
	}
}
