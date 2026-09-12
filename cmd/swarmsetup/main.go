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
	installUser, createUser := "", ""
	chooseAccount := false
	onboarding, desktop := false, false
	onboardingInstall := false
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
		case "--install-user", "--create-user":
			option := args[i]
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") || args[i+1] == "" {
				return fmt.Errorf("missing value for %s", option)
			}
			i++
			if installUser != "" || createUser != "" {
				return fmt.Errorf("choose only one installation account")
			}
			if option == "--install-user" {
				installUser = args[i]
			} else {
				createUser = args[i]
			}
		case "--onboarding-install":
			onboardingInstall = true
		case "--onboarding":
			onboarding = true
		case "--desktop":
			desktop = true
		case "--choose-account":
			chooseAccount = true
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

	if onboardingInstall {
		if os.Geteuid() != 0 || onboarding || desktop || applyRelease || chooseAccount || createServiceAccount || installUser != "" || createUser != "" || artifactRoot == "" || !installService {
			return fmt.Errorf("invalid recorded onboarding installation")
		}
		restore, err := launcher.SelectRecordedOnboardingInstall(artifactRoot)
		if err != nil {
			return err
		}
		defer restore()
	}
	if desktop && !onboarding {
		return fmt.Errorf("--desktop requires --onboarding")
	}
	if onboarding {
		if applyRelease || chooseAccount || createServiceAccount || installUser != "" || createUser != "" || artifactRoot == "" || !installService {
			return fmt.Errorf("onboarding requires one artifact root and service setup, without account overrides")
		}
		return runPrerequisiteOnboarding(artifactRoot, desktop)
	}
	if ((installUser != "" || createUser != "") && (createServiceAccount || chooseAccount)) || (createServiceAccount && chooseAccount) {
		return fmt.Errorf("choose only one account mode")
	}
	if applyRelease && (installUser != "" || createUser != "" || chooseAccount || createServiceAccount) {
		return fmt.Errorf("release updates cannot change installation identity")
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
	if chooseAccount && os.Geteuid() == 0 && artifactRoot != "" && installService {
		return runPrerequisiteOnboarding(artifactRoot, false)
	}
	// Non-root interactive installs already have their invoking OS identity.
	// Explicit administrative account flags remain available for automation.
	if installUser != "" || createUser != "" {
		name := installUser
		if createUser != "" {
			name = createUser
		}
		fmt.Fprintf(os.Stderr, "Installation account: %s. No sudo or SSH policy changes; OS password prompts stay on the terminal.\n", name)
		restore, err := launcher.SelectInstallationAccount(name, createUser != "")
		if err != nil {
			return err
		}
		defer restore()
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
	fmt.Fprintln(os.Stderr, "OS installation identity is not Swarm authentication. Open Swarm and complete its authenticated account setup or sign in; existing Swarm account/provider/workspace state is retained on retry.")
	if pathOnPATH(report.BinHome) {
		return nil
	}
	fmt.Fprintf(os.Stderr, "warning: %s is not on PATH; add it to use swarm/swarmdev/rebuild/swarmsetup directly\n", report.BinHome)
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage:
  swarmsetup [--no-service] [--choose-account | --install-user NAME | --create-user NAME | --create-service-account]
  swarmsetup --service [--create-service-account]
  swarmsetup --artifact-root /path/to/dist [--no-service]
  swarmsetup --artifact-root /path/to/dist --service
  swarmsetup --apply-release --lane main --target-version <tag> --asset-name <name> --asset-url <url> --sha256 <digest> [--parent-pid <pid>] [--relaunch-arg <arg>...]

Use --choose-account for an OS-terminal choice before provisioning: preserve the
existing/invoking identity, select --install-user NAME, or explicitly create a
human --create-user NAME as root using useradd and the OS passwd terminal prompt.
A cancelled password step retains the account; finish passwd and retry with
--install-user NAME. No sudo/SSH policy is modified and no password is collected
by Swarm. --create-service-account explicitly selects the locked non-login swarm
account for a fresh direct-root install. Existing installation ownership is never
silently reassigned. OS identity is not a Swarm login: complete authenticated
Swarm onboarding or sign in after installation.
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
