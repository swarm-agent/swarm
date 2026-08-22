package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevealLocalPathUsesConfirmedFileManagerFolderCall(t *testing.T) {
	target := t.TempDir()
	var executable string
	var arguments []string
	method, err := revealLocalPathWithDependencies(target, revealLocalPathDependencies{
		lookPath: func(name string) (string, error) {
			if name == "dbus-send" {
				return "/usr/bin/dbus-send", nil
			}
			return "", os.ErrNotExist
		},
		run: func(name string, args, _ []string) error {
			executable = name
			arguments = append([]string(nil), args...)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "freedesktop-file-manager-show-folders" || executable != "/usr/bin/dbus-send" {
		t.Fatalf("method=%q executable=%q", method, executable)
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "--print-reply") || !strings.Contains(joined, "org.freedesktop.FileManager1.ShowFolders") || !strings.Contains(joined, "file://") {
		t.Fatalf("arguments = %q", arguments)
	}
}

func TestRevealLocalPathFallsBackAfterFileManagerFailure(t *testing.T) {
	target := t.TempDir()
	var calls []string
	method, err := revealLocalPathWithDependencies(target, revealLocalPathDependencies{
		lookPath: func(name string) (string, error) {
			if name == "dbus-send" || name == "gio" {
				return "/usr/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
		run: func(name string, args, _ []string) error {
			calls = append(calls, filepath.Base(name)+" "+strings.Join(args, " "))
			if filepath.Base(name) == "dbus-send" {
				return errors.New("session bus unavailable")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != "gio" || len(calls) != 2 || !strings.HasPrefix(calls[1], "gio open ") {
		t.Fatalf("method=%q calls=%q", method, calls)
	}
}

func TestRevealLocalPathReturnsFailureWhenNoOpenerSucceeds(t *testing.T) {
	target := t.TempDir()
	_, err := revealLocalPathWithDependencies(target, revealLocalPathDependencies{
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		run:      func(string, []string, []string) error { return errors.New("not opened") },
	})
	if err == nil || !strings.Contains(err.Error(), "native file manager did not open") {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalDesktopSessionEnvironmentPreservesProcessEnvironment(t *testing.T) {
	key := "SWARM_REVEAL_ENV_TEST"
	t.Setenv(key, "preserved")
	env := localDesktopSessionEnvironment()
	if !containsEnvironmentEntry(env, key+"=preserved") {
		t.Fatalf("environment entry %q was not preserved", key)
	}
}

func containsEnvironmentEntry(env []string, expected string) bool {
	for _, entry := range env {
		if entry == expected {
			return true
		}
	}
	return false
}
