package environments

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeploymentLease_Valid(t *testing.T) {
	lease := &DeploymentLease{
		ID:             "lease-1",
		AccountScopeID: "acct-1",
		WorkspaceID:    "ws-1",
		DeploymentID:   "dep-1",
		EnvironmentID:  "env-1",
		ConsumerType:   ConsumerTypeSession,
		ConsumerID:     "sess-abc",
		ConsumerMetadata: map[string]string{
			"run_id": "run-123",
			"origin": "user_chat",
		},
		AcquiredAt: 1700000000000,
		ExpiresAt:  1700003600000,
		Active:     true,
	}

	if err := lease.Validate(); err != nil {
		t.Fatalf("expected valid lease, got: %v", err)
	}

	// JSON roundtrip
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var roundtrip DeploymentLease
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if roundtrip.ID != lease.ID || roundtrip.ConsumerID != lease.ConsumerID || !roundtrip.Active {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", roundtrip, lease)
	}
	if roundtrip.ConsumerMetadata["run_id"] != "run-123" {
		t.Fatalf("metadata mismatch: %+v", roundtrip.ConsumerMetadata)
	}
}

func TestDeploymentLease_ConsumerTypes(t *testing.T) {
	types := []ConsumerType{
		ConsumerTypeSession,
		ConsumerTypeTestRun,
		ConsumerTypeWorker,
		ConsumerTypeCustom,
	}

	for _, ct := range types {
		lease := &DeploymentLease{
			ID:             "l-1",
			AccountScopeID: "a-1",
			WorkspaceID:    "w-1",
			DeploymentID:   "d-1",
			EnvironmentID:  "e-1",
			ConsumerType:   ct,
			ConsumerID:     "c-1",
			AcquiredAt:     1700000000000,
			Active:         true,
		}
		if err := lease.Validate(); err != nil {
			t.Fatalf("expected consumer type %q to be valid, got: %v", ct, err)
		}
	}
}

func TestDeploymentLease_Validation_Errors(t *testing.T) {
	validLease := func() *DeploymentLease {
		return &DeploymentLease{
			ID:             "l-1",
			AccountScopeID: "a-1",
			WorkspaceID:    "w-1",
			DeploymentID:   "d-1",
			EnvironmentID:  "e-1",
			ConsumerType:   ConsumerTypeWorker,
			ConsumerID:     "worker-1",
			AcquiredAt:     1700000000000,
			Active:         true,
		}
	}

	tests := []struct {
		name    string
		mutate  func(l *DeploymentLease)
		wantErr string
	}{
		{
			name: "missing id",
			mutate: func(l *DeploymentLease) {
				l.ID = ""
			},
			wantErr: "lease id cannot be empty",
		},
		{
			name: "missing account scope id",
			mutate: func(l *DeploymentLease) {
				l.AccountScopeID = ""
			},
			wantErr: "account_scope_id cannot be empty",
		},
		{
			name: "missing workspace id",
			mutate: func(l *DeploymentLease) {
				l.WorkspaceID = ""
			},
			wantErr: "workspace_id cannot be empty",
		},
		{
			name: "missing deployment id",
			mutate: func(l *DeploymentLease) {
				l.DeploymentID = ""
			},
			wantErr: "deployment_id cannot be empty",
		},
		{
			name: "missing environment id",
			mutate: func(l *DeploymentLease) {
				l.EnvironmentID = ""
			},
			wantErr: "environment_id cannot be empty",
		},
		{
			name: "missing consumer id",
			mutate: func(l *DeploymentLease) {
				l.ConsumerID = ""
			},
			wantErr: "consumer_id cannot be empty",
		},
		{
			name: "unsupported consumer type",
			mutate: func(l *DeploymentLease) {
				l.ConsumerType = "unknown_consumer"
			},
			wantErr: "unsupported consumer type",
		},
		{
			name: "invalid acquired_at",
			mutate: func(l *DeploymentLease) {
				l.AcquiredAt = 0
			},
			wantErr: "acquired_at timestamp must be greater than 0",
		},
		{
			name: "expires_at before acquired_at",
			mutate: func(l *DeploymentLease) {
				l.ExpiresAt = l.AcquiredAt - 1000
			},
			wantErr: "expires_at cannot be earlier than acquired_at",
		},
		{
			name: "released_at before acquired_at",
			mutate: func(l *DeploymentLease) {
				l.ReleasedAt = l.AcquiredAt - 1000
			},
			wantErr: "released_at cannot be earlier than acquired_at",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := validLease()
			tc.mutate(l)
			err := l.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestDeploymentLease_ExpirationLogic(t *testing.T) {
	acquired := int64(1700000000000)
	expires := int64(1700001000000)

	// Active lease with expiration in future
	lease := &DeploymentLease{
		AcquiredAt: acquired,
		ExpiresAt:  expires,
		Active:     true,
	}

	nowBefore := int64(1700000500000)
	if lease.IsExpired(nowBefore) {
		t.Fatal("expected lease to not be expired before expires_at")
	}
	if !lease.IsHeld(nowBefore) {
		t.Fatal("expected lease to be held before expires_at")
	}

	nowAfter := int64(1700002000000)
	if !lease.IsExpired(nowAfter) {
		t.Fatal("expected lease to be expired after expires_at")
	}
	if lease.IsHeld(nowAfter) {
		t.Fatal("expected lease to not be held after expires_at")
	}

	// Inactive lease
	lease.Active = false
	if lease.IsHeld(nowBefore) {
		t.Fatal("expected inactive lease to not be held")
	}

	// Indefinite lease (expires_at == 0)
	indefiniteLease := &DeploymentLease{
		AcquiredAt: acquired,
		ExpiresAt:  0,
		Active:     true,
	}
	if indefiniteLease.IsExpired(nowAfter) {
		t.Fatal("indefinite lease should never be expired")
	}
	if !indefiniteLease.IsHeld(nowAfter) {
		t.Fatal("indefinite active lease should always be held")
	}
}

func TestDeploymentLease_CloneImmutability(t *testing.T) {
	orig := &DeploymentLease{
		ID:         "l-1",
		ConsumerID: "orig-c",
		ConsumerMetadata: map[string]string{
			"key": "val",
		},
	}

	cloned := orig.Clone()
	cloned.ConsumerID = "mutated-c"
	cloned.ConsumerMetadata["key"] = "mutated-val"

	if orig.ConsumerID != "orig-c" {
		t.Fatalf("original ConsumerID mutated: %s", orig.ConsumerID)
	}
	if orig.ConsumerMetadata["key"] != "val" {
		t.Fatalf("original ConsumerMetadata mutated: %s", orig.ConsumerMetadata["key"])
	}
}
