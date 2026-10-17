package config

import (
	"fmt"
	"net"
	"strconv"
)

func validateRunnerGateway(cfg Config) error {
	port := cfg.RunnerGatewayPort
	if port == 0 {
		return nil
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("runner-gateway-port must be 0 or 1-65535")
	}
	ports := []int{cfg.DesktopPort, cfg.ContainerSDKPort, cfg.PeerTransportPort}
	addresses := []string{cfg.ListenAddr}
	if cfg.SecretsGateway == "on" {
		addresses = append(addresses, cfg.SecretsGatewayAddr)
	}
	for _, addr := range addresses {
		_, text, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		other, err := strconv.Atoi(text)
		if err != nil {
			return err
		}
		ports = append(ports, other)
	}
	for _, other := range ports {
		if port == other {
			return fmt.Errorf("runner-gateway-port must differ from every other configured listener port")
		}
	}
	return nil
}
