package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Purpose: resolvePodmanEnvironment must recover a missing daemon session only
// from the effective user's private runtime directory and owned socket. This
// narrow resolver test prevents cross-user, symlink and missing-bus discovery
// without depending on the machine's login session or changing its environment.
func TestLocalPodmanEnvironmentDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		env              []string
		uid              int
		dirMode, busMode os.FileMode
		owner            int
		missing          bool
		wantDir, wantBus string
		wantErr          bool
	}{
		{name: "absent", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: os.ModeSocket | 0600, wantDir: "/run/user/1234", wantBus: "unix:path=/run/user/1234/bus"},
		{name: "empty", env: []string{"XDG_RUNTIME_DIR=", "DBUS_SESSION_BUS_ADDRESS="}, uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: os.ModeSocket, wantDir: "/run/user/1234", wantBus: "unix:path=/run/user/1234/bus"},
		{name: "explicit-runtime", env: []string{"XDG_RUNTIME_DIR=/private/user space"}, uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: os.ModeSocket, wantDir: "/private/user space", wantBus: "unix:path=/private/user%20space/bus"},
		{name: "explicit-bus", env: []string{"DBUS_SESSION_BUS_ADDRESS=unix:abstract=explicit"}, uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, wantDir: "/run/user/1234", wantBus: "unix:abstract=explicit"},
		{name: "both-explicit", env: []string{"XDG_RUNTIME_DIR=/explicit", "DBUS_SESSION_BUS_ADDRESS=unix:abstract=explicit"}, uid: 1234, missing: true, wantDir: "/explicit", wantBus: "unix:abstract=explicit"},
		{name: "root", uid: 0, wantErr: true},
		{name: "missing", uid: 1234, missing: true, wantErr: true},
		{name: "wrong-owner", uid: 1234, owner: 2345, dirMode: os.ModeDir | 0700, busMode: os.ModeSocket, wantErr: true},
		{name: "public-directory", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0755, busMode: os.ModeSocket, wantErr: true},
		{name: "directory-symlink", uid: 1234, owner: 1234, dirMode: os.ModeSymlink | 0700, busMode: os.ModeSocket, wantErr: true},
		{name: "bus-symlink", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: os.ModeSymlink, wantErr: true},
		{name: "bus-wrong-owner", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: os.ModeSocket, wantErr: true},
		{name: "bus-missing", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, wantErr: true},
		{name: "bus-file", uid: 1234, owner: 1234, dirMode: os.ModeDir | 0700, busMode: 0600, wantErr: true},
		{name: "relative-runtime", env: []string{"XDG_RUNTIME_DIR=relative"}, uid: 1234, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := append([]string{"KEEP=value"}, tc.env...)
			before := append([]string(nil), env...)
			var paths []string
			got, err := resolvePodmanEnvironment(env, tc.uid, func(path string) (os.FileMode, int, error) {
				paths = append(paths, path)
				if tc.missing {
					return 0, -1, os.ErrNotExist
				}
				if strings.HasSuffix(path, "/bus") {
					if tc.name == "bus-wrong-owner" {
						return tc.busMode, tc.owner + 1, nil
					}
					if tc.name == "bus-missing" {
						return 0, -1, os.ErrNotExist
					}
					return tc.busMode, tc.owner, nil
				}
				return tc.dirMode, tc.owner, nil
			})
			if !reflect.DeepEqual(env, before) {
				t.Fatal("mutated input environment")
			}
			if tc.wantErr {
				if err == nil || got != nil {
					t.Fatalf("unsafe discovery accepted: %v %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"KEEP=value", "XDG_RUNTIME_DIR=" + tc.wantDir, "DBUS_SESSION_BUS_ADDRESS=" + tc.wantBus}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for _, path := range paths {
				if path != tc.wantDir && path != tc.wantDir+"/bus" {
					t.Fatalf("inspected unrelated session: %s", path)
				}
			}
			if tc.name == "both-explicit" && len(paths) != 0 {
				t.Fatal("rediscovered explicit session")
			}
		})
	}
}

// Purpose: OSCommandRunner must apply per-process discovery consistently for
// probes, combined output and streamed lifecycle commands, while rejecting
// discovery failure before starting a child. A bounded real shell command is
// the narrowest layer proving os/exec environment propagation (not a benchmark).
func TestLocalPodmanRunnerEnvironment(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires sh")
	}
	t.Setenv("XDG_RUNTIME_DIR", "daemon-original")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := &OSCommandRunner{commandEnv: func() ([]string, error) {
		return []string{"XDG_RUNTIME_DIR=resolved", "DBUS_SESSION_BUS_ADDRESS=unix:path=resolved/bus"}, nil
	}}
	for _, method := range []string{"run", "combined", "io"} {
		t.Run(method, func(t *testing.T) {
			args := []string{"-c", `printf '%s|%s' "$XDG_RUNTIME_DIR" "$DBUS_SESSION_BUS_ADDRESS"`}
			var out []byte
			var err error
			switch method {
			case "run":
				out, err = r.Run(ctx, sh, args...)
			case "combined":
				out, err = r.RunCombined(ctx, sh, args...)
			case "io":
				var b strings.Builder
				err = r.RunWithIO(ctx, nil, &b, io.Discard, sh, args...)
				out = []byte(b.String())
			}
			if err != nil || string(out) != "resolved|unix:path=resolved/bus" {
				t.Fatalf("%q %v", out, err)
			}
		})
	}
	if os.Getenv("XDG_RUNTIME_DIR") != "daemon-original" {
		t.Fatal("changed daemon environment")
	}
	failure := errors.New("user bus unavailable")
	r.commandEnv = func() ([]string, error) { return nil, failure }
	if out, err := r.Run(ctx, sh, "-c", "echo started"); !errors.Is(err, failure) || len(out) != 0 {
		t.Fatalf("child started: %q %v", out, err)
	}
	if out, err := r.RunCombined(ctx, sh, "-c", "echo started"); !errors.Is(err, failure) || len(out) != 0 {
		t.Fatalf("child started: %q %v", out, err)
	}
	var b strings.Builder
	if err := r.RunWithIO(ctx, nil, &b, &b, sh, "-c", "echo started"); !errors.Is(err, failure) || b.Len() != 0 {
		t.Fatalf("child started: %q %v", b.String(), err)
	}
}

// Purpose: NewLocalPodmanProvider must own discovery without changing a shared
// Docker runner; constructor-level assertions isolate this provider boundary.
func TestLocalPodmanRunnerOwnership(t *testing.T) {
	r := &OSCommandRunner{MaxOutputBytes: 1024, WaitDelay: time.Second}
	docker := NewLocalDockerProvider(r)
	podman := NewLocalPodmanProvider(r)
	local := podman.runner.(*OSCommandRunner)
	if local == r || local.commandEnv == nil || r.commandEnv != nil || docker.runner != r || local.MaxOutputBytes != r.MaxOutputBytes || local.WaitDelay != r.WaitDelay {
		t.Fatal("Podman discovery changed shared runner or lost limits")
	}
	if NewLocalPodmanProvider(nil).runner.(*OSCommandRunner).commandEnv == nil {
		t.Fatal("default runner lacks discovery")
	}
}
