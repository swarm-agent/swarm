package artifact

import (
	"errors"
	"path/filepath"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type registryResolver struct { sessions []pebblestore.SessionSnapshot; tombstones []pebblestore.V3SessionTombstone; repaired map[string]int }
func (r *registryResolver) GetSession(id string) (pebblestore.SessionSnapshot, bool, error) { for _, s := range r.sessions { if s.ID == id { return s, true, nil } }; return pebblestore.SessionSnapshot{}, false, nil }
func (r *registryResolver) GetSessionTombstone(id string) (pebblestore.V3SessionTombstone, bool, error) { for _, s := range r.tombstones { if s.SessionID == id { return s, true, nil } }; return pebblestore.V3SessionTombstone{}, false, nil }
func (r *registryResolver) ListSessions(limit int) ([]pebblestore.SessionSnapshot, error) { if limit < len(r.sessions) { return r.sessions[:limit], nil }; return r.sessions, nil }
func (r *registryResolver) ListPendingSessionArtifactCleanups(limit int) ([]pebblestore.V3SessionTombstone, error) { out := []pebblestore.V3SessionTombstone{}; for _, s := range r.tombstones { if s.ArtifactCleanupPending { out = append(out, s); if len(out)==limit { break } } }; return out, nil }
func (r *registryResolver) MarkSessionArtifactCleanupComplete(id string) error { for i := range r.tombstones { if r.tombstones[i].SessionID == id { r.tombstones[i].ArtifactCleanupPending=false; return nil } }; return errors.New("not found") }
func (r *registryResolver) RepairSessionArtifactCollections(id string) (pebblestore.SessionArtifactRepairReport, error) { if r.repaired==nil { r.repaired=map[string]int{} }; r.repaired[id]++; return pebblestore.SessionArtifactRepairReport{}, nil }

func TestRegistryOwnedSessionDoesNotUseWorkspaceIdentity(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	resolver := &registryResolver{sessions: []pebblestore.SessionSnapshot{{ID:"owned", AccountScopeID:"account", UserID:"user", WorkspacePath:"/does/not/control/artifacts"}}}
	registry := NewRegistry(resolver, Limits{})
	if _, err := registry.OwnedSession("owned", "other", "user"); err == nil { t.Fatal("accepted foreign account") }
	if got, err := registry.OwnedSession("owned", "account", "user"); err != nil || got.ID != "owned" { t.Fatalf("owned=%+v err=%v", got, err) }
}

func TestRegistryMaintenanceAcknowledgesObsoleteWorkspaceCleanup(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", filepath.Join(t.TempDir(), "data"))
	resolver := &registryResolver{tombstones: []pebblestore.V3SessionTombstone{{SessionID:"deleted", Deleted:true, ArtifactCleanupPending:true}}}
	report, err := NewRegistry(resolver, Limits{}).RunMaintenance(10)
	if err != nil || report.DeletedSessions != 1 || resolver.tombstones[0].ArtifactCleanupPending { t.Fatalf("report=%+v err=%v", report, err) }
}
