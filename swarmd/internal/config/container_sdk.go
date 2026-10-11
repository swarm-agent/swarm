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
		if cfg.TailnetIdentity {
			return errors.New("tailnet-identity requires container-sdk-port")
		}
		return nil
	}
	if cfg.ContainerSDKPort < 1 || cfg.ContainerSDKPort > 65535 {
		return errors.New("container-sdk-port must be 0 or 1-65535")
	}
	// Tool permissions are the owner's choice (the owner-only bypass setting,
	// persisted in the startup config), not a property of this listener: the
	// headless setup lets the owner run agents without prompts inside the
	// container. --lock-permission-policy keeps them on regardless.
	if cfg.DesktopPort != 0 {
		return errors.New("container SDK requires desktop-port=0")
	}
	if ip := net.ParseIP(cfg.ContainerSDKHost); ip == nil || ip.To4() == nil || !(ip.IsLoopback() || ip.IsUnspecified()) {
		return errors.New("container-sdk-host must be 127.0.0.1 (host service) or 0.0.0.0 (inside a container)")
	}
	// The Serve header is trusted only when nothing but Serve on this machine
	// can reach the listener.
	if cfg.TailnetIdentity && !net.ParseIP(cfg.ContainerSDKHost).IsLoopback() {
		return errors.New("tailnet-identity requires container-sdk-host=127.0.0.1 behind Tailscale Serve")
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
