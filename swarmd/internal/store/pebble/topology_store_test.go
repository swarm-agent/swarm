package pebblestore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestTopologyStoreDirectWritersAndSnapshot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "topology.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)

	runtimeRecord, err := topology.PutRuntime(TopologyRuntimeRecord{
		SwarmID:         " child-1 ",
		UserID:          " user-a ",
		AccountScopeID:  " account-a ",
		Name:            " Child One ",
		Relationship:    "CHILD",
		ObservedSources: []string{" deploy_container ", "deploy_container"},
	})
	if err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if runtimeRecord.SwarmID != "child-1" {
		t.Fatalf("runtime swarm id = %q", runtimeRecord.SwarmID)
	}
	if runtimeRecord.Relationship != "child" {
		t.Fatalf("runtime relationship = %q", runtimeRecord.Relationship)
	}
	if runtimeRecord.UserID != "user-a" || runtimeRecord.AccountScopeID != "account-a" {
		t.Fatalf("runtime account ownership not normalized: %+v", runtimeRecord)
	}
	if runtimeRecord.UpdatedAt <= 0 || runtimeRecord.CreatedAt <= 0 {
		t.Fatalf("runtime timestamps not populated: %+v", runtimeRecord)
	}

	hostContainerRecord, err := topology.PutHostContainer(TopologyHostContainerRecord{
		HostContainerID:     " manager-1:ctr-1 ",
		UserID:              " user-a ",
		AccountScopeID:      " account-a ",
		HostSwarmID:         " manager-1 ",
		RuntimeContainerRef: " ctr-1 ",
		ContainerName:       " app ",
		Runtime:             "DOCKER",
	})
	if err != nil {
		t.Fatalf("put host container: %v", err)
	}
	if hostContainerRecord.HostContainerID != "manager-1:ctr-1" {
		t.Fatalf("host container id = %q", hostContainerRecord.HostContainerID)
	}
	if hostContainerRecord.Runtime != "docker" {
		t.Fatalf("host container runtime = %q", hostContainerRecord.Runtime)
	}
	if hostContainerRecord.Name != "app" {
		t.Fatalf("host container name = %q", hostContainerRecord.Name)
	}

	attachmentRecord, err := topology.PutAttachment(TopologyAttachmentRecord{
		AttachmentID:    " manager-1:ctr-1=>child-1 ",
		UserID:          " user-a ",
		AccountScopeID:  " account-a ",
		HostContainerID: hostContainerRecord.HostContainerID,
		RuntimeSwarmID:  runtimeRecord.SwarmID,
		State:           "ATTACHED",
	})
	if err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	if attachmentRecord.State != "attached" {
		t.Fatalf("attachment state = %q", attachmentRecord.State)
	}

	bindingRecord, err := topology.PutWorkspaceBinding(TopologyWorkspaceBindingRecord{
		BindingID:                 " /src|child-1|/dst|child ",
		UserID:                    " user-a ",
		AccountScopeID:            " account-a ",
		SourceWorkspacePath:       " /src ",
		DestinationRuntimeSwarmID: runtimeRecord.SwarmID,
		DestinationWorkspacePath:  " /dst ",
		ReplicationMode:           " mirror ",
	})
	if err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}
	if bindingRecord.BindingID != "/src|child-1|/dst|child" {
		t.Fatalf("binding id = %q", bindingRecord.BindingID)
	}

	routeRecord, err := topology.PutSessionRoute(TopologySessionRouteRecord{
		SessionID:          " session-1 ",
		UserID:             " user-a ",
		AccountScopeID:     " account-a ",
		RuntimeSwarmID:     runtimeRecord.SwarmID,
		WorkspaceBindingID: bindingRecord.BindingID,
		BackendURL:         " http://child.example:7781/ ",
	})
	if err != nil {
		t.Fatalf("put session route: %v", err)
	}
	if routeRecord.BackendURL != "http://child.example:7781/" {
		t.Fatalf("route backend url = %q", routeRecord.BackendURL)
	}

	refreshedStatus, ok, err := topology.GetMigrationStatus(DefaultTopologyMigrationStatusID)
	if err != nil || !ok {
		t.Fatalf("get refreshed migration status ok=%t err=%v", ok, err)
	}
	if refreshedStatus.RuntimeCount != 1 || refreshedStatus.HostContainerCount != 1 || refreshedStatus.AttachmentCount != 1 || refreshedStatus.WorkspaceBindingCount != 1 || refreshedStatus.SessionRouteCount != 1 {
		t.Fatalf("unexpected refreshed migration counts after direct writes: %+v", refreshedStatus)
	}

	statusRecord, err := topology.PutMigrationStatus(TopologyMigrationStatusRecord{
		Version:               "checkpoint1-v1",
		RuntimeCount:          1,
		HostContainerCount:    1,
		AttachmentCount:       1,
		WorkspaceBindingCount: 1,
		SessionRouteCount:     1,
	})
	if err != nil {
		t.Fatalf("put migration status: %v", err)
	}
	if statusRecord.ID != DefaultTopologyMigrationStatusID {
		t.Fatalf("migration status id = %q", statusRecord.ID)
	}
	if statusRecord.RebuiltAt <= 0 {
		t.Fatalf("migration status rebuilt_at = %d", statusRecord.RebuiltAt)
	}

	loadedRuntime, ok, err := topology.GetRuntime("child-1")
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if loadedRuntime.Name != "Child One" {
		t.Fatalf("loaded runtime name = %q", loadedRuntime.Name)
	}
	if loadedRuntime.UserID != "user-a" || loadedRuntime.AccountScopeID != "account-a" {
		t.Fatalf("loaded runtime account ownership = %+v", loadedRuntime)
	}

	loadedAttachment, ok, err := topology.GetAttachment("manager-1:ctr-1=>child-1")
	if err != nil || !ok {
		t.Fatalf("get attachment ok=%t err=%v", ok, err)
	}
	if loadedAttachment.HostContainerID != hostContainerRecord.HostContainerID {
		t.Fatalf("loaded attachment host container id = %q", loadedAttachment.HostContainerID)
	}
	if loadedAttachment.UserID != "user-a" || loadedAttachment.AccountScopeID != "account-a" {
		t.Fatalf("loaded attachment account ownership = %+v", loadedAttachment)
	}

	snapshot, err := topology.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Runtimes) != 1 || len(snapshot.HostContainers) != 1 || len(snapshot.Attachments) != 1 || len(snapshot.WorkspaceBindings) != 1 || len(snapshot.SessionRoutes) != 1 {
		t.Fatalf("unexpected snapshot counts: %+v", snapshot.MigrationStatus)
	}
	if snapshot.MigrationStatus.Version != "checkpoint1-v1" {
		t.Fatalf("snapshot migration version = %q", snapshot.MigrationStatus.Version)
	}
	if snapshot.HostContainers[0].UserID != "user-a" || snapshot.HostContainers[0].AccountScopeID != "account-a" ||
		snapshot.WorkspaceBindings[0].UserID != "user-a" || snapshot.WorkspaceBindings[0].AccountScopeID != "account-a" ||
		snapshot.SessionRoutes[0].UserID != "user-a" || snapshot.SessionRoutes[0].AccountScopeID != "account-a" {
		t.Fatalf("snapshot account ownership missing: %+v", snapshot)
	}

	if err := topology.DeleteAttachment(attachmentRecord.AttachmentID); err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	if _, ok, err := topology.GetAttachment(attachmentRecord.AttachmentID); err != nil || ok {
		t.Fatalf("attachment after delete ok=%t err=%v", ok, err)
	}
	if err := topology.DeleteSessionRoute(routeRecord.SessionID); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	if err := topology.DeleteWorkspaceBinding(bindingRecord.BindingID); err != nil {
		t.Fatalf("delete binding: %v", err)
	}
	if err := topology.DeleteHostContainer(hostContainerRecord.HostContainerID); err != nil {
		t.Fatalf("delete host container: %v", err)
	}
	if err := topology.DeleteRuntime(runtimeRecord.SwarmID); err != nil {
		t.Fatalf("delete runtime: %v", err)
	}
	refreshedStatus, ok, err = topology.GetMigrationStatus(DefaultTopologyMigrationStatusID)
	if err != nil || !ok {
		t.Fatalf("get refreshed migration status after deletes ok=%t err=%v", ok, err)
	}
	if refreshedStatus.RuntimeCount != 0 || refreshedStatus.HostContainerCount != 0 || refreshedStatus.AttachmentCount != 0 || refreshedStatus.WorkspaceBindingCount != 0 || refreshedStatus.SessionRouteCount != 0 {
		t.Fatalf("unexpected refreshed migration counts after direct deletes: %+v", refreshedStatus)
	}
}

func TestTopologyStoreAccountScopedKeyShape(t *testing.T) {
	accountScopeID := " Account/A "
	if got, want := KeyTopologyRuntimeForAccount(accountScopeID, " Child-1 "), "topology/runtime_by_account/account%2Fa/child-1"; got != want {
		t.Fatalf("runtime account key = %q, want %q", got, want)
	}
	if got, want := TopologyRuntimePrefixForAccount(accountScopeID), "topology/runtime_by_account/account%2Fa/"; got != want {
		t.Fatalf("runtime account prefix = %q, want %q", got, want)
	}
	if got, want := TopologyRuntimePrefixForAccount(""), KeyTopologyRuntimeAccountPrefix; got != want {
		t.Fatalf("empty runtime account prefix = %q, want %q", got, want)
	}

	if got, want := KeyTopologyHostContainerForAccount(accountScopeID, " Host:Container "), "topology/host_container_by_account/account%2Fa/host:container"; got != want {
		t.Fatalf("host container account key = %q, want %q", got, want)
	}
	if got, want := TopologyHostContainerPrefixForAccount(accountScopeID), "topology/host_container_by_account/account%2Fa/"; got != want {
		t.Fatalf("host container account prefix = %q, want %q", got, want)
	}

	if got, want := KeyTopologyAttachmentForAccount(accountScopeID, " Host:Container=>Child "), "topology/attachment_by_account/account%2Fa/host:container=%3Echild"; got != want {
		t.Fatalf("attachment account key = %q, want %q", got, want)
	}
	if got, want := TopologyAttachmentPrefixForAccount(accountScopeID), "topology/attachment_by_account/account%2Fa/"; got != want {
		t.Fatalf("attachment account prefix = %q, want %q", got, want)
	}

	if got, want := KeyTopologyWorkspaceBindingForAccount(accountScopeID, " /src|Child|/dst "), "topology/workspace_binding_by_account/account%2Fa/%2Fsrc%7Cchild%7C%2Fdst"; got != want {
		t.Fatalf("workspace binding account key = %q, want %q", got, want)
	}
	if got, want := TopologyWorkspaceBindingPrefixForAccount(accountScopeID), "topology/workspace_binding_by_account/account%2Fa/"; got != want {
		t.Fatalf("workspace binding account prefix = %q, want %q", got, want)
	}

	if got, want := KeyTopologySessionRouteForAccount(accountScopeID, " Session-1 "), "topology/session_route_by_account/account%2Fa/session-1"; got != want {
		t.Fatalf("session route account key = %q, want %q", got, want)
	}
	if got, want := TopologySessionRoutePrefixForAccount(accountScopeID), "topology/session_route_by_account/account%2Fa/"; got != want {
		t.Fatalf("session route account prefix = %q, want %q", got, want)
	}
}

func TestTopologyStoreAccountFieldsJSONAndMigrationPolicy(t *testing.T) {
	records := []struct {
		name    string
		payload any
	}{
		{name: "runtime", payload: TopologyRuntimeRecord{SwarmID: "runtime-1", UserID: "user-a", AccountScopeID: "account-a"}},
		{name: "host_container", payload: TopologyHostContainerRecord{HostContainerID: "host-1:ctr-1", UserID: "user-a", AccountScopeID: "account-a"}},
		{name: "attachment", payload: TopologyAttachmentRecord{AttachmentID: "attach-1", UserID: "user-a", AccountScopeID: "account-a"}},
		{name: "workspace_binding", payload: TopologyWorkspaceBindingRecord{BindingID: "binding-1", UserID: "user-a", AccountScopeID: "account-a"}},
		{name: "session_route", payload: TopologySessionRouteRecord{SessionID: "session-1", UserID: "user-a", AccountScopeID: "account-a"}},
	}
	for _, tc := range records {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !jsonHasStringField(t, payload, "user_id", "user-a") || !jsonHasStringField(t, payload, "account_scope_id", "account-a") {
				t.Fatalf("account fields missing from %s JSON: %s", tc.name, string(payload))
			}
		})
	}

	// Migration/backfill policy for Checkpoint 1.1:
	//   - Source-owned topology rows inherit UserID and AccountScopeID from their source record.
	//   - Ambiguous or orphaned legacy/global rows remain migration inputs only and are not product-visible.
	//   - Converted runtime reads must use account-scoped keys/indexes and must not fall back to legacy global mutable keys.
}

func jsonHasStringField(t *testing.T, payload []byte, field string, want string) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := decoded[field].(string)
	return ok && got == want
}

func TestTopologyStoreWriterValidation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "topology-validation.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)

	if _, err := topology.PutRuntime(TopologyRuntimeRecord{}); err == nil {
		t.Fatal("expected runtime validation error")
	}
	if _, err := topology.PutHostContainer(TopologyHostContainerRecord{HostContainerID: "a", HostSwarmID: "manager-1"}); err == nil {
		t.Fatal("expected host container validation error")
	}
	if _, err := topology.PutAttachment(TopologyAttachmentRecord{AttachmentID: "a", HostContainerID: "manager-1:ctr-1"}); err == nil {
		t.Fatal("expected attachment validation error")
	}
	if _, err := topology.PutWorkspaceBinding(TopologyWorkspaceBindingRecord{BindingID: "binding-1"}); err == nil {
		t.Fatal("expected workspace binding validation error")
	}
	if _, err := topology.PutSessionRoute(TopologySessionRouteRecord{}); err == nil {
		t.Fatal("expected session route validation error")
	}
}
