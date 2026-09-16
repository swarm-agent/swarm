//go:build !linux

package launcher

import (
	"errors"
	"os/user"
)

func installOnboardingSSHKey(_ *user.User, _ []byte, _ func() error) error {
	return errors.New("safe SSH key provisioning requires Linux; skip SSH setup on this platform")
}
