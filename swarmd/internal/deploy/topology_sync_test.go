package deploy

import (
	"context"
	"path/filepath"
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestMirrorDeploymentSyncsCanonicalHostContainerAndAttachment(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "deploy-topology-sync.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	deploySvc := NewService(pebblestore.NewDeployContainerStore(store), nil, nil, nil, nil, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)

	deployment, err := deploySvc.MirrorDeployment(context.Background(), ContainerDeployment{
		ID:               "deploy-1",
		UserID:           "user-1",
		AccountScopeID:   "account-1",
		Name:             "managed child",
		Kind:             "container",
		Status:           "running",
		Runtime:          "docker",
		ContainerName:    "managed-child",
		ContainerID:      "ctr-123",
		HostSwarmID:      "managed-swarm",
		HostDisplayName:  "Managed Host",
		HostBackendURL:   "http://managed.example:7781",
		HostAPIBaseURL:   "http://managed.example:7781",
		HostDesktopURL:   "https://managed.example",
		BackendHostPort:  7781,
		DesktopHostPort:  7782,
		Image:            "ghcr.io/swarm/child:latest",
		AttachStatus:     "attached",
		ChildSwarmID:     "child-swarm-1",
		ChildDisplayName: "Child One",
		ChildBackendURL:  "http://child.example:7781",
		ChildDesktopURL:  "https://child.example",
	})
	if err != nil {
		t.Fatalf("mirror deployment: %v", err)
	}

	hostContainerID, err := deploySvc.canonicalHostContainerIDForDeployment(pebblestore.DeployContainerRecord{
		ID:              deployment.ID,
		ContainerID:     deployment.ContainerID,
		ContainerName:   deployment.ContainerName,
		HostSwarmID:     deployment.HostSwarmID,
		HostContainerID: deployment.HostContainerID,
	})
	if err != nil {
		t.Fatalf("canonical host container id: %v", err)
	}
	if hostContainerID == "" {
		t.Fatal("expected canonical host container id")
	}
	hostContainer, ok, err := topologyStore.GetHostContainerForAccount("account-1", hostContainerID)
	if err != nil || !ok {
		t.Fatalf("get host container ok=%t err=%v", ok, err)
	}
	if hostContainer.UserID != "user-1" || hostContainer.AccountScopeID != "account-1" {
		t.Fatalf("host container account = %q/%q", hostContainer.UserID, hostContainer.AccountScopeID)
	}
	if hostContainer.HostSwarmID != "managed-swarm" {
		t.Fatalf("host container host swarm id = %q", hostContainer.HostSwarmID)
	}

	attachmentID := pebblestore.CanonicalTopologyAttachmentID(hostContainerID, "child-swarm-1")
	attachment, ok, err := topologyStore.GetAttachmentForAccount("account-1", attachmentID)
	if err != nil || !ok {
		t.Fatalf("get attachment ok=%t err=%v", ok, err)
	}
	if attachment.UserID != "user-1" || attachment.AccountScopeID != "account-1" {
		t.Fatalf("attachment account = %q/%q", attachment.UserID, attachment.AccountScopeID)
	}
	if attachment.DeploymentID != "deploy-1" {
		t.Fatalf("attachment deployment id = %q", attachment.DeploymentID)
	}
	runtimeRecord, ok, err := topologyStore.GetRuntimeForAccount("account-1", "child-swarm-1")
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.UserID != "user-1" || runtimeRecord.AccountScopeID != "account-1" {
		t.Fatalf("runtime account = %q/%q", runtimeRecord.UserID, runtimeRecord.AccountScopeID)
	}
	if runtimeRecord.OwnerHostContainerID != hostContainerID {
		t.Fatalf("runtime owner host container id = %q", runtimeRecord.OwnerHostContainerID)
	}
	placement, ok, err := topologyStore.GetRuntimePlacementForAccount("account-1", "child-swarm-1")
	if err != nil || !ok {
		t.Fatalf("get runtime placement ok=%t err=%v", ok, err)
	}
	if placement.RuntimeKind != pebblestore.TopologyRuntimeKindContainer || placement.AuthorityHostSwarmID != "managed-swarm" || placement.AuthorityContainerID != hostContainerID {
		t.Fatalf("unexpected runtime placement: %+v", placement)
	}
}

func TestLocalContainerDeleteUsesCanonicalAttachmentsForCleanup(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "local-topology-cleanup.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topologyStore := pebblestore.NewTopologyStore(store)
	swarmStore := pebblestore.NewSwarmStore(store, topologyStore)
	if _, err := swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{SwarmID: "host-swarm", Name: "Host", Role: "master"}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarmStore.PutTrustedPeer(pebblestore.SwarmTrustedPeerRecord{SwarmID: "child-swarm", Name: "Child", Relationship: "child"}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}
	localStore := pebblestore.NewSwarmLocalContainerStore(store)
	deploymentStore := pebblestore.NewDeployContainerStore(store)
	localSvc := localcontainers.NewService(localStore, deploymentStore, swarmStore, nil, nil, filepath.Join(t.TempDir(), "swarm.conf"), topologyStore)
	localSvc.SetControlContainerFuncForTest(func(context.Context, string, string, string) error { return nil })
	localSvc.SetInspectContainerFuncForTest(func(string, string) (string, string, error) { return "missing", "", nil })
	if _, err := localStore.Put(pebblestore.SwarmLocalContainerRecord{ID: "pc-child", Name: "pc child", ContainerName: "pc-child", Runtime: "podman", Status: "missing", ContainerID: "ctr-1", UserID: "user-1", AccountScopeID: "account-1"}); err != nil {
		t.Fatalf("put local container: %v", err)
	}
	if err := pebblestore.UpsertTopologyHostContainerForAccount(topologyStore, "account-1", pebblestore.TopologyHostContainerRecord{
		HostContainerID:     pebblestore.CanonicalTopologyHostContainerID("host-swarm", "ctr-1"),
		UserID:              "user-1",
		AccountScopeID:      "account-1",
		HostSwarmID:         "host-swarm",
		RuntimeContainerRef: "ctr-1",
		Name:                "pc child",
		ContainerName:       "pc-child",
		ContainerID:         "ctr-1",
		Runtime:             "podman",
		ObservedSources:     []string{pebblestore.TopologyHostContainerSourceSwarmLocalContainer},
	}); err != nil {
		t.Fatalf("put topology host container: %v", err)
	}
	if err := pebblestore.UpsertTopologyRuntimeRecordForAccount(topologyStore, "account-1", pebblestore.TopologyRuntimeRecord{SwarmID: "child-swarm", UserID: "user-1", AccountScopeID: "account-1", Name: "Child", Relationship: "child", ObservedSources: []string{pebblestore.TopologyRuntimeSourceDeployContainer}}); err != nil {
		t.Fatalf("put topology runtime: %v", err)
	}
	attachmentID := pebblestore.CanonicalTopologyAttachmentID(pebblestore.CanonicalTopologyHostContainerID("host-swarm", "ctr-1"), "child-swarm")
	if _, err := topologyStore.PutAttachmentForAccount("account-1", pebblestore.TopologyAttachmentRecord{AttachmentID: attachmentID, UserID: "user-1", AccountScopeID: "account-1", HostContainerID: pebblestore.CanonicalTopologyHostContainerID("host-swarm", "ctr-1"), RuntimeSwarmID: "child-swarm", DeploymentID: "deploy-1", State: "attached"}); err != nil {
		t.Fatalf("put topology attachment: %v", err)
	}
	if _, err := topologyStore.PutWorkspaceBindingForAccount("account-1", pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 "binding:replica:pc-child:/workspace",
		UserID:                    "user-1",
		AccountScopeID:            "account-1",
		SourceWorkspacePath:       "/workspace",
		SourceWorkspaceName:       "workspace",
		DestinationRuntimeSwarmID: "child-swarm",
		DestinationWorkspacePath:  "/workspaces/workspace",
		ReplicationMode:           "bundle",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("put topology workspace binding: %v", err)
	}
	if _, err := topologyStore.PutSessionRouteForAccount("account-1", pebblestore.TopologySessionRouteRecord{SessionID: "session-1", UserID: "user-1", AccountScopeID: "account-1", RuntimeSwarmID: "child-swarm", WorkspaceBindingID: "binding:replica:pc-child:/workspace", HostWorkspacePath: "/workspace", RuntimeWorkspacePath: "/workspaces/workspace"}); err != nil {
		t.Fatalf("put topology session route: %v", err)
	}

	result, err := localSvc.BulkDelete(testPrincipalContext(), []string{"pc-child"})
	if err != nil {
		t.Fatalf("bulk delete: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("result count = %d", result.Count)
	}
	if _, ok, err := topologyStore.GetAttachmentForAccount("account-1", attachmentID); err != nil || ok {
		t.Fatalf("attachment remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := topologyStore.GetHostContainerForAccount("account-1", pebblestore.CanonicalTopologyHostContainerID("host-swarm", "ctr-1")); err != nil || ok {
		t.Fatalf("host container remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := topologyStore.GetWorkspaceBindingForAccount("account-1", "binding:replica:pc-child:/workspace"); err != nil || ok {
		t.Fatalf("workspace binding remaining ok=%t err=%v", ok, err)
	}
	if _, ok, err := topologyStore.GetSessionRouteForAccount("account-1", "session-1"); err != nil || ok {
		t.Fatalf("session route remaining ok=%t err=%v", ok, err)
	}
}
