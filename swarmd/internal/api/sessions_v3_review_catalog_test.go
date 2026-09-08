package api

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: Manage can display owned review navigation before Git inspection.
// Threat: catalog mode accidentally performs mutations or grants actions without
// cleanliness/lineage evidence. The service boundary is the narrowest layer to
// prove absent Git/settings dependencies, principal denial, and no metadata writes.
func TestSessionsV3ReviewCatalogIsPendingAndReadOnly(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "catalog.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore := pebblestore.NewSessionStore(store)
	svc := sessionruntime.NewService(sessionStore, events)
	principal := identity.Principal{UserID: "user", AccountScopeID: "account"}
	snapshot := pebblestore.SessionSnapshot{
		ID: "review-lane", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		WorkspacePath: t.TempDir(), WorkspaceName: "workspace", Title: "Pending review", Mode: "auto",
		WorktreeEnabled: true, WorktreeRootPath: filepath.Join(t.TempDir(), "unavailable"),
		WorktreeBranch: "agent/review", WorktreeBaseBranch: "dev", CreatedAt: 1, UpdatedAt: 1,
	}
	if err := sessionStore.CreateSession(snapshot); err != nil {
		t.Fatal(err)
	}
	before, _, err := svc.GetSession(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{sessions: svc} // No settings service; no Git executable.
	t.Setenv("PATH", t.TempDir())
	req := sessionsV3ReviewWorktreesRequest{CatalogOnly: true, SessionIDs: []string{snapshot.ID}}
	result, err := server.classifySessionsV3ReviewWorktrees(context.Background(), principal, req)
	if err != nil {
		t.Fatal(err)
	}
	items := result["retained"].([]sessionreview.Classification)
	if len(items) != 1 || items[0].SessionID != snapshot.ID || items[0].Reason != "inspection_pending" || result["inspection_pending"] != true {
		t.Fatalf("catalog = %+v", result)
	}
	if items[0].ArchiveReady || items[0].CommitEligible || items[0].IntegrateEligible || items[0].SourceHead != "" {
		t.Fatalf("catalog granted unverified authority: %+v", items[0])
	}
	for _, other := range []identity.Principal{
		{UserID: "other", AccountScopeID: principal.AccountScopeID},
		{UserID: principal.UserID, AccountScopeID: "other"},
	} {
		if result, err := server.classifySessionsV3ReviewWorktrees(context.Background(), other, req); err == nil || result != nil {
			t.Fatalf("foreign catalog returned result=%v err=%v", result, err)
		}
	}
	for _, mutation := range []sessionsV3ReviewWorktreesRequest{
		{CatalogOnly: true, ArchiveAll: true}, {CatalogOnly: true, Automatic: true},
		{CatalogOnly: true, ArchiveIDs: []string{snapshot.ID}},
		{CatalogOnly: true, CommitIDs: []string{snapshot.ID}},
		{CatalogOnly: true, PromoteIDs: []string{snapshot.ID}},
	} {
		if result, err := server.classifySessionsV3ReviewWorktrees(context.Background(), principal, mutation); err == nil || result != nil || !strings.Contains(err.Error(), "cannot perform") {
			t.Fatalf("catalog mutation returned result=%v err=%v", result, err)
		}
	}
	after, found, err := svc.GetSession(snapshot.ID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("catalog changed session: found=%v err=%v before=%+v after=%+v", found, err, before, after)
	}
}

// Requirement: review snapshot/history work has at most four concurrent workers.
// Threat: large review lists saturate the host with Git processes. Exercise the
// actual scheduler with a blocked first wave and exact-once completion assertions.
func TestSessionsV3ReviewWorkersBoundConcurrency(t *testing.T) {
	started := make(chan struct{}, 12)
	release := make(chan struct{})
	done := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var active, peak atomic.Int32
	var visits [12]atomic.Int32
	go func() {
		runSessionsV3ReviewWorkers(len(visits), func(index int) {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-release
			visits[index].Add(1)
			active.Add(-1)
		})
		close(done)
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than four workers started")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("workers did not finish")
	}
	if peak.Load() > 4 {
		t.Fatalf("peak concurrency = %d", peak.Load())
	}
	for i := range visits {
		if visits[i].Load() != 1 {
			t.Fatalf("index %d visited %d times", i, visits[i].Load())
		}
	}
}
