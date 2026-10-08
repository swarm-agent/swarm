package pebblestore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerGCPRegistration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)

	req := WorkerGCPRegistration{
		Name:           "gcp-worker-target",
		RuntimeID:      "gcp_runtime_1",
		DesktopURL:     "https://10.88.0.224:5555",
		IdempotencyKey: "gcp_target_key",
	}

	rec, err := ws.RegisterWorkerGCPRuntime("account", "user", req)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if rec.Transport != "gcp" || rec.Status != "registered" || rec.SwarmID != req.RuntimeID || rec.Name != req.Name {
		t.Fatalf("unexpected record: %+v", rec)
	}

	// Idempotent retry returns existing record
	again, err := ws.RegisterWorkerGCPRuntime("account", "user", req)
	if err != nil || again.SwarmID != rec.SwarmID {
		t.Fatalf("retry failed: %v", err)
	}

	// Conflicting parameters reject
	changed := req
	changed.Name = "different-name"
	if _, err = ws.RegisterWorkerGCPRuntime("account", "user", changed); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("conflicting retry accepted: %v", err)
	}

	// Invalid runtime ID / idempotency key reject
	invalid := req
	invalid.RuntimeID = "invalid/slash"
	if _, err = ws.RegisterWorkerGCPRuntime("account", "user", invalid); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("invalid runtime id accepted: %v", err)
	}

	// Verified in TopologyStore
	ts := NewTopologyStore(db)
	stored, found, err := ts.GetRuntimeForAccount("account", req.RuntimeID)
	if err != nil || !found || stored.Transport != "gcp" || stored.Name != req.Name {
		t.Fatalf("stored runtime mismatch: %+v %v", stored, err)
	}

	// Cross-account isolation
	if _, found, err := ts.GetRuntimeForAccount("other-account", req.RuntimeID); err != nil || found {
		t.Fatal("cross-account gcp runtime exposed")
	}
}

func TestWorkerCommandAcknowledgement(t *testing.T) {
	_, ws := openTestStore(t)
	w, d := runtimeFixture(t, ws, "persistent")

	cmd, err := ws.QueueWorkerCommand("account", "owner", w.ID, d.ID, WorkerCommandRequest{
		ExpectedRevision: d.Revision,
		Generation:       d.Generation,
		Kind:             "stop",
		IdempotencyKey:   "stop-cmd-1",
	})
	if err != nil {
		t.Fatalf("queue command failed: %v", err)
	}
	if cmd.Status != "pending" {
		t.Fatalf("expected pending status: %+v", cmd)
	}

	evidenceDigest := strings.Repeat("e", 64)
	ack := WorkerCommandAcknowledgement{
		CommandID:      cmd.ID,
		Generation:     d.Generation,
		Status:         "acknowledged",
		EvidenceDigest: evidenceDigest,
	}

	acked, err := ws.AcknowledgeWorkerCommand("account", "owner", w.ID, d.ID, ack)
	if err != nil {
		t.Fatalf("acknowledge failed: %v", err)
	}
	if acked.Status != "acknowledged" {
		t.Fatalf("expected acknowledged status: %+v", acked)
	}

	// Idempotent re-ack
	replay, err := ws.AcknowledgeWorkerCommand("account", "owner", w.ID, d.ID, ack)
	if err != nil || replay.Status != "acknowledged" {
		t.Fatalf("replay ack failed: %v", err)
	}

	// Conflicting status rejects
	conflicting := ack
	conflicting.Status = "rejected"
	if _, err = ws.AcknowledgeWorkerCommand("account", "owner", w.ID, d.ID, conflicting); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("conflicting ack accepted: %v", err)
	}

	// Stale generation rejects
	stale := ack
	stale.Generation = d.Generation + 1
	if _, err = ws.AcknowledgeWorkerCommand("account", "owner", w.ID, d.ID, stale); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("stale generation ack accepted: %v", err)
	}

	// Invalid evidence digest rejects
	badDigest := ack
	badDigest.EvidenceDigest = "bad"
	if _, err = ws.AcknowledgeWorkerCommand("account", "owner", w.ID, d.ID, badDigest); !errors.Is(err, ErrWorkerInvalid) {
		t.Fatalf("invalid evidence digest accepted: %v", err)
	}
}
