package pebblestore

import (
	"errors"
	"path/filepath"
	"testing"
)

// Requirement: trusted-host file references survive canonical SSH registration
// and retries cannot silently swap trust policy. The real temp-store boundary is
// the smallest layer proving persistence and rejected retry has no write effect.
func TestWorkerSSHRegistrationTrustReference(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ws := NewWorkerStore(db)
	req := WorkerSSHRegistration{WorkspaceID: "workspace", Name: "ssh", Host: "worker.example", User: "worker", Port: 22, IdempotencyKey: "target", KnownHostsFile: "/trust/known_hosts"}
	c, err := ws.RegisterWorkerSSHConnection("account", req)
	if err != nil {
		t.Fatal(err)
	}
	if c.SSH.KnownHostsFile != req.KnownHostsFile {
		t.Fatal("trust reference lost")
	}
	if again, err := ws.RegisterWorkerSSHConnection("account", req); err != nil || again.ID != c.ID {
		t.Fatalf("retry %v %v", again, err)
	}
	req.KnownHostsFile = "/trust/different"
	if _, err = ws.RegisterWorkerSSHConnection("account", req); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("changed trust accepted: %v", err)
	}
	stored, found, err := NewConnectionStore(db).Get("account", "workspace", c.ID)
	if err != nil || !found || stored.SSH.KnownHostsFile != "/trust/known_hosts" {
		t.Fatalf("rejected write changed trust: %v", err)
	}
	if _, found, err := NewConnectionStore(db).Get("other", "workspace", c.ID); err != nil || found {
		t.Fatal("cross-account trust exposed")
	}
	for _, file := range []string{"relative", "/trust/../other", "/trust/hosts\n"} {
		req.KnownHostsFile = file
		if _, err = ws.RegisterWorkerSSHConnection("account", req); !errors.Is(err, ErrWorkerInvalid) {
			t.Fatalf("invalid trust reference accepted: %v", err)
		}
	}
}
