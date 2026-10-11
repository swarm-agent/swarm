package runtime

import (
	"context"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/api"
	"swarm/packages/swarmd/internal/config"
)

// minServeAppCapsVersion is the first Tailscale release whose Serve sets
// Tailscale-App-Capabilities and drops any copy the caller sends. An older
// Serve could pass a caller's own header through, so it is never trusted.
var minServeAppCapsVersion = [2]int{1, 92}

// sdkListenerHandler picks the handler for the SDK listener: the tailnet
// gateway when --tailnet-identity is on and the local Tailscale is new enough,
// else bearer credentials only.
func sdkListenerHandler(cfg config.Config, apiServer *api.Server) http.Handler {
	if !cfg.TailnetIdentity {
		return apiServer.ContainerSDKHandler()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "version").Output()
	if err != nil || !tailscaleVersionAtLeast(string(out), minServeAppCapsVersion) {
		log.Printf("swarmd tailnet identity off: Tailscale %d.%d or newer is required (found %q, %v); AI keys still work",
			minServeAppCapsVersion[0], minServeAppCapsVersion[1], strings.TrimSpace(firstLine(string(out))), err)
		return apiServer.ContainerSDKHandler()
	}
	log.Printf("swarmd tailnet identity on: Swarm Control admits devices granted %s", api.SwarmTailnetCapability)
	return apiServer.TailnetGatewayHandler()
}

// tailscaleVersionAtLeast parses the first line of `tailscale version`
// (for example "1.92.3" or "1.92.3-t1234abcd").
func tailscaleVersionAtLeast(output string, min [2]int) bool {
	fields := strings.SplitN(strings.TrimSpace(firstLine(output)), ".", 3)
	if len(fields) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(fields[0])
	minor, err2 := strconv.Atoi(strings.SplitN(fields[1], "-", 2)[0])
	if err1 != nil || err2 != nil {
		return false
	}
	return major > min[0] || (major == min[0] && minor >= min[1])
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
