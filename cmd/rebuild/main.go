package main

import (
	"fmt"
	"os"

	"swarm-refactor/swarmtui/internal/launcher"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := launcher.ResolveRoot()
	if err != nil {
		return err
	}
	lane := launcher.DefaultLane("main")
	includeWeb := false
	restartSystemd := false
	for _, arg := range args {
		switch arg {
		case "", "f", "full", "frontend":
			if arg != "" {
				includeWeb = true
			}
		case "s", "systemd":
			restartSystemd = true
			includeWeb = true
		case "main", "dev":
			lane = arg
		default:
			return fmt.Errorf("usage: rebuild [main|dev] [f] [s]")
		}
	}
	profile, err := launcher.LoadBuildProfile(root, lane, nil)
	if err != nil {
		return err
	}
	if err := launcher.BuildToolBinaries(root, map[string]bool{"rebuild": true}); err != nil {
		return err
	}
	return launcher.Rebuild(profile, includeWeb, restartSystemd)
}
