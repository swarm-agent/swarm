package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: Git and Bash are mandatory runtime dependencies and the public
// installer must provision only the commands that are absent before installing
// or starting Swarm. The fake package manager proves detection, the Ubuntu
// package mapping, post-install verification, and the no-op path without
// mutating the test host.
func TestInstallerProvisionsMissingRuntimePrerequisitesAndSkipsPresentOnes(t *testing.T) {
	tests := []struct {
		name       string
		missing    []string
		wantOutput []string
		wantCalls  []string
		noCalls    bool
	}{
		{
			name:       "git missing",
			missing:    []string{"git"},
			wantOutput: []string{"Installing missing mandatory Swarm runtime prerequisites: git", "Swarm install plan", "downloading release and checksum..."},
			wantCalls:  []string{"update", "install -y --no-install-recommends git"},
		},
		{
			name:       "git and bash missing",
			missing:    []string{"git", "bash"},
			wantOutput: []string{"Installing missing mandatory Swarm runtime prerequisites: git, bash", "Swarm install plan", "downloading release and checksum..."},
			wantCalls:  []string{"update", "install -y --no-install-recommends git bash"},
		},
		{
			name:       "already installed",
			wantOutput: []string{"Swarm install plan", "downloading release and checksum..."},
			noCalls:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sh", "cat", "uname", "tar", "sed", "grep", "awk", "head", "dirname", "pwd", "readlink", "mkdir", "chmod", "sleep", "mktemp", "id", "install", "sha256sum", "ln", "rm"} {
				linkHostCommand(t, bin, name)
			}
			for _, name := range []string{"git", "bash"} {
				if !contains(tc.missing, name) {
					linkHostCommand(t, bin, name)
				}
			}
			calls := filepath.Join(tmp, "package-manager.calls")
			writeExecutable(t, filepath.Join(bin, "apt-get"), `#!/bin/sh
printf '%s\n' "$*" >> "$SWARM_TEST_PACKAGE_CALLS"
if [ "${1:-}" = update ]; then exit 0; fi
shift
for arg in "$@"; do
  case "$arg" in
    git) ln -sf "$SWARM_TEST_GIT_SOURCE" "$SWARM_TEST_BIN/git" ;;
    bash) ln -sf "$SWARM_TEST_BASH_SOURCE" "$SWARM_TEST_BIN/bash" ;;
  esac
done
`)
			writeExecutable(t, filepath.Join(bin, "env"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in *=*) export "$1"; shift ;; *) break ;; esac
done
exec "$@"
`)
			writeExecutable(t, filepath.Join(bin, "sudo"), `#!/bin/sh
exec "$@"
`)
			writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
case "$*" in
  *releases/latest*) printf '%s\n' '  "tag_name": "v1.2.3"' ;;
  *) exit 22 ;;
esac
`)
			gitSource, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bashSource, err := exec.LookPath("bash")
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service")
			cmd.Env = []string{"PATH=" + bin, "SWARM_TEST_PACKAGE_CALLS=" + calls, "SWARM_TEST_BIN=" + bin, "SWARM_TEST_GIT_SOURCE=" + gitSource, "SWARM_TEST_BASH_SOURCE=" + bashSource}
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("install.sh unexpectedly passed the intentionally failed release download")
			}
			text := string(output)
			for _, want := range tc.wantOutput {
				if !strings.Contains(text, want) {
					t.Errorf("output %q does not contain %q", text, want)
				}
			}
			callBytes, readErr := os.ReadFile(calls)
			if tc.noCalls {
				if readErr == nil && strings.TrimSpace(string(callBytes)) != "" {
					t.Fatalf("package manager called for present prerequisites: %s", callBytes)
				}
				return
			}
			if readErr != nil {
				t.Fatalf("read package-manager calls: %v", readErr)
			}
			callText := string(callBytes)
			for _, want := range tc.wantCalls {
				if !strings.Contains(callText, want) {
					t.Errorf("package-manager calls %q do not contain %q", callText, want)
				}
			}
		})
	}
}

// Requirement: prerequisite provisioning is a pre-mutation gate. A package-manager
// failure or a successful command that does not provide Git must fail closed before
// any release download or Swarm-owned path mutation. This shell-level test is the
// narrowest layer that observes ordering and the installer diagnostic together.
func TestInstallerFailsClosedWhenRuntimePrerequisitesCannotBeProvisioned(t *testing.T) {
	tests := []struct {
		name       string
		aptBody    string
		wantOutput string
	}{
		{
			name:       "package manager fails",
			aptBody:    "exit 42\n",
			wantOutput: "Failed to install mandatory Swarm runtime prerequisites: git.",
		},
		{
			name:       "package manager does not provide git",
			aptBody:    "exit 0\n",
			wantOutput: "Runtime prerequisite provisioning did not provide required command: git",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sh", "bash", "cat", "uname", "tar", "sed", "grep", "awk", "head", "dirname", "pwd", "readlink", "mkdir", "chmod", "sleep", "mktemp", "id", "install", "sha256sum", "ln", "rm"} {
				linkHostCommand(t, bin, name)
			}
			writeExecutable(t, filepath.Join(bin, "apt-get"), "#!/bin/sh\n"+tc.aptBody)
			writeExecutable(t, filepath.Join(bin, "env"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in *=*) export "$1"; shift ;; *) break ;; esac
done
exec "$@"
`)
			writeExecutable(t, filepath.Join(bin, "sudo"), "#!/bin/sh\nexec \"$@\"\n")
			writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
case "$*" in
  *releases/latest*) printf '%s\n' '  "tag_name": "v1.2.3"' ;;
  *) printf '%s\n' 'release download must not run' >&2; exit 91 ;;
esac
`)

			cmd := exec.Command(filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service")
			cmd.Env = []string{"PATH=" + bin}
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("install.sh unexpectedly continued without its mandatory Git prerequisite")
			}
			text := string(output)
			if !strings.Contains(text, tc.wantOutput) {
				t.Fatalf("output %q does not contain %q", text, tc.wantOutput)
			}
			if !strings.Contains(text, "No Swarm files, service definitions, or state paths were changed.") || !strings.Contains(text, "Swarm installation has not started.") {
				t.Fatalf("output %q does not explain the pre-mutation failure", text)
			}
			if strings.Contains(text, "release download must not run") || strings.Contains(text, "downloading release and checksum...") || strings.Contains(text, "Swarm install plan") {
				t.Fatalf("installer reached planning or release download after prerequisite failure: %q", text)
			}
		})
	}
}

// Requirement: when Git is absent, an unsupported host cannot satisfy Swarm's
// mandatory runtime contract. The installer must explain the prerequisite and
// stop before release resolution or filesystem mutation rather than treating a
// Git-free runtime as supported.
func TestInstallerFailsClosedWithoutSupportedPrerequisiteProvisioner(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "bash", "cat", "uname", "tar", "sed", "grep", "awk", "head", "dirname", "pwd", "readlink", "mkdir", "chmod", "sleep", "mktemp", "id", "install", "sha256sum", "ln", "rm"} {
		linkHostCommand(t, bin, name)
	}
	writeExecutable(t, filepath.Join(bin, "env"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in *=*) export "$1"; shift ;; *) break ;; esac
done
exec "$@"
`)
	writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
case "$*" in
  *releases/latest*) printf '%s\n' '  "tag_name": "v1.2.3"' ;;
  *) printf '%s\n' 'release download must not run' >&2; exit 91 ;;
esac
`)

	cmd := exec.Command(filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service")
	cmd.Env = []string{"PATH=" + bin}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh unexpectedly accepted a Git-free runtime without a supported package manager")
	}
	text := string(output)
	if !strings.Contains(text, "Swarm requires git at runtime, but no supported package manager was found.") {
		t.Fatalf("output %q does not explain the mandatory Git prerequisite", text)
	}
	if !strings.Contains(text, "Install the missing commands, then rerun install.sh.") || !strings.Contains(text, "No Swarm files, service definitions, or state paths were changed.") || !strings.Contains(text, "Swarm installation has not started.") {
		t.Fatalf("output %q does not provide fail-closed remediation", text)
	}
	if strings.Contains(text, "release download must not run") || strings.Contains(text, "downloading release and checksum...") || strings.Contains(text, "Swarm install plan") {
		t.Fatalf("installer reached planning or release download without prerequisite authority: %q", text)
	}
}

func linkHostCommand(t *testing.T, bin, name string) {
	t.Helper()
	commandPath, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(commandPath, filepath.Join(bin, name)); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

