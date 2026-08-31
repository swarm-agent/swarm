package run

import (
	"context"
	"path/filepath"
	"testing"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/artifactv2"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// Requirement: regular and swarm managed Designers allocate real V2 working
// artifacts and receive only the V2 capability; image launches remain separate.
// Threat: orchestration could keep creating V1 collection placeholders or expose
// manage_artifact through the compiled Designer profile.
func TestManagedDesignerAllocationUsesArtifactV2WithoutLegacyPlaceholder(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessionStore := pebblestore.NewSessionStore(store)
	if err := sessionStore.CreateSession(pebblestore.SessionSnapshot{ID: "parent", UserID: "user", AccountScopeID: "account", WorkspacePath: t.TempDir(), Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(sessionStore, nil)
	registry := artifact.NewRegistry(sessions, artifact.Limits{})
	core := artifactv2.NewService(sessions, sessions, sessions, artifactv2.NewGitBlobStore(registry))
	runtime := tool.NewRuntime(1)
	runtime.SetArtifactV2AuthorService(artifactv2.NewAuthorService(core, nil, nil))
	svc := &Service{sessions: sessions, tools: runtime}
	parent, _, _ := sessions.GetSession("parent")
	specs := []taskLaunchSpec{{RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "regular"}, {RequestedSubagentType: "designer", OutputMode: taskOutputModeManaged, MetaPrompt: "swarm", SwarmMode: true}}
	contexts, iteration, err := svc.allocateManagedDesignerArtifactV2(context.Background(), parent, "task-call", specs)
	if err != nil {
		t.Fatal(err)
	}
	if iteration != nil {
		t.Fatalf("regular allocation unexpectedly opened iteration: %+v", iteration)
	}
	for index, author := range contexts {
		if author == nil || author.Grant.ArtifactID == "" || author.Grant.OwnerSessionID != parent.ID || !author.Grant.AllowPartDeclaration {
			t.Fatalf("context[%d]=%+v", index, author)
		}
		working, ok, err := sessions.GetArtifactV2Working(parent.AccountScopeID, author.Grant.ArtifactID)
		if err != nil || !ok || working.State != pebblestore.ArtifactV2StateAllocated {
			t.Fatalf("working[%d]=%+v ok=%v err=%v", index, working, ok, err)
		}
	}
	collections, err := sessions.ListSessionArtifactCollections(parent.AccountScopeID, parent.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(collections) != 0 {
		t.Fatalf("legacy placeholders allocated: %+v", collections)
	}
	profile := agentruntime.DesignerAgentProfileForParent(pebblestore.AgentProfile{})
	if cfg := profile.ToolContract.Tools["artifact_v2_author"]; cfg.Enabled == nil || !*cfg.Enabled {
		t.Fatalf("Artifact V2 tool disabled: %+v", profile.ToolContract)
	}
	if cfg := profile.ToolContract.Tools["manage_artifact"]; cfg.Enabled == nil || *cfg.Enabled {
		t.Fatalf("legacy writer enabled: %+v", profile.ToolContract)
	}
}
