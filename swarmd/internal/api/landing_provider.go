package api

import (
	"context"
	"log"
	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/sandbox"
	"time"
)

type landingProvider struct {
	server *Server
	name   string
	wait   time.Duration
}

func (p landingProvider) Acquire(ctx context.Context, spec environment.Spec) (environment.Environment, error) {
	selected := spec.Selected
	spec.Selected = func(name string) {
		log.Printf("landing environment: %s", name)
		if selected != nil {
			selected(name)
		}
	}
	var local environment.Provider = environment.Local{Manager: sandbox.Default()}
	remote := environment.Runners{Store: p.server.runnerStore, Wait: p.wait}
	switch p.name {
	case "runners":
		return remote.Acquire(ctx, spec)
	case "gcp-cloudbuild":
		if p.server.cloudBuildProvider == nil {
			return nil, &environment.Failure{Reason: "Cloud Build is not configured"}
		}
		return p.server.cloudBuildProvider.Acquire(ctx, spec)
	case "auto":
		if p.server.cloudBuildProvider != nil {
			local = environment.Auto{Remote: p.server.cloudBuildProvider, Local: local}
		}
		return (environment.Auto{Remote: remote, Local: local}).Acquire(ctx, spec)
	default:
		e, err := local.Acquire(ctx, spec)
		if err == nil {
			spec.Selected("local")
		}
		return e, err
	}
}

// SetCloudBuildProvider injects the configured provider at startup.
func (s *Server) SetCloudBuildProvider(p environment.Provider) { s.cloudBuildProvider = p }
