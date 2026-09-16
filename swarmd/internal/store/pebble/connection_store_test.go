package pebblestore

import (
	"path/filepath"
	"testing"

	"swarm-refactor/swarmtui/pkg/environments"
)

func openEphemeralStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func TestConnectionStore_CRUD(t *testing.T) {
	store := openEphemeralStore(t)
	cs := NewConnectionStore(store)

	localConn := environments.Connection{
		ID:             "conn-local-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		Name:           "Local Docker Engine",
		Description:    "Local development docker socket",
		Kind:           environments.ConnectionKindLocalDocker,
		Capabilities: environments.ConnectionCapabilities{
			SupportsDocker:      true,
			SupportsDirectMount: true,
		},
		LocalDocker: &environments.LocalDockerConfig{
			SocketPath: "/var/run/docker.sock",
		},
	}

	saved, err := cs.Save(localConn)
	if err != nil {
		t.Fatalf("save connection: %v", err)
	}
	if saved.CreatedAt <= 0 || saved.UpdatedAt <= 0 {
		t.Fatalf("expected positive timestamps, got created=%d updated=%d", saved.CreatedAt, saved.UpdatedAt)
	}

	got, found, err := cs.Get("acc-test", "ws-test", "conn-local-1")
	if err != nil || !found {
		t.Fatalf("get connection: found=%v, err=%v", found, err)
	}
	if got.Name != "Local Docker Engine" || got.Kind != environments.ConnectionKindLocalDocker {
		t.Fatalf("unexpected connection: %+v", got)
	}

	// Update connection
	got.Description = "Updated description"
	updated, err := cs.Save(got)
	if err != nil {
		t.Fatalf("update connection: %v", err)
	}
	if updated.CreatedAt != saved.CreatedAt {
		t.Fatalf("created_at altered on update: %d != %d", updated.CreatedAt, saved.CreatedAt)
	}
	if updated.UpdatedAt < saved.UpdatedAt {
		t.Fatalf("updated_at not advanced: %d < %d", updated.UpdatedAt, saved.UpdatedAt)
	}

	// List connections
	sshConn := environments.Connection{
		ID:             "conn-ssh-1",
		AccountScopeID: "acc-test",
		WorkspaceID:    "ws-test",
		Name:           "Remote Host",
		Kind:           environments.ConnectionKindSSH,
		Capabilities: environments.ConnectionCapabilities{
			SupportsSSH:    true,
			SupportsDocker: true,
		},
		SSH: &environments.SSHConfig{
			Host: "192.168.1.50",
			Port: 22,
			User: "ubuntu",
		},
	}
	if _, err := cs.Save(sshConn); err != nil {
		t.Fatalf("save ssh connection: %v", err)
	}

	list, err := cs.List("acc-test", "ws-test", 100)
	if err != nil {
		t.Fatalf("list connections: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(list))
	}

	// Delete
	deleted, err := cs.Delete("acc-test", "ws-test", "conn-local-1")
	if err != nil || !deleted {
		t.Fatalf("delete connection: deleted=%v, err=%v", deleted, err)
	}

	_, found, err = cs.Get("acc-test", "ws-test", "conn-local-1")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if found {
		t.Fatal("expected connection to be deleted")
	}

	listAfter, err := cs.List("acc-test", "ws-test", 100)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(listAfter) != 1 || listAfter[0].ID != "conn-ssh-1" {
		t.Fatalf("unexpected list after delete: %+v", listAfter)
	}
}

func TestConnectionStore_Isolation(t *testing.T) {
	store := openEphemeralStore(t)
	cs := NewConnectionStore(store)

	conn := environments.Connection{
		ID:             "conn-iso-1",
		AccountScopeID: "acc-a",
		WorkspaceID:    "ws-a",
		Name:           "Isolated Conn",
		Kind:           environments.ConnectionKindLocalDocker,
	}

	if _, err := cs.Save(conn); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Different account scope, same workspace ID
	_, found, err := cs.Get("acc-b", "ws-a", "conn-iso-1")
	if err != nil {
		t.Fatalf("get cross-account: %v", err)
	}
	if found {
		t.Fatal("cross-account leakage detected")
	}

	// Same account scope, different workspace ID
	_, found, err = cs.Get("acc-a", "ws-b", "conn-iso-1")
	if err != nil {
		t.Fatalf("get cross-workspace: %v", err)
	}
	if found {
		t.Fatal("cross-workspace leakage detected")
	}

	// List cross-account
	listA, err := cs.List("acc-b", "ws-a", 10)
	if err != nil {
		t.Fatalf("list cross-account: %v", err)
	}
	if len(listA) != 0 {
		t.Fatalf("expected empty list cross-account, got %d", len(listA))
	}

	// Delete cross-account should not delete
	del, err := cs.Delete("acc-b", "ws-a", "conn-iso-1")
	if err != nil {
		t.Fatalf("delete cross-account: %v", err)
	}
	if del {
		t.Fatal("cross-account delete should return false")
	}

	// Original record still exists
	_, found, err = cs.Get("acc-a", "ws-a", "conn-iso-1")
	if err != nil || !found {
		t.Fatalf("original record missing: %v", err)
	}
}

func TestConnectionStore_RejectsUnvalidated(t *testing.T) {
	store := openEphemeralStore(t)
	cs := NewConnectionStore(store)

	// Missing ID
	badConn := environments.Connection{
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Missing ID",
		Kind:           environments.ConnectionKindLocalDocker,
	}
	if _, err := cs.Save(badConn); err == nil {
		t.Fatal("expected error for missing ID")
	}

	// Invalid SSH config (missing host)
	badSSH := environments.Connection{
		ID:             "conn-bad-ssh",
		AccountScopeID: "acc-1",
		WorkspaceID:    "ws-1",
		Name:           "Bad SSH",
		Kind:           environments.ConnectionKindSSH,
		SSH: &environments.SSHConfig{
			User: "root",
		},
	}
	if _, err := cs.Save(badSSH); err == nil {
		t.Fatal("expected error for invalid SSH config")
	}
}
