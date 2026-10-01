package config

import (
	"errors"
	"net"
	"strconv"
)

// The ordinary API remains loopback-only. This separate listener is an explicit
// deployment choice, never persisted into host startup defaults.
func validateContainerSDK(cfg Config) error {
	if cfg.ContainerSDKPort == 0 {
		return nil
	}
	if cfg.ContainerSDKPort < 1 || cfg.ContainerSDKPort > 65535 {
		return errors.New("container-sdk-port must be 0 or 1-65535")
	}
	if cfg.DesktopPort != 0 || cfg.BypassPermissions {
		return errors.New("container SDK requires desktop-port=0 and permissions enabled")
	}
	_, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return err
	}
	if port == strconv.Itoa(cfg.ContainerSDKPort) || cfg.PeerTransportPort == cfg.ContainerSDKPort {
		return errors.New("container SDK port must differ from API and peer ports")
	}
	return nil
}
