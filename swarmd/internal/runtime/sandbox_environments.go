package runtime

import (
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// sandboxEnvironmentResolver gives each folder's sandbox the last image its
// environment built successfully; a folder without one (or whose first build
// has not finished) runs on the default image.
type sandboxEnvironmentResolver struct {
	store *pebblestore.SandboxEnvironmentStore
}

func (r sandboxEnvironmentResolver) SandboxEnvironment(root string) (sandbox.EnvironmentImage, bool, error) {
	env, found, err := r.store.Get(root)
	if err != nil || !found || env.ReadyImage == "" {
		return sandbox.EnvironmentImage{}, false, err
	}
	return sandbox.EnvironmentImage{Image: env.ReadyImage, Path: env.ReadyPath}, true, nil
}
