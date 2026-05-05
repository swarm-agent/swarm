package deploy

import localcontainers "swarm/packages/swarmd/internal/localcontainers"

const (
	DefaultContainerPackageBaseImage      = localcontainers.SupportedPackageBaseImage
	DefaultContainerPackagePackageManager = localcontainers.DefaultPackageManager
)

func ContainerPackageDefaults() ContainerPackageManifest {
	return ContainerPackageManifest{
		BaseImage:      DefaultContainerPackageBaseImage,
		PackageManager: DefaultContainerPackagePackageManager,
	}
}
