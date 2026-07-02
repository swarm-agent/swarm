package deploy

const (
	DefaultContainerPackageBaseImage      = "docker.io/ubuntu:26.04"
	DefaultContainerPackagePackageManager = "apt"
)

func ContainerPackageDefaults() ContainerPackageManifest {
	return ContainerPackageManifest{
		BaseImage:      DefaultContainerPackageBaseImage,
		PackageManager: DefaultContainerPackagePackageManager,
	}
}
