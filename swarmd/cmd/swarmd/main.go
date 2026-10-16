package main

import (
	"context"
	"fmt"
	"os"
	"swarm/packages/swarmd/internal/environment"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"

	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/runtime"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		if err := runnerMain(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "swarmd runner: %v\n", err)
			os.Exit(1)
		}
		return
	}
	// Introspection must work even when the installed config cannot be parsed.
	if len(os.Args) == 3 && os.Args[1] == "config" && os.Args[2] == "removed-keys" {
		for _, key := range startupconfig.RemovedKeys() {
			fmt.Println(key.Name)
		}
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "config" && os.Args[2] == "cloudbuild-check" {
		if err := cloudBuildCheck(os.Args[3:]); err != nil {
			fmt.Fprintf(os.Stderr, "swarmd config cloudbuild-check: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("OK")
		return
	}
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarmd config error: %v\n", err)
		os.Exit(2)
	}

	daemon, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swarmd startup error: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if closeErr := daemon.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "swarmd shutdown error: %v\n", closeErr)
		}
	}()

	if err := daemon.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "swarmd runtime error: %v\n", err)
		os.Exit(1)
	}
}

func cloudBuildCheck(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	if cfg.CloudBuildProject == "" {
		return fmt.Errorf("cloudbuild_project is required")
	}
	if cfg.CloudBuildBucket == "" {
		return fmt.Errorf("cloudbuild_bucket is required")
	}
	// This permission-only probe has no folder check timeout. Use the same
	// resolver as Acquire; zero is valid and is derived per landing there.
	if _, err := environment.ResolveCloudBuildTimeout(cfg.CloudBuildTimeoutMinutes, 0); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rest, err := environment.NewGoogleREST(ctx, cfg.CloudBuildProject, cfg.CloudBuildRegion)
	if err != nil {
		return err
	}
	return environment.CheckCloudBuild(ctx, cfg.CloudBuildBucket, environment.GoogleStorage{REST: rest}, rest)
}
