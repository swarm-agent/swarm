package config

import (
	"fmt"
	"strings"
)

// HeadlessSandboxMode prevents startup-dependent host fallback. Empty direct
// constructor configuration has the same required default as the headless CLI.
// Host execution requires an explicit off selection; Desktop retains auto.
func HeadlessSandboxMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "required":
		return "required", nil
	case "off":
		return "off", nil
	default:
		return "", fmt.Errorf("headless --sandbox must be required (default; blocks commands if unavailable) or explicitly off (host execution); auto host fallback is not supported")
	}
}
