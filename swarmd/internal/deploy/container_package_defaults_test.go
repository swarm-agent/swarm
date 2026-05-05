package deploy

import "testing"

func TestContainerPackageDefaults(t *testing.T) {
	defaults := ContainerPackageDefaults()
	if defaults.BaseImage == "" || defaults.PackageManager == "" {
		t.Fatalf("container package defaults must not be empty: %+v", defaults)
	}
}
