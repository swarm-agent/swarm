package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/internal/client"
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
	applyRelease := false
	installService := false
	createServiceAccount := false
	lane := "main"
	plan := client.UpdateApplyPlan{}
	parentPID := 0
	var relaunchArgs []string
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
		case "--create-service-account":
			createServiceAccount = true
		case "--apply-release":
			applyRelease = true
		case "--service", "--systemd":
			installService = true
		case "--no-service", "--no-systemd", "--files-only":
			installService = false
		case "--lane":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			lane = strings.TrimSpace(args[i])
		case "--target-version":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			plan.TargetVersion = strings.TrimSpace(args[i])
		case "--asset-name":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			plan.AssetName = strings.TrimSpace(args[i])
		case "--asset-url":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			plan.AssetURL = strings.TrimSpace(args[i])
		case "--sha256":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			plan.SHA256 = strings.TrimSpace(args[i])
		case "--parent-pid":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			parsed, err := strconv.Atoi(strings.TrimSpace(args[i]))
			if err != nil || parsed < 0 {
				return fmt.Errorf("invalid parent pid: %s", args[i])
			}
			parentPID = parsed
		case "--relaunch-arg":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			i++
			relaunchArgs = append(relaunchArgs, args[i])
		default:
			return fmt.Errorf("unsupported argument: %s", args[i])
		}
	}

	if applyRelease {
		profile, err := launcher.LoadRuntimeProfile(lane, nil)
		if err != nil {
			return err
		}
		return launcher.RunUpdateHelper(profile, plan, parentPID, relaunchArgs)
	}

	if err := launcher.PreflightInstallation(installService); err != nil {
		return err
	}
	if createServiceAccount {
		fmt.Fprintln(os.Stderr, "Account consent: create locked non-root swarm with home /var/lib/swarm if needed. No sudo, password, interactive login, or SSH changes.")
		previous, present := os.LookupEnv("SWARM_CREATE_SERVICE_ACCOUNT")
		if err := os.Setenv("SWARM_CREATE_SERVICE_ACCOUNT", "1"); err != nil {
			return err
		}
		defer func() {
			if present {
				_ = os.Setenv("SWARM_CREATE_SERVICE_ACCOUNT", previous)
			} else {
				_ = os.Unsetenv("SWARM_CREATE_SERVICE_ACCOUNT")
			}
		}()
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
	if installService {
		if err := launcher.InstallInstalledService(); err != nil {
			return fmt.Errorf("runtime and launchers installed, but service installation/start failed (not rolled back): %w", err)
		}
		fmt.Println("installed runtime, launchers, and swarm.service:")
	} else {
		fmt.Println("installed runtime and launchers; no service installed or started:")
	}
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
  swarmsetup [--no-service] [--create-service-account]
  swarmsetup --service [--create-service-account]
  swarmsetup --artifact-root /path/to/dist [--no-service]
  swarmsetup --artifact-root /path/to/dist --service
  swarmsetup --apply-release --lane main --target-version <tag> --asset-name <name> --asset-url <url> --sha256 <digest> [--parent-pid <pid>] [--relaunch-arg <arg>...]

Root-only first install requires --create-service-account consent: Swarm creates
locked non-root swarm with home /var/lib/swarm; no password, sudo, login, or SSH
changes. Reinstall preserves consistent existing install directory ownership.
For a human login/password, an administrator can separately use the distribution's
interactive account tools (Ubuntu/Debian: adduser NAME, then passwd NAME if needed).
Optional sudo access is a separate administrator decision using the distribution's
sudo policy; never unlock or grant sudo to the swarm service account. Swarm does
not collect passwords or alter SSH access.
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
