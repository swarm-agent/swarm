package deploy

import (
	"testing"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
)

func TestBuildLocalAttachApprovePayloadOmitsGroupWhenHostDidNotAssignOne(t *testing.T) {
	cfg := startupconfig.Default(t.TempDir() + "/swarm.conf")
	cfg.DeployContainer.DeploymentID = "deployment-1"
	cfg.DeployContainer.BootstrapSecret = "bootstrap-secret"
	state := swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{Name: "child"}}

	payload, err := buildLocalAttachApprovePayload(cfg, state, ContainerAttachState{
		HostSwarmID:              "managed-host-swarm",
		HostDisplayName:          "managed-host",
		HostBackendURL:           "http://127.0.0.1:7781",
		HostToChildPeerAuthToken: "host-to-child",
		ChildToHostPeerAuthToken: "child-to-host",
	})
	if err != nil {
		t.Fatalf("buildLocalAttachApprovePayload() error = %v", err)
	}
	for _, key := range []string{"group_id", "group_name", "group_network_name"} {
		if value, ok := payload[key]; ok {
			t.Fatalf("payload[%q] = %#v, want omitted", key, value)
		}
	}
}
