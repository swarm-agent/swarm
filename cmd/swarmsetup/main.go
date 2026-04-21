package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"swarm-refactor/swarmtui/internal/launcher"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	artifactRoot := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			usage()
			return nil
		case "--artifact-root":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			artifactRoot = strings.TrimSpace(args[i])
		default:
			return fmt.Errorf("unsupported argument: %s", args[i])
		}
	}

	var (
		report launcher.InstallReport
		err    error
	)
	if artifactRoot != "" {
		report, err = launcher.InstallRuntimeFromArtifact(artifactRoot)
	} else {
		var root string
		root, err = launcher.ResolveRoot()
		if err != nil {
			return err
		}
		if err := launcher.BuildToolBinaries(root, nil); err != nil {
			return err
		}
		report, err = launcher.InstallLaunchers(root)
	}
	if err != nil {
		return err
	}
	fmt.Println("installed launchers:")
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
		target := report.Links[name]
		fmt.Printf("  %s -> %s\n", filepath.Join(report.BinHome, name), target)
	}
	if pathOnPATH(report.BinHome) {
		return nil
	}
	fmt.Fprintf(os.Stderr, "warning: %s is not on PATH; add it to use swarm/swarmdev/rebuild/swarmsetup directly\n", report.BinHome)
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  swarmsetup
  swarmsetup --artifact-root /path/to/dist
`)
}

func pathOnPATH(dir string) bool {
	dir = filepath.Clean(dir)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(strings.TrimSpace(entry)) == dir {
			return true
		}
	}
	return false
}
