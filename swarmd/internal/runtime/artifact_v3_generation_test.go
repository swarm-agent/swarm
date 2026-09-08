package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"swarm/packages/swarmd/internal/api"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"testing"
	"time"
)

// Requirement: authenticated native detail/list hydration resolves failed and
// unpublished siblings after restart without selecting anything. Real durable
// allocation plus API DTO serialization tests this boundary without a browser.
func TestArtifactV3GenerationHydration(t *testing.T) {
	root := t.TempDir()
	dbpath := filepath.Join(root, "store")
	store, err := pebblestore.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	_, _, err = sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "owner", AccountScopeID: "account", UserID: "user", Title: "wave", WorkspacePath: root, WorkspaceName: "workspace", Mode: sessionruntime.ModeAuto, Preference: &pebblestore.ModelPreference{Provider: "codex", Model: "test", Thinking: "medium"}})
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Join(root, "repos")
	service, err := pebblestore.NewArtifactV3Service(sessions.Store(), repoRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := newArtifactV3RuntimeAdapter(service, sessions.Store(), repoRoot, filepath.Join(root, "evidence"), pebblestore.ArtifactV3Limits{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	grants := []tool.ArtifactV3AuthorGrant{}
	for i, call := range []string{"initial-a", "initial-b"} {
		grant, err := adapter.PrepareArtifactV3Turn(ctx, tool.ArtifactV3PrepareTurnRequest{AccountScopeID: "account", UserID: "user", OwnerSessionID: "owner", TaskCallID: call, Initial: true, PolicyRevision: "policy", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), GenerationWaveID: "wave", GenerationIndex: i + 1, GenerationCount: 2})
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, grant)
	}
	g := grants[1]
	if err := adapter.FailArtifactV3Turn(ctx, tool.ArtifactV3TurnFailure{ArtifactID: g.ArtifactID, TurnID: g.TurnID, CandidateID: g.CandidateID, Code: "test_failure", Message: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = pebblestore.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ss := pebblestore.NewSessionStore(store)
	service, err = pebblestore.NewArtifactV3Service(ss, repoRoot, pebblestore.ArtifactV3Limits{})
	if err != nil {
		t.Fatal(err)
	}
	adapter = newArtifactV3RuntimeAdapter(service, ss, repoRoot, filepath.Join(root, "evidence"), pebblestore.ArtifactV3Limits{}, nil)
	principal := api.ArtifactV3Principal{AccountScopeID: "account", UserID: "user"}
	before, _, _ := ss.GetArtifactV3Repository("account", "user", grants[0].ArtifactID)
	got, err := adapter.GetArtifact(ctx, principal, "owner", grants[0].ArtifactID)
	if err != nil || len(got.GenerationGroups) != 1 || len(got.GenerationGroups[0].Members) != 2 {
		t.Fatalf("hydrate %+v %v", got, err)
	}
	if got.GenerationGroups[0].Members[1].Status != "error" {
		t.Fatalf("failed sibling %+v", got.GenerationGroups)
	}
	listed, err := adapter.ListArtifacts(ctx, principal, "owner", 10)
	if err != nil || len(listed) != 2 || len(listed[0].Generations) != 1 {
		t.Fatalf("list %+v %v", listed, err)
	}
	if _, err := adapter.GetArtifact(ctx, api.ArtifactV3Principal{AccountScopeID: "account", UserID: "foreign"}, "owner", grants[0].ArtifactID); err == nil {
		t.Fatal("foreign read accepted")
	}
	after, _, _ := ss.GetArtifactV3Repository("account", "user", grants[0].ArtifactID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("navigation mutated repository")
	}
}
