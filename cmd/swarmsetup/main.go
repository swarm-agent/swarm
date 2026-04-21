package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"swarm-refactor/swarmtui/internal/launcher"
)

func main() {
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "--artifact-root" {
		report, err := launcher.InstallReleaseBundle(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		printInstallReport(report)
		return
	}
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: swarmsetup [--artifact-root <path>]")
		os.Exit(2)
	}

	root, err := launcher.ResolveRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := launcher.BuildToolBinaries(root, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report, err := launcher.InstallLaunchers(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printInstallReport(report)
}

func printInstallReport(report launcher.InstallReport) {
	fmt.Println("installed launchers:")
	for _, name := range []string{"swarm", "swarmdev", "rebuild", "swarmsetup"} {
		target := report.Links[name]
		fmt.Printf("  %s -> %s\n", filepath.Join(report.BinHome, name), target)
	}
	if pathOnPATH(report.BinHome) {
		return
	}
	fmt.Fprintf(os.Stderr, "warning: %s is not on PATH; add it to use swarm/swarmdev/rebuild/swarmsetup directly\n", report.BinHome)
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
