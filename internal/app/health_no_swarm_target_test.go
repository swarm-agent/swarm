package app

import (
	"reflect"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
)

func TestHealthStatusDoesNotCarrySwarmID(t *testing.T) {
	typ := reflect.TypeOf(client.HealthStatus{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Name == "SwarmID" {
			t.Fatalf("HealthStatus must not carry SwarmID; use workspace overview swarm_target")
		}
	}
}
