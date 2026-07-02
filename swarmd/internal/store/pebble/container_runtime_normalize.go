package pebblestore

import "strings"

func normalizeContainerRuntime(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "podman", "docker":
		return value
	default:
		return strings.TrimSpace(value)
	}
}

func normalizeSwarmLocalContainerRuntime(value string) string {
	return normalizeContainerRuntime(value)
}
