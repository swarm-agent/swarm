package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type registryResolver struct {
	sessions   []pebblestore.SessionSnapshot
	tombstones []pebblestore.V3SessionTombstone
	repaired   map[string]int
}

func (r *registryResolver) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) {
	for _, session := range r.sessions {
		if session.ID == id {
			return session, true, nil
		}
	}
	return pebblestore.SessionSnapshot{}, false, nil
}

func (r *registryResolver) GetSessionTombstone(id string) (pebblestore.V3SessionTombstone, bool, error) {
	for _, tombstone := range r.tombstones {
		if tombstone.SessionID == id {
			return tombstone, true, nil
		}
	}
	return pebblestore.V3SessionTombstone{}, false, nil
}

func (r *registryResolver) ListSessions(limit int) ([]pebblestore.SessionSnapshot, error) {
	if limit < len(r.sessions) {
		return r.sessions[:limit], nil
	}
	return r.sessions, nil
}

func (r *registryResolver) ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error) {
	out := make([]pebblestore.V3SessionTombstone, 0, limit)
	for _, tombstone := range r.tombstones {
		if tombstone.ArtifactCleanupPending {
			out = append(out, tombstone)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (r *registryResolver) RepairSessionArtifactCollections(id string) (pebblestore.SessionArtifactRepairReport, error) {
	if r.repaired == nil { r.repaired = make(map[string]int) }
	r.repaired[id]++
	return pebblestore.SessionArtifactRepairReport{}, nil
}

func (r *registryResolver) MarkSessionArtifactCleanupComplete(id string) error {
	for i := range r.tombstones {
		if r.tombstones[i].SessionID == id {
			r.tombstones[i].ArtifactCleanupPending = false
			return nil
		}
	}
	return errors.New("tombstone not found")
}

func (r *registryResolver) ListSessionTombstonesForAccount(_ string, limit int) ([]pebblestore.V3SessionTombstone, error) {
	if limit < len(r.tombstones) {
		return r.tombstones[:limit], nil
	}
	return r.tombstones, nil
}

func TestRegistryServiceForOwnedSessionRejectsMismatchedOwnerAndTombstone(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil { t.Fatal(err) }
	resolver := &registryResolver{
		sessions: []pebblestore.SessionSnapshot{{ID: "owned", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace}},
		tombstones: []pebblestore.V3SessionTombstone{{SessionID: "deleted", AccountScopeID: "account-1", UserID: "user-1", WorkspacePath: workspace, Deleted: true}},
	}
	registry := NewRegistry(resolver, Limits{})
	if _, _, err := registry.ServiceForOwnedSession("owned", "other", "user-1"); err == nil { t.Fatal("expected account ownership error") }
	if _, _, err := registry.ServiceForOwnedSession("owned", "account-1", "other"); err == nil { t.Fatal("expected user ownership error") }
	if _, _, err := registry.ServiceForOwnedSession("deleted", "account-1", "user-1"); err == nil { t.Fatal("expected tombstone rejection") }
	if _, _, err := registry.ServiceForOwnedSession("owned", "account-1", "user-1"); err != nil { t.Fatalf("owned service: %v", err) }
}

func TestRegistryMaintenancePreservesArchivedAndDeletesDeleted(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{tombstones: []pebblestore.V3SessionTombstone{
		{SessionID: "archived-session", WorkspacePath: workspace, Archived: true, Kind: "archived"},
		{SessionID: "deleted-session", WorkspacePath: workspace, Deleted: true, Kind: "deleted", ArtifactCleanupPending: true},
	}}
	registry := NewRegistry(resolver, Limits{})
	service, err := registry.ServiceForWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"archived-session", "deleted-session"} {
		variant := testVariant("variant-1", "note.txt", "text/plain", "text")
		variant.SessionID = sessionID
		staged, err := service.Stage(context.Background(), variant, strings.NewReader(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.RunMaintenance(10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.sessionDir("archived-session")); err != nil {
		t.Fatalf("archived bytes removed: %v", err)
	}
	if _, err := os.Stat(service.sessionDir("deleted-session")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted bytes remain: %v", err)
	}
}

func TestRegistryMaintenanceRetriesPendingDeleteAndMarksComplete(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{tombstones: []pebblestore.V3SessionTombstone{{
		SessionID: "pending-delete", WorkspacePath: workspace, Deleted: true, Kind: "deleted", ArtifactCleanupPending: true,
	}}}
	registry := NewRegistry(resolver, Limits{})
	service, err := registry.ServiceForWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	variant.SessionID = "pending-delete"
	staged, err := service.Stage(context.Background(), variant, strings.NewReader("delete me"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finalize(context.Background(), staged, "", 0); err != nil {
		t.Fatal(err)
	}

	report, err := registry.RunMaintenance(10)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedSessions != 1 || resolver.tombstones[0].ArtifactCleanupPending {
		t.Fatalf("maintenance retry = %+v tombstone=%+v", report, resolver.tombstones[0])
	}
	if _, err := os.Stat(service.sessionDir("pending-delete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending delete bytes remain: %v", err)
	}
	second, err := registry.RunMaintenance(10)
	if err != nil || second.DeletedSessions != 0 {
		t.Fatalf("idempotent maintenance = %+v err=%v", second, err)
	}
}

func TestRegistryRestartReconcilesActiveStaging(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{{ID: "active-session", WorkspacePath: workspace}}}
	registry := NewRegistry(resolver, Limits{})
	service, err := registry.ServiceForSession("active-session")
	if err != nil {
		t.Fatal(err)
	}
	variant := testVariant("variant-1", "note.txt", "text/plain", "text")
	variant.SessionID = "active-session"
	if _, err := service.Stage(context.Background(), variant, strings.NewReader("incomplete")); err != nil {
		t.Fatal(err)
	}
	restarted := NewRegistry(resolver, Limits{})
	report, err := restarted.RunMaintenance(10)
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedStaging != 1 {
		t.Fatalf("maintenance report = %+v", report)
	}
}
