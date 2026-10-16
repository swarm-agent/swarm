package api

import (
	"context"
	"fmt"
	"log"
	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/sandbox"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
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
			local = environment.Auto{Remote: p.server.cloudBuildProvider, Local: local, RemoteName: "cloud build"}
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

// ConfigureCloudBuild resolves ADC before accepting Cloud Build landings.
func (s *Server) ConfigureCloudBuild(ctx context.Context, name string, cfg environment.CloudBuildConfig, store *pebblestore.Store) error {
	if name != "gcp-cloudbuild" && (name != "auto" || cfg.Project == "" || cfg.Bucket == "") {
		return nil
	}
	var err error
	var rest environment.GoogleREST
	if cfg.Project == "" || cfg.Bucket == "" {
		err = fmt.Errorf("cloudbuild_project and cloudbuild_bucket are required")
	} else {
		rest, err = environment.NewGoogleREST(ctx, cfg.Project, cfg.Region)
	}
	if err != nil {
		if name == "auto" {
			log.Printf("warning: cloud build disabled, falling back: %v", err)
			return nil
		}
		return err
	}
	s.SetCloudBuildProvider(environment.CloudBuild{Store: store, Config: cfg, Storage: environment.GoogleStorage{REST: rest}, Builds: rest})
	return nil
}

// SetCloudBuildProvider injects the configured provider at startup.
func (s *Server) SetCloudBuildProvider(p environment.Provider) { s.cloudBuildProvider = p }
