package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

type workerScopeSessions struct{}

func (workerScopeSessions) GetSession(id string) (store.SessionSnapshot, bool, error) {
	return store.SessionSnapshot{ID: id, AccountScopeID: "account"}, true, nil
}

// Purpose: gateWorkspaceScopeCalls must turn an outside host search into a
// model-visible error and continue admitting the next in-scope call, without
// emitting permission events or persisting waits. This run-gate test exercises
// real scope expansion and the shared permission service, not a fake decision.
func TestWorkerOutsideSearchDenialContinues(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PutJSON(store.KeyWorkerRun("account", "worker", "job"), store.WorkerRunRecord{ID: "job", WorkerID: "worker", AccountScopeID: "account", SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	ps := store.NewPermissionStore(db)
	perms := permission.NewService(ps, nil, nil)
	perms.SetSessionResolver(workerScopeSessions{})
	perms.SetBypassGate(func() (bool, string) { return true, "" })
	svc := NewService(nil, nil, nil, nil, perms, nil, nil, nil)
	root, outside := t.TempDir(), t.TempDir()
	args := func(path string) string {
		b, _ := json.Marshal(map[string]any{"path": path, "query": "needle"})
		return string(b)
	}
	calls := []tool.Call{{CallID: "outside", Name: "search", Arguments: args(outside)}, {CallID: "inside", Name: "search", Arguments: args(root)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	emitted := 0
	results, approved, indexes, changed, _, err := svc.gateWorkspaceScopeCalls(ctx, "session", "session", "run", 1, "auto", root, "workspace", identity.Principal{}, &runWorkspaceContext{OriginWorkspacePath: root, OriginWorkspaceRoots: []string{root}}, calls, func(StreamEvent) { emitted++ })
	if err != nil || changed || emitted != 0 || len(approved) != 1 || len(indexes) != 1 || indexes[0] != 1 {
		t.Fatalf("run did not continue: approved=%+v indexes=%v changed=%v emitted=%d err=%v", approved, indexes, changed, emitted, err)
	}
	if !strings.Contains(results[0].Error, "use bash inside the sandbox") {
		t.Fatalf("missing guidance: %+v", results)
	}
	pending, err := ps.ListPendingPermissions("session", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
