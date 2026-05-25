package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const InstalledServiceUnit = "swarm.service"

type InstalledServiceStatus struct {
	Unit             string
	Scope            string
	UnitPath         string
	SystemdAvailable bool
	Installed        bool
	Enabled          string
	Active           string
	Daemon           ServerStatus
	InstallGuidance  string
	StartGuidance    string
}

type UninstallOptions struct {
	Purge bool
}

type InstallServiceOptions struct {
	Service bool
}

func systemdSystemUnitPath() string {
	return filepath.Join(string(filepath.Separator), "etc", "systemd", "system", InstalledServiceUnit)
}

func InstalledServicePlan() []string {
	return InstalledInstallPlan(InstallServiceOptions{Service: true})
}

func InstalledInstallPlan(opts InstallServiceOptions) []string {
	serviceLine := "Service: none; install runtime/files only and exit without starting Swarm"
	if opts.Service {
		serviceLine = "Service: " + systemdSystemUnitPath() + ", enabled and started with systemd"
	}
	return []string{
		"Runtime: " + systemInstallRoot(),
		"Launchers: " + filepath.Join(systemBinDir(), "swarm") + ", " + filepath.Join(systemBinDir(), "swarmdev") + ", " + filepath.Join(systemBinDir(), "rebuild") + ", " + filepath.Join(systemBinDir(), "swarmsetup"),
		"Daemon config: /etc/swarmd",
		"Daemon data: /var/lib/swarmd",
		"Daemon runtime: /run/swarmd",
		serviceLine,
	}
}

func UninstallPlan(opts UninstallOptions) []string {
	paths := []string{
		"disable and stop " + InstalledServiceUnit,
		"remove " + systemdSystemUnitPath(),
		"remove /etc/tmpfiles.d/swarmd.conf",
		"remove " + filepath.Join(systemBinDir(), "swarm") + ", " + filepath.Join(systemBinDir(), "swarmdev") + ", " + filepath.Join(systemBinDir(), "rebuild") + ", " + filepath.Join(systemBinDir(), "swarmsetup"),
		"remove " + systemInstallRoot(),
		"remove /run/swarmd",
	}
	if opts.Purge {
		paths = append(paths,
			"purge /etc/swarmd",
			"purge /var/lib/swarmd",
			"purge /var/cache/swarmd",
			"purge /var/log/swarmd",
		)
	} else {
		paths = append(paths,
			"preserve /etc/swarmd",
			"preserve /var/lib/swarmd",
			"preserve /var/cache/swarmd",
			"preserve /var/log/swarmd",
		)
	}
	return paths
}

func EnsureInstalledRuntimeReady() error {
	checks := []struct {
		label string
		path  string
	}{
		{label: "swarm launcher", path: filepath.Join(systemBinDir(), "swarm")},
		{label: "swarmd binary", path: filepath.Join(systemBinaryDir(), "swarmd")},
		{label: "swarmctl binary", path: filepath.Join(systemBinaryDir(), "swarmctl")},
		{label: "TUI binary", path: filepath.Join(systemBinaryDir(), "swarmtui")},
	}
	for _, check := range checks {
		if !isExecutable(check.path) {
			return fmt.Errorf("installed %s is missing at %s; install Swarm with install.sh before managing the daemon service", check.label, check.path)
		}
	}
	return nil
}

func InstalledServiceStatusForProfile(profile Profile) InstalledServiceStatus {
	status := InstalledServiceStatus{
		Unit:            InstalledServiceUnit,
		Scope:           string(systemdServiceSystem),
		UnitPath:        systemdSystemUnitPath(),
		Enabled:         "unknown",
		Active:          "unknown",
		Daemon:          Status(profile),
		InstallGuidance: "Swarm is not installed as a system service. Install with install.sh or run: swarm install --yes",
		StartGuidance:   "Swarm service is installed but not running. Start it with: swarm start",
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		status.SystemdAvailable = true
	}
	if _, err := os.Stat(systemdSystemUnitPath()); err == nil {
		status.Installed = true
	} else if active, installed, err := serviceActiveForScope(systemdServiceSystem, InstalledServiceUnit); err == nil && (active || installed) {
		status.Installed = true
	}
	if status.SystemdAvailable {
		if enabled, ok := systemdIsEnabled(systemdServiceSystem, InstalledServiceUnit); ok {
			status.Enabled = enabled
		}
		if active, installed, err := serviceActiveForScope(systemdServiceSystem, InstalledServiceUnit); err == nil {
			if active {
				status.Active = "active"
				status.Installed = true
			} else if installed || status.Installed {
				status.Active = "inactive"
				status.Installed = true
			} else {
				status.Active = "not-installed"
			}
		}
	} else if !status.Installed {
		status.Active = "unavailable"
	}
	return status
}

func EnsureInstalledDaemonReady(profile Profile) error {
	status := InstalledServiceStatusForProfile(profile)
	if !status.Installed {
		return errors.New(status.InstallGuidance)
	}
	if status.Daemon.Health != "healthy" {
		return fmt.Errorf("Swarm daemon is not ready at %s (service=%s, daemon=%s/%s). %s", profile.URL, status.Active, status.Daemon.Status, status.Daemon.Health, status.StartGuidance)
	}
	return nil
}

func WaitForInstalledDaemonReady(profile Profile) error {
	if err := waitForHealth(profile, 100); err != nil {
		return err
	}
	return nil
}

func DesktopURL(profile Profile, port int) string {
	if port == 0 {
		port = profile.DesktopPort
	}
	return desktopFrontendURL(profile, port)
}

func InstallInstalledService() error {
	return InstallInstalledDaemon(InstallServiceOptions{Service: true})
}

func InstallInstalledDaemon(opts InstallServiceOptions) error {
	if err := EnsureInstalledRuntimeReady(); err != nil {
		return err
	}
	if err := EnsureSystemInstallReady(); err != nil {
		return err
	}
	if !opts.Service {
		return nil
	}
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if err := EnsureSystemdServiceUnit(); err != nil {
		return err
	}
	return EnableStartInstalledService()
}

func EnableStartInstalledService() error {
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if err := requireInstalledServiceUnit(); err != nil {
		return err
	}
	if err := runPrivilegedCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if err := runPrivilegedCommand("systemctl", "enable", "--now", InstalledServiceUnit); err != nil {
		return fmt.Errorf("enable/start %s: %w", InstalledServiceUnit, err)
	}
	return nil
}

func StartInstalledService() error {
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if err := requireInstalledServiceUnit(); err != nil {
		return err
	}
	if err := runPrivilegedCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if err := runPrivilegedCommand("systemctl", "start", InstalledServiceUnit); err != nil {
		return fmt.Errorf("start %s: %w", InstalledServiceUnit, err)
	}
	return nil
}

func StopInstalledService() error {
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if err := requireInstalledServiceUnit(); err != nil {
		return err
	}
	if err := runPrivilegedCommand("systemctl", "stop", InstalledServiceUnit); err != nil {
		return fmt.Errorf("stop %s: %w", InstalledServiceUnit, err)
	}
	return nil
}

func RestartInstalledService() error {
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if err := requireInstalledServiceUnit(); err != nil {
		return err
	}
	if err := runPrivilegedCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if err := runPrivilegedCommand("systemctl", "restart", InstalledServiceUnit); err != nil {
		return fmt.Errorf("restart %s: %w", InstalledServiceUnit, err)
	}
	return nil
}

func UninstallInstalledService(opts UninstallOptions) error {
	if err := requireSystemdHost(); err != nil {
		return err
	}
	if _, err := os.Stat(systemdSystemUnitPath()); err == nil {
		_ = runPrivilegedCommand("systemctl", "stop", InstalledServiceUnit)
		_ = runPrivilegedCommand("systemctl", "disable", InstalledServiceUnit)
	}
	removeFiles := []string{
		systemdSystemUnitPath(),
		filepath.Join(string(filepath.Separator), "etc", "tmpfiles.d", "swarmd.conf"),
		filepath.Join(systemBinDir(), "swarm"),
		filepath.Join(systemBinDir(), "swarmdev"),
		filepath.Join(systemBinDir(), "rebuild"),
		filepath.Join(systemBinDir(), "swarmsetup"),
	}
	for _, path := range removeFiles {
		if err := removePath(path); err != nil {
			return err
		}
	}
	removeDirs := []string{
		systemInstallRoot(),
		filepath.Join(string(filepath.Separator), "run", "swarmd"),
	}
	if opts.Purge {
		removeDirs = append(removeDirs,
			filepath.Join(string(filepath.Separator), "etc", "swarmd"),
			filepath.Join(string(filepath.Separator), "var", "lib", "swarmd"),
			filepath.Join(string(filepath.Separator), "var", "cache", "swarmd"),
			filepath.Join(string(filepath.Separator), "var", "log", "swarmd"),
		)
	}
	for _, path := range removeDirs {
		if err := removePath(path); err != nil {
			return err
		}
	}
	if err := runPrivilegedCommand("systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd after uninstall: %w", err)
	}
	return nil
}

func removePath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || path == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove unsafe path %q", path)
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := runPrivilegedCommand("rm", "-rf", "--", path); err != nil {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func requireSystemdHost() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("systemctl not found; Swarm daemon lifecycle requires systemd. Install on a systemd Linux host or use install.sh on a supported host")
	}
	if _, err := os.Stat(filepath.Join(string(filepath.Separator), "run", "systemd", "system")); err != nil {
		return errors.New("systemd is not running as the system manager; cannot manage swarm.service")
	}
	return nil
}

func requireInstalledServiceUnit() error {
	if _, err := os.Stat(systemdSystemUnitPath()); err == nil {
		return nil
	}
	return fmt.Errorf("%s is not installed at %s; install Swarm with install.sh or run: swarm install --yes", InstalledServiceUnit, systemdSystemUnitPath())
}

func systemdIsEnabled(scope systemdServiceScope, unit string) (string, bool) {
	args, err := systemctlQueryArgs(scope, "is-enabled", unit)
	if err != nil {
		return "unknown", false
	}
	cmd := exec.Command(args[0], args[1:]...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if text != "" {
		return text, true
	}
	if err == nil {
		return "enabled", true
	}
	return "disabled", true
}
