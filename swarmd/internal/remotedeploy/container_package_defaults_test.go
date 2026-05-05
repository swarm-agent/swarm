package remotedeploy

import (
	"testing"

	localcontainers "swarm/packages/swarmd/internal/localcontainers"
)

func TestRemotePackageDefaultsMatchLocalContainerDefaults(t *testing.T) {
	if RemotePackageBaseImage != localcontainers.SupportedPackageBaseImage {
		t.Fatalf("remote package base image %q does not match local container base image %q", RemotePackageBaseImage, localcontainers.SupportedPackageBaseImage)
	}
	if RemotePackageManager != localcontainers.DefaultPackageManager {
		t.Fatalf("remote package manager %q does not match local package manager %q", RemotePackageManager, localcontainers.DefaultPackageManager)
	}
}
