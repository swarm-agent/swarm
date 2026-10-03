package lifecycle

import "swarm-refactor/swarmtui/pkg/environments"

// Record the actual local source selected by provisioning, not merely a caller's
// requested path. Other strategies cannot attest to a local committed tree.
func deploymentWorkspacePath(env environments.Environment, requested, connectionKind string) string {
	strategy := env.Provisioning.Strategy
	if connectionKind != "local_docker" || strategy.Kind != environments.SourceStrategyKindLocalMount || strategy.LocalMount == nil {
		return ""
	}
	if strategy.LocalMount.HostPath != "" {
		return strategy.LocalMount.HostPath
	}
	return requested
}
