//go:build !linux

package provider

import "errors"

func localPodmanEnvironment() ([]string, error) {
	return nil, errors.New("local Podman requires a local rootless Linux engine")
}
