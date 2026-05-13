package pebblestore

import (
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
	if runtimeRecord.UpdatedAt <= 0 || runtimeRecord.CreatedAt <= 0 {
		t.Fatalf("runtime timestamps not populated: %+v", runtimeRecord)
	}

	hostContainerRecord, err := topology.PutHostContainer(TopologyHostContainerRecord{
		HostContainerID:     " manager-1:ctr-1 ",
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

	loadedAttachment, ok, err := topology.GetAttachment("manager-1:ctr-1=>child-1")
	if err != nil || !ok {
		t.Fatalf("get attachment ok=%t err=%v", ok, err)
	}
	if loadedAttachment.HostContainerID != hostContainerRecord.HostContainerID {
		t.Fatalf("loaded attachment host container id = %q", loadedAttachment.HostContainerID)
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
