package sandbox

import (
	"os"

	"strings"
	"swarm/packages/swarmd/internal/egress"
	"testing"
)

// Tests must not inherit injected Git config from the caller/CI command router.
func TestMain(m *testing.M) {
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if strings.HasPrefix(key, "GIT_") {
			_ = os.Unsetenv(key)
		}
	}
	os.Exit(m.Run())
}

type registryBroker struct {
	registry *egress.Registry
	token    string
}

func (b *registryBroker) PrepareSandbox(root string) (SecretSetup, error) { return SecretSetup{}, nil }
func (b *registryBroker) ReleaseSandbox(root string)                      { b.registry.Unregister(root) }
