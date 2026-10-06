package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
)

// Purpose: SSHDockerProvider cannot admit forged/imported managed-build receipts
// without connection provenance via Deploy, Inspect, ResolveAccess or Exec. The
// provider layer is the narrowest boundary proving zero SSH commands/resources;
// ordinary SSH deployments without build provenance retain their existing path.
func TestSSHManagedBuildReceiptBeforeTransport(t *testing.T) {
	runner := newMockSSHRunner()
	p := NewSSHDockerProvider(runner)
	q := deploymentImageRequest(environments.ConnectionKindLocalDocker, "sha256:"+deploymentImageDigest)
	q.Connection.Kind = environments.ConnectionKindSSH
	q.Connection.SSH = &environments.SSHConfig{Host: "example.invalid", User: "builder"}
	q.Deployment.Runtime.ContainerID = "cached-container"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := p.Deploy(ctx, q); err == nil || out != nil {
		t.Fatalf("managed deployment admitted: %+v %v", out, err)
	}
	if out, err := p.Inspect(ctx, q.Connection, q.Deployment); err == nil || out != nil {
		t.Fatalf("managed inspection admitted: %+v %v", out, err)
	}
	if out, err := p.ResolveAccess(ctx, q.Connection, q.Deployment); err == nil || out != nil {
		t.Fatalf("cached access bypassed capability: %+v %v", out, err)
	}
	if out, err := p.Exec(ctx, q.Connection, q.Deployment, ExecRequest{}); err == nil || out != nil {
		t.Fatalf("managed execution admitted: %+v %v", out, err)
	}
	if len(runner.calls) != 0 || len(runner.containers) != 0 {
		t.Fatal("capability rejection touched SSH transport/resources")
	}
	q.Deployment.Build = nil
	if _, err := p.Inspect(ctx, q.Connection, q.Deployment); err == nil || errors.Is(err, environments.ErrSSHManagedBuildUnavailable) {
		t.Fatalf("ordinary SSH inspection contract changed: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatal("ordinary SSH inspection did not reach transport")
	}
}
