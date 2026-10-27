package tool

import (
	"swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"testing"
)

// Purpose: the runtime must obtain Start tasks from the account, never legacy
// session metadata. A real store with conflicting legacy values proves the
// authority choice at the narrow runtime boundary for both supported values.
func TestAccountTaskRule(t *testing.T) {
	for _, value := range []string{"approve", "auto"} {
		t.Run(value, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := store.NewSessionStore(db)
			if err := db.PutJSON(store.KeySession("s"), store.SessionSnapshot{ID: "s", AccountScopeID: "a", Metadata: map[string]any{store.SessionAccessMetadataKey: store.SessionAccessMetadata(store.SessionAccess{Tasks: "free"})}}); err != nil {
				t.Fatal(err)
			}
			p := store.DefaultAccountPermissions()
			p.Tasks = value
			if _, err := db.SetAccountPermissions("a", 0, p); err != nil {
				t.Fatal(err)
			}
			r := NewRuntime(1)
			defer r.Close()
			r.SetManageSessionService(session.NewService(ss, nil))
			if got := r.sessionTasks("s"); got != value {
				t.Fatalf("got %q want %q", got, value)
			}
		})
	}
}
