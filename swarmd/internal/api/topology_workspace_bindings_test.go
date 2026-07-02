package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	topologyruntime "swarm/packages/swarmd/internal/topology"
)

func TestSwarmTopologyWorkspaceBindingsSupportsIdentityQueries(t *testing.T) {
	t.Parallel()

	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "topology-workspace-bindings.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	topologyStore := pebblestore.NewTopologyStore(store)
	topologyService := topologyruntime.NewService(topologyStore, nil, nil, nil, nil, nil, nil)
	server := NewServer(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologyService)

	principal := accountTestPrincipal()
	if _, err := topologyStore.PutRuntimeForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimeRecord{SwarmID: "runtime-self", UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Name: "runtime-self"}); err != nil {
		t.Fatalf("put runtime: %v", err)
	}
	if _, err := topologyStore.PutRuntimePlacementForAccount(principal.AccountScopeID, pebblestore.TopologyRuntimePlacementRecord{RuntimeSwarmID: "runtime-self", AccountScopeID: principal.AccountScopeID, AuthorityHostSwarmID: "runtime-self", RuntimeKind: pebblestore.TopologyRuntimeKindHost, PlacementGeneration: 1, State: pebblestore.TopologyRuntimePlacementStateActive}); err != nil {
		t.Fatalf("put placement: %v", err)
	}
	record, err := topologyStore.PutWorkspaceBindingForAccount(principal.AccountScopeID, pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                       "binding-identity",
		UserID:                          principal.UserID,
		AccountScopeID:                  principal.AccountScopeID,
		SourceWorkspaceID:               "workspace-identity",
		SourceWorkspaceGeneration:       3,
		SourceWorkspacePath:             "/workspace/source",
		SourceWorkspaceName:             "source",
		DestinationRuntimeSwarmID:       "runtime-self",
		DestinationAuthorityHostSwarmID: "runtime-self",
		DestinationRuntimeKind:          "host",
		DestinationWorkspacePath:        "/workspace/source",
		PlacementGeneration:             1,
		BindingGeneration:               1,
		State:                           "bound",
		AccessMode:                      pebblestore.TopologyWorkspaceBindingAccessModeReadWrite,
		MaterializationKind:             "source",
		AttestedByHostSwarmID:           "runtime-self",
		Writable:                        true,
	})
	if err != nil {
		t.Fatalf("put workspace binding: %v", err)
	}

	for _, target := range []string{
		"/v1/swarm/topology/workspace-bindings?source_workspace_id=workspace-identity",
		"/v1/swarm/topology/workspace-bindings?workspace_binding_id=" + record.BindingID,
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = requestWithTestPrincipalForAccount(req, principal.UserID, principal.AccountScopeID)
		resp := httptest.NewRecorder()

		server.handleSwarmTopologyWorkspaceBindings(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", target, resp.Code, resp.Body.String())
		}
		body := resp.Body.String()
		for _, want := range []string{
			`"binding_id":"binding-identity"`,
			`"workspace_binding_id":"binding-identity"`,
			`"source_workspace_id":"workspace-identity"`,
			`"source_workspace_generation":3`,
			`"destination_authority_host_swarm_id":"runtime-self"`,
			`"destination_runtime_kind":"host"`,
			`"placement_generation":1`,
			`"binding_generation":1`,
			`"state":"bound"`,
			`"access_mode":"read_write"`,
			`"materialization_kind":"source"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s response missing %s: %s", target, want, body)
			}
		}
	}
}
