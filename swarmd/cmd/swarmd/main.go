package main

import (
	"fmt"
	"os"

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
