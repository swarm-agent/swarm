package pebblestore

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationKeysEncodeAndPrefixRelations(t *testing.T) {
	if got, want := KeyIntegrationPack(" Spotify/Main "), "integration/pack/spotify%2Fmain"; got != want {
		t.Fatalf("pack key = %q, want %q", got, want)
	}
	if got, want := KeyIntegrationPackVersion("Pack One", "Draft/Current"), "integration/pack_version/pack%20one/draft%2Fcurrent"; got != want {
		t.Fatalf("version key = %q, want %q", got, want)
	}
	if got, want := IntegrationToolPrefix("Pack One", "Draft/Current"), "integration/tool/pack%20one/draft%2Fcurrent/"; got != want {
		t.Fatalf("tool prefix = %q, want %q", got, want)
	}
	if got, want := IntegrationAssignmentByAgentPrefix("Research Agent"), "integration/assignment_by_agent/research%20agent/"; got != want {
		t.Fatalf("assignment by agent prefix = %q, want %q", got, want)
	}
	if got, want := IntegrationWorkspaceSessionPrefix("Spotify Draft"), "integration/workspace_session/spotify%20draft/"; got != want {
		t.Fatalf("workspace session prefix = %q, want %q", got, want)
	}
}

func TestIntegrationStoreRecordsRoundTripAndReverseIndexes(t *testing.T) {
	store := openIntegrationTestStore(t)
	integrations := NewIntegrationStore(store)

	pack, err := integrations.PutPack(IntegrationPackRecord{
		PackID:      "Spotify",
		DisplayName: "Spotify",
		Description: "Music library integration",
	})
	if err != nil {
		t.Fatalf("put pack: %v", err)
	}
	if pack.PackID != "spotify" || pack.CreatedAt.IsZero() || pack.UpdatedAt.IsZero() {
		t.Fatalf("normalized pack = %+v", pack)
	}

	version, err := integrations.PutPackVersion(IntegrationPackVersionRecord{
		PackID:     "Spotify",
		VersionID:  "Draft",
		ToolIDs:    []string{"Search", "search", "Playlists"},
		AdapterIDs: []string{"CLI"},
		PromptIDs:  []string{"Context"},
	})
	if err != nil {
		t.Fatalf("put version: %v", err)
	}
	if version.Status != IntegrationVersionStatusDraft || len(version.ToolIDs) != 2 {
		t.Fatalf("normalized version = %+v", version)
	}

	adapter, err := integrations.PutAdapter(IntegrationAdapterRecord{
		PackID:    "Spotify",
		VersionID: "Draft",
		AdapterID: "CLI",
		Type:      "local_api_bridge",
		Settings:  map[string]string{"command_ref": "spotify-cli"},
	})
	if err != nil {
		t.Fatalf("put adapter: %v", err)
	}
	if adapter.Type != IntegrationAdapterTypeHostHTTPBridge {
		t.Fatalf("adapter type = %q", adapter.Type)
	}

	tool, err := integrations.PutTool(IntegrationToolRecord{
		PackID:      "Spotify",
		VersionID:   "Draft",
		ToolID:      "Search",
		Name:        "Search tracks",
		AdapterID:   "CLI",
		InputSchema: []byte(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("put tool: %v", err)
	}
	if tool.PermissionMode != IntegrationPermissionModeAskBlocking {
		t.Fatalf("tool permission mode = %q", tool.PermissionMode)
	}

	fragment, err := integrations.PutPromptFragment(IntegrationPromptFragmentRecord{
		PackID:    "Spotify",
		VersionID: "Draft",
		PromptID:  "Context",
		Content:   "Use least-privilege local auth.",
	})
	if err != nil {
		t.Fatalf("put prompt fragment: %v", err)
	}
	if fragment.PromptID != "context" {
		t.Fatalf("fragment = %+v", fragment)
	}

	if packs, err := integrations.ListPacks(10); err != nil || len(packs) != 1 || packs[0].PackID != "spotify" {
		t.Fatalf("list packs = %+v err=%v", packs, err)
	}
	if tools, err := integrations.ListTools("Spotify", "Draft", 10); err != nil || len(tools) != 1 || tools[0].ToolID != "search" {
		t.Fatalf("list tools = %+v err=%v", tools, err)
	}
	if adapters, err := integrations.ListAdapters("Spotify", "Draft", 10); err != nil || len(adapters) != 1 || adapters[0].AdapterID != "cli" {
		t.Fatalf("list adapters = %+v err=%v", adapters, err)
	}
	if prompts, err := integrations.ListPromptFragments("Spotify", "Draft", 10); err != nil || len(prompts) != 1 || prompts[0].PromptID != "context" {
		t.Fatalf("list prompts = %+v err=%v", prompts, err)
	}

	assignment, err := integrations.PutAssignment(IntegrationAssignmentRecord{
		AssignmentID: "Assign-1",
		AgentName:    "Research Agent",
		PackID:       "Spotify",
		VersionID:    "Draft",
	})
	if err != nil {
		t.Fatalf("put assignment: %v", err)
	}
	if assignment.Status != IntegrationAssignmentStatusActive || assignment.AgentName != "research agent" {
		t.Fatalf("normalized assignment = %+v", assignment)
	}
	if byAgent, err := integrations.ListAssignmentsByAgent("Research Agent", 10); err != nil || len(byAgent) != 1 || byAgent[0].AssignmentID != "assign-1" {
		t.Fatalf("assignments by agent = %+v err=%v", byAgent, err)
	}
	if byPack, err := integrations.ListAssignmentsByPack("Spotify", "Draft", 10); err != nil || len(byPack) != 1 || byPack[0].AssignmentID != "assign-1" {
		t.Fatalf("assignments by pack = %+v err=%v", byPack, err)
	}

	updated, err := integrations.PutAssignment(IntegrationAssignmentRecord{
		AssignmentID: "Assign-1",
		AgentName:    "Builder Agent",
		PackID:       "Spotify",
		VersionID:    "Published",
	})
	if err != nil {
		t.Fatalf("update assignment: %v", err)
	}
	if updated.AgentName != "builder agent" || updated.VersionID != "published" {
		t.Fatalf("updated assignment = %+v", updated)
	}
	if stale, err := integrations.ListAssignmentsByAgent("Research Agent", 10); err != nil || len(stale) != 0 {
		t.Fatalf("stale assignments by agent = %+v err=%v", stale, err)
	}
	if stale, err := integrations.ListAssignmentsByPack("Spotify", "Draft", 10); err != nil || len(stale) != 0 {
		t.Fatalf("stale assignments by pack = %+v err=%v", stale, err)
	}
	if current, err := integrations.ListAssignmentsByAgent("Builder Agent", 10); err != nil || len(current) != 1 || current[0].VersionID != "published" {
		t.Fatalf("current assignments by agent = %+v err=%v", current, err)
	}

	if err := integrations.DeleteAssignment("Assign-1"); err != nil {
		t.Fatalf("delete assignment: %v", err)
	}
	if current, err := integrations.ListAssignmentsByAgent("Builder Agent", 10); err != nil || len(current) != 0 {
		t.Fatalf("assignments after delete = %+v err=%v", current, err)
	}
}

func TestIntegrationWorkspaceLatestChildPointerAndSessionOrdering(t *testing.T) {
	store := openIntegrationTestStore(t)
	integrations := NewIntegrationStore(store)

	workspace, err := integrations.PutWorkspace(IntegrationWorkspaceRecord{
		WorkspaceID:    "Spotify Draft",
		DisplayName:    "Spotify integration",
		PackID:         "Spotify",
		DraftVersionID: "Draft",
	})
	if err != nil {
		t.Fatalf("put workspace: %v", err)
	}
	if workspace.WorkspaceID != "spotify draft" || workspace.LatestChildSessionID != "" {
		t.Fatalf("workspace = %+v", workspace)
	}

	olderTime := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)
	newerTime := time.Date(2025, 1, 2, 11, 0, 0, 0, time.UTC)
	if _, err := integrations.PutWorkspaceSession(IntegrationWorkspaceSessionRecord{WorkspaceID: "Spotify Draft", SessionID: "session-old", UpdatedAt: olderTime}); err != nil {
		t.Fatalf("put older session: %v", err)
	}
	if _, err := integrations.PutWorkspaceSession(IntegrationWorkspaceSessionRecord{WorkspaceID: "Spotify Draft", SessionID: "session-new", UpdatedAt: newerTime}); err != nil {
		t.Fatalf("put newer session: %v", err)
	}

	latest, ok, err := integrations.LatestWorkspaceSession("Spotify Draft")
	if err != nil || !ok || latest.SessionID != "session-new" {
		t.Fatalf("latest session = %+v ok=%v err=%v", latest, ok, err)
	}
	loadedWorkspace, ok, err := integrations.GetWorkspace("Spotify Draft")
	if err != nil || !ok {
		t.Fatalf("get workspace ok=%v err=%v", ok, err)
	}
	if loadedWorkspace.LatestChildSessionID != "session-new" || !loadedWorkspace.LatestChildSessionAt.Equal(newerTime) {
		t.Fatalf("workspace latest pointer = %+v", loadedWorkspace)
	}

	newestTime := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	if _, err := integrations.PutWorkspaceSession(IntegrationWorkspaceSessionRecord{WorkspaceID: "Spotify Draft", SessionID: "session-old", UpdatedAt: newestTime}); err != nil {
		t.Fatalf("update older session: %v", err)
	}
	latest, ok, err = integrations.LatestWorkspaceSession("Spotify Draft")
	if err != nil || !ok || latest.SessionID != "session-old" {
		t.Fatalf("latest after update = %+v ok=%v err=%v", latest, ok, err)
	}
	sessions, err := integrations.ListWorkspaceSessions("Spotify Draft", 10)
	if err != nil {
		t.Fatalf("list workspace sessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].SessionID != "session-old" || sessions[1].SessionID != "session-new" {
		t.Fatalf("workspace sessions = %+v", sessions)
	}
}

func openIntegrationTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "integrations.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
