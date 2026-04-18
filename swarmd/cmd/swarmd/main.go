package main

import (
	"fmt"
	"os"

	"swarm/packages/swarmd/internal/config"
	"swarm/packages/swarmd/internal/runtime"
)

func main() {
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
