package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareUpdateHelperLaunchUsesSystemdRunScopeForSystemService(t *testing.T) {
	withUpdateHelperLaunchTestHooks(t)
	t.Setenv(updateHelperUnitEnv, "swarm-update-test")
	t.Setenv("USER", "swarm")
	workspaceDir := filepath.Join(string(os.PathSeparator), "srv", "swarm", "swarm-go")

	launch, err := prepareUpdateHelperLaunch(updateHelperLaunchConfig{
		SwarmPath:    "/usr/local/share/swarm/libexec/swarm",
		Args:         []string{"main", "update", "dev"},
		Env:          []string{"SWARM_UPDATE_JOB_ID=job-1", "SWARM_UPDATE_JOB_KIND=dev", "SWARM_ROOT=" + workspaceDir},
		Dir:          workspaceDir,
		LogPath:      "/var/lib/swarmd/update/helpers/job-1.log",
		SystemdScope: "system",
		SystemdUnit:  "swarm.service",
	})
	if err != nil {
		t.Fatalf("prepareUpdateHelperLaunch error = %v", err)
	}

	if launch.CommandPath != "/usr/bin/sudo" {
		t.Fatalf("command path = %q, want sudo", launch.CommandPath)
	}
	assertStringInSlice(t, launch.Args, "-n")
	assertStringInSlice(t, launch.Args, "/bin/systemd-run")
	assertStringNotInSlice(t, launch.Args, "--scope")
	assertStringInSlice(t, launch.Args, "--quiet")
	assertStringInSlice(t, launch.Args, "--collect")
	assertStringInSlice(t, launch.Args, "--property=KillMode=process")
	assertStringInSlice(t, launch.Args, "--property=SendSIGHUP=no")
	assertStringInSlice(t, launch.Args, "--uid=swarm")
	assertStringNotInSlice(t, launch.Args, "--user")
	assertStringInSlice(t, launch.Args, "--unit=swarm-update-test")
	assertStringInSlice(t, launch.Args, "--working-directory="+workspaceDir)
	assertStringInSlice(t, launch.Args, "--setenv=SWARM_ROOT="+workspaceDir)
	assertStringInSlice(t, launch.Args, "--setenv=SWARM_UPDATE_JOB_ID=job-1")
	assertStringInSlice(t, launch.Args, "--setenv=SWARM_UPDATE_JOB_KIND=dev")
	assertStringInSlice(t, launch.Args, "/usr/local/share/swarm/libexec/swarm")
	assertStringInSlice(t, launch.Args, "main")
	assertStringInSlice(t, launch.Args, "update")
	assertStringInSlice(t, launch.Args, "dev")
	if launch.Dir != "" {
		t.Fatalf("systemd-run launch dir = %q, want empty because working-directory is passed to systemd", launch.Dir)
	}
	if len(launch.Env) == 0 {
		t.Fatal("systemd-run launch env is empty, want current environment")
	}
}

func TestPrepareUpdateHelperLaunchUsesUserScopeForUserService(t *testing.T) {
	withUpdateHelperLaunchTestHooks(t)
	t.Setenv(updateHelperUnitEnv, "swarm-update-user")

	launch, err := prepareUpdateHelperLaunch(updateHelperLaunchConfig{
		SwarmPath:    "/opt/swarm/swarm",
		Args:         []string{"main", "update", "apply"},
		Env:          []string{"SWARM_UPDATE_JOB_ID=job-2"},
		SystemdScope: "user",
		SystemdUnit:  "swarm.service",
	})
	if err != nil {
		t.Fatalf("prepareUpdateHelperLaunch error = %v", err)
	}

	if launch.CommandPath != "/bin/systemd-run" {
		t.Fatalf("command path = %q, want systemd-run", launch.CommandPath)
	}
	assertStringInSlice(t, launch.Args, "--user")
	assertStringNotInSliceWithPrefix(t, launch.Args, "--uid=")
	assertStringInSlice(t, launch.Args, "--unit=swarm-update-user")
}

func TestPrepareUpdateHelperLaunchFallsBackWithoutSystemdContextOrBinary(t *testing.T) {
	withUpdateHelperLaunchTestHooks(t)
	execLookPathForUpdate = func(name string) (string, error) {
		if name == "systemd-run" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + name, nil
	}

	cfg := updateHelperLaunchConfig{
		SwarmPath:    "/opt/swarm/swarm",
		Args:         []string{"main", "update", "dev"},
		Env:          []string{"SWARM_UPDATE_JOB_ID=job-3"},
		Dir:          "/repo",
		SystemdScope: "system",
		SystemdUnit:  "swarm.service",
	}
	_, err := prepareUpdateHelperLaunch(cfg)
	if err == nil {
		t.Fatal("prepareUpdateHelperLaunch error = nil, want missing systemd-run error")
	}
	if !strings.Contains(err.Error(), "systemd-run not found") {
		t.Fatalf("missing systemd-run error = %q", err.Error())
	}

	execLookPathForUpdate = func(name string) (string, error) {
		if name == "systemd-run" {
			return "/bin/systemd-run", nil
		}
		return "/usr/bin/" + name, nil
	}
	cfg.SystemdUnit = ""
	launch, err := prepareUpdateHelperLaunch(cfg)
	if err != nil {
		t.Fatalf("prepareUpdateHelperLaunch without systemd unit error = %v", err)
	}
	if launch.CommandPath != cfg.SwarmPath {
		t.Fatalf("non-systemd command path = %q, want %q", launch.CommandPath, cfg.SwarmPath)
	}
}

func TestPrepareUpdateHelperLaunchCanBeDisabled(t *testing.T) {
	withUpdateHelperLaunchTestHooks(t)
	t.Setenv(updateHelperScopeEnv, "0")

	launch, err := prepareUpdateHelperLaunch(updateHelperLaunchConfig{
		SwarmPath:    "/opt/swarm/swarm",
		Args:         []string{"main", "update", "dev"},
		SystemdScope: "system",
		SystemdUnit:  "swarm.service",
	})
	if err != nil {
		t.Fatalf("prepareUpdateHelperLaunch error = %v", err)
	}
	if launch.CommandPath != "/opt/swarm/swarm" {
		t.Fatalf("disabled command path = %q, want direct swarm", launch.CommandPath)
	}
}

func withUpdateHelperLaunchTestHooks(t *testing.T) {
	t.Helper()
	origLookPath := execLookPathForUpdate
	origGeteuid := osGeteuidForUpdate
	execLookPathForUpdate = func(name string) (string, error) {
		switch name {
		case "systemd-run":
			return "/bin/systemd-run", nil
		case "sudo":
			return "/usr/bin/sudo", nil
		default:
			return "", errors.New("unexpected lookup " + name)
		}
	}
	osGeteuidForUpdate = func() int { return 1000 }
	t.Cleanup(func() {
		execLookPathForUpdate = origLookPath
		osGeteuidForUpdate = origGeteuid
		_ = os.Unsetenv(updateHelperUnitEnv)
		_ = os.Unsetenv(updateHelperScopeEnv)
	})
}

func assertStringInSlice(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %#v", want, values)
}

func assertStringNotInSlice(t *testing.T, values []string, unwanted string) {
	t.Helper()
	for _, value := range values {
		if value == unwanted {
			t.Fatalf("unexpected %q in %#v", unwanted, values)
		}
	}
}

func assertStringNotInSliceWithPrefix(t *testing.T, values []string, prefix string) {
	t.Helper()
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			t.Fatalf("unexpected %q with prefix %q in %#v", value, prefix, values)
		}
	}
}
