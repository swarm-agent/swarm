package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"swarm-refactor/swarmtui/internal/launcher"
	"swarm-refactor/swarmtui/internal/updatehandoff"
	"swarm-refactor/swarmtui/pkg/startupconfig"
)

var defaultInvokedName = "swarm"

func main() {
	args := os.Args[1:]
	invoked := filepath.Base(os.Args[0])
	if strings.TrimSpace(defaultInvokedName) != "" {
		invoked = defaultInvokedName
	}
	if invoked == "swarmdev" {
		args = append([]string{"dev"}, args...)
	}
	if err := run(os.Args[0], args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(argv0 string, args []string) error {
	invoked := filepath.Base(argv0)
	if strings.TrimSpace(defaultInvokedName) != "" {
		invoked = defaultInvokedName
	}
	defaultLane := "main"
	if invoked == "swarmdev" {
		defaultLane = "dev"
	}
	lane := launcher.DefaultLane(defaultLane)
	if len(args) > 0 && (args[0] == "main" || args[0] == "dev") {
		lane = args[0]
		args = args[1:]
	}
	bypassOverride, bootstrap, args, err := parseLaunchFlags(args)
	if err != nil {
		return err
	}
	profile, err := launcher.LoadRuntimeProfile(lane, bypassOverride)
	if err != nil {
		return err
	}
	if os.Getenv("SWARM_PENDING_UPDATE_BOOT") != "" {
		if err := launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap}); err != nil {
			return err
		}
		return nil
	}
	if bootstrap.HasAny() {
		if profile.Startup.Exists {
			return startupconfig.BootstrapExistingConfigError(profile.Startup.Path)
		}
		if err := bootstrap.Validate(); err != nil {
			return err
		}
	}
	if len(args) > 0 && args[0] == "--desktop" {
		return runDesktop(profile, args[1:])
	}
	cmd := "run"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "help", "-h", "--help":
		usage()
		return nil
	case "ctl":
		if len(args) < 2 {
			return errors.New("missing swarmctl arguments")
		}
		return launcher.RunCtl(profile, args[1:], false)
	case "auth":
		if len(args) < 2 {
			return errors.New("missing swarmctl auth arguments")
		}
		return launcher.RunCtl(profile, args[1:], true)
	case "server":
		if len(args) < 2 {
			return errors.New("usage: swarm [main|dev] server <on|off|run|status>")
		}
		switch args[1] {
		case "on":
			return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
		case "off":
			return launcher.StopBackend(profile)
		case "run":
			return launcher.RunBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
		case "status":
			status := launcher.Status(profile)
			fmt.Printf("status=%s\nhealth=%s\nlane=%s\nlisten=%s\nurl=%s\npid=%s\n", status.Status, status.Health, profile.Lane, profile.Listen, profile.URL, status.PID)
			return nil
		default:
			return errors.New("usage: swarm [main|dev] server <on|off|run|status>")
		}
	case "backend-up":
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
	case "backend-down":
		return launcher.StopBackend(profile)
	case "backend-restart":
		if err := launcher.StopBackend(profile); err != nil {
			return err
		}
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
	case "backend-rebuild":
		buildProfile, err := loadBuildProfile(lane, bypassOverride)
		if err != nil {
			return err
		}
		if err := launcher.BuildSwarmdBinaries(buildProfile); err != nil {
			return err
		}
		if err := launcher.StopBackend(profile); err != nil {
			return err
		}
		return launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap})
	case "update":
		if len(args) < 2 || args[1] != "dev" {
			return errors.New("usage: swarm [main|dev] update dev")
		}
		buildProfile, err := loadBuildProfile(lane, bypassOverride)
		if err != nil {
			return err
		}
		return launcher.RunDevUpdate(buildProfile, nil)
	case "backend-build":
		buildProfile, err := loadBuildProfile(lane, bypassOverride)
		if err != nil {
			return err
		}
		if err := launcher.BuildSwarmdBinaries(buildProfile); err != nil {
			return err
		}
		return nil
	case "info":
		if err := launcher.RecordPortFile(profile); err != nil {
			return err
		}
		fmt.Printf("lane=%s\nlisten=%s\nurl=%s\nport=%d\nstate_root=%s\npid_file=%s\nlog_file=%s\nport_record=%s\nstartup_config=%s\nstartup_mode=%s\nbypass_permissions=%t\nswarm_bin_dir=%s\n",
			profile.Lane,
			profile.Listen,
			profile.URL,
			profile.LanePort,
			profile.StateRoot,
			profile.PIDFile,
			profile.LogFile,
			profile.PortRecord,
			profile.Startup.Path,
			profile.Startup.Mode,
			profile.Bypass,
			profile.BinDir,
		)
		return nil
	case "run":
		if len(args) > 0 {
			args = args[1:]
		}
	default:
		// treat all args as tui args
	}
	if err := launcher.StartBackend(profile, launcher.StartBackendOptions{BuildIfMissing: false, Bootstrap: bootstrap}); err != nil {
		return err
	}
	if err := launcher.RecordPortFile(profile); err != nil {
		return err
	}
	if err := launcher.RunTUI(profile, args); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == updatehandoff.ExitCodeDevUpdateRequested {
			buildProfile, err := loadBuildProfile(lane, bypassOverride)
			if err != nil {
				return err
			}
			return launcher.RunDevUpdate(buildProfile, args)
		}
		return err
	}
	return nil
}

func runDesktop(profile launcher.Profile, args []string) error {
	port := profile.DesktopPort
	for len(args) > 0 {
		switch args[0] {
		case "--port":
			if len(args) < 2 {
				return errors.New("missing value for --port")
			}
			parsed, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil || parsed < 1 || parsed > 65535 {
				return fmt.Errorf("invalid desktop port: %s (expected 1-65535)", args[1])
			}
			port = parsed
			args = args[2:]
		case "help", "-h", "--help":
			usage()
			return nil
		default:
			return fmt.Errorf("unsupported --desktop argument: %s", args[0])
		}
	}
	return launcher.RunDesktop(profile, port)
}

func loadBuildProfile(lane string, bypassOverride *bool) (launcher.Profile, error) {
	root, err := launcher.ResolveRoot()
	if err != nil {
		return launcher.Profile{}, fmt.Errorf("source checkout required for build commands: %w", err)
	}
	return launcher.LoadBuildProfile(root, lane, bypassOverride)
}

func parseLaunchFlags(args []string) (*bool, startupconfig.BootstrapFlags, []string, error) {
	out := make([]string, 0, len(args))
	var override *bool
	bootstrap := startupconfig.BootstrapFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--bypass-permissions":
			v := true
			override = &v
		case "--yolo":
			return nil, startupconfig.BootstrapFlags{}, nil, errors.New("--yolo was removed; use --bypass-permissions or set bypass_permissions in swarm.conf")
		case "--swarm-name":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --swarm-name")
			}
			i++
			bootstrap.SwarmName = args[i]
			bootstrap.SwarmNameSet = true
		case "--child":
			bootstrap.Child = true
			bootstrap.ChildSet = true
		case "--mode":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --mode")
			}
			i++
			bootstrap.Mode = args[i]
			bootstrap.ModeSet = true
		case "--advertise-host":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-host")
			}
			i++
			bootstrap.AdvertiseHost = args[i]
			bootstrap.AdvertiseHostSet = true
		case "--advertise-port":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --advertise-port")
			}
			i++
			parsed, err := strconv.Atoi(strings.TrimSpace(args[i]))
			if err != nil {
				return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", args[i])
			}
			bootstrap.AdvertisePort = parsed
			bootstrap.AdvertisePortSet = true
		case "--tailscale-url":
			if i+1 >= len(args) {
				return nil, startupconfig.BootstrapFlags{}, nil, errors.New("missing value for --tailscale-url")
			}
			i++
			bootstrap.TailscaleURL = args[i]
			bootstrap.TailscaleURLSet = true
		default:
			if value, ok := consumeInlineFlag(arg, "--swarm-name="); ok {
				bootstrap.SwarmName = value
				bootstrap.SwarmNameSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--child="); ok {
				parsed, err := strconv.ParseBool(strings.TrimSpace(value))
				if err != nil {
					return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --child %q (expected true or false)", value)
				}
				bootstrap.Child = parsed
				bootstrap.ChildSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--mode="); ok {
				bootstrap.Mode = value
				bootstrap.ModeSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-host="); ok {
				bootstrap.AdvertiseHost = value
				bootstrap.AdvertiseHostSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--advertise-port="); ok {
				parsed, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil {
					return nil, startupconfig.BootstrapFlags{}, nil, fmt.Errorf("invalid --advertise-port %q (expected 1-65535)", value)
				}
				bootstrap.AdvertisePort = parsed
				bootstrap.AdvertisePortSet = true
				continue
			}
			if value, ok := consumeInlineFlag(arg, "--tailscale-url="); ok {
				bootstrap.TailscaleURL = value
				bootstrap.TailscaleURLSet = true
				continue
			}
			out = append(out, arg)
		}
	}
	return override, bootstrap, out, nil
}

func consumeInlineFlag(arg, prefix string) (string, bool) {
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func usage() {
	fmt.Print(`swarm launcher

Usage:
  swarm [main|dev] [run] [--swarm-name NAME] [--child] [--mode lan|tailscale] [--advertise-host HOST] [--advertise-port PORT] [--tailscale-url URL] [tui-args...]
  swarm [main|dev] --desktop [--port N]
  swarm [main|dev] server <on|off|run|status>
  swarm [main|dev] ctl <swarmctl-args...>
  swarm [main|dev] auth <swarmctl-auth-args...>
  swarm [main|dev] backend-up
  swarm [main|dev] backend-down
  swarm [main|dev] backend-restart
  swarm [main|dev] backend-rebuild
  swarm [main|dev] backend-build
  swarm [main|dev] update dev
  swarm [main|dev] info
  swarm help

Alias:
  swarmdev [run] [--swarm-name NAME] [--child] [--mode lan|tailscale] [--advertise-host HOST] [--advertise-port PORT] [--tailscale-url URL] [tui-args...]
  swarmdev --desktop [--port N]
  swarmdev server <on|off|run|status>
  swarmdev ctl <swarmctl-args...>
  swarmdev auth <swarmctl-auth-args...>
  swarmdev backend-up|down|restart|rebuild|build|info
  swarmdev update dev
`)
}
