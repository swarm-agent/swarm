package provider

import (
	"context"
	"encoding/json"
	"errors"
	"swarm-refactor/swarmtui/pkg/environments"
)

func validateSSHBuildDeployment(conn *environments.Connection, dep *environments.Deployment) error {
	if dep == nil || dep.Build == nil {
		return nil
	}
	b := dep.Build
	if conn == nil || conn.Kind != environments.ConnectionKindSSH || conn.AccountScopeID != dep.AccountScopeID || conn.ID != dep.ConnectionID || b.ConnectionID != conn.ID || b.ConnectionDigest == "" || b.ConnectionDigest != environments.ConnectionTransportDigest(conn) || !environments.ValidBuildImageID(b.ImageID) || len(b.ContextDigest) != 64 || len(b.DefinitionDigest) != 64 || b.OperationID == "" {
		return errors.New("SSH deployment build receipt or connection mismatch")
	}
	return nil
}

// Delete only an observed owned container, by immutable ID. An unavailable
// inspect is uncertainty, not proof of absence. No broad host path cleanup.
func (p *SSHDockerProvider) cleanupSSHDeployment(ctx context.Context, conn *environments.Connection, dep *environments.Deployment, target string) error {
	if err := validateSSHBuildDeployment(conn, dep); err != nil {
		return err
	}
	out, err := p.runSSH(ctx, conn, "docker", "inspect", target)
	if err != nil {
		return errors.Join(ErrOperationNotConfirmed, errors.New("owned remote container inspect unavailable"))
	}
	var records []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(out, &records) != nil || len(records) != 1 || records[0].ID == "" || records[0].Config.Labels["swarm.deployment_id"] != dep.ID || records[0].Config.Labels["swarm.account_scope_id"] != dep.AccountScopeID || records[0].Config.Labels["swarm.build_receipt"] != dep.Build.OperationID {
		return errors.New("remote container cleanup ownership mismatch; retained")
	}
	_, err = p.runSSH(ctx, conn, "docker", "rm", "-f", "-v", records[0].ID)
	return err
}
