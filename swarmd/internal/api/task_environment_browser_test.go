package api

import (
	"sync/atomic"

	"swarm-refactor/swarmtui/pkg/environments"
)

type browserConnectionFixture struct {
	manageConnectionStore
	connection environments.Connection
}

func (f *browserConnectionFixture) Get(string, string, string) (environments.Connection, bool, error) {
	return f.connection, true, nil
}

type browserDeploymentFixture struct {
	*taskAttachmentLifecycleFixture
	stopped *atomic.Bool
}

func (f *browserDeploymentFixture) GetDeployment(a, w, id string) (environments.Deployment, bool, error) {
	d, found, err := f.taskAttachmentLifecycleFixture.GetDeployment(a, w, id)
	if f.stopped.Load() {
		d.Status = environments.DeploymentStatusStopped
	}
	return d, found, err
}
