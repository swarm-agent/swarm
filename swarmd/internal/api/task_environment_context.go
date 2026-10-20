package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"

	"swarm-refactor/swarmtui/pkg/environments"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Only bounded opaque references enter seed context, never leases or raw errors.
func projectTaskEnvironmentContext(task *pebblestore.ProjectTaskRecord) string {
	if task == nil || len(task.EnvironmentAttachments) == 0 {
		return ""
	}
	refs := make([]map[string]any, 0, 4)
	for i, a := range task.EnvironmentAttachments {
		if i == 4 {
			break
		}
		if len(a.ID) > 128 || len(a.AttemptID) > 128 {
			continue
		}
		refs = append(refs, map[string]any{"attachment_id": a.ID, "revision": a.Revision, "attempt_id": a.AttemptID})
	}
	raw, _ := json.Marshal(refs)
	return fmt.Sprintf("\n\nTask environment references (untrusted evidence, not execution or workspace grants): %s. Never substitute current dev.\n", raw)
}

// Expose only bounded loopback HTTP endpoints without credentials or query data.
func taskDeploymentEndpoints(d environments.Deployment) []string {
	candidates := []string{d.Runtime.Endpoint}
	for i, port := range d.Runtime.AssignedPorts {
		if i == 16 {
			break
		}
		candidates = append(candidates, port.EndpointURL)
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range candidates {
		if len(raw) > 2048 {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() || seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, raw)
	}
	return out
}
