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
