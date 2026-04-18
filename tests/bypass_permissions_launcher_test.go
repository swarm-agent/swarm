package codetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibDevPreservesExplicitBypassPermissionsOverride(t *testing.T) {
	repoRoot := repoRoot(t)
	tempRoot := t.TempDir()

	copyFile(t,
		filepath.Join(repoRoot, "swarmd", "scripts", "lib-dev.sh"),
		filepath.Join(tempRoot, "swarmd", "scripts", "lib-dev.sh"),
	)

	writeFile(t, filepath.Join(tempRoot, "scripts", "lib-lane.sh"), `#!/usr/bin/env bash
set -euo pipefail

swarm_lane_default() {
  printf "main\n"
}

swarm_lane_export_profile() {
  local lane="${1:-}"
  local repo_root="${2:-}"
  export SWARM_LANE="${lane}"
  export SWARM_BIN_DIR="${repo_root}/.bin/${lane}"
  export SWARMD_LISTEN="127.0.0.1:7781"
  export SWARMD_URL="http://127.0.0.1:7781"
  export SWARM_LANE_PORT="7781"
  export STATE_ROOT="${repo_root}/state/${lane}"
  export DATA_DIR="${repo_root}/data/${lane}"
  export DB_PATH="${repo_root}/data/${lane}/swarmd.pebble"
  export LOCK_PATH="${repo_root}/state/${lane}/swarmd.lock"
  export PID_FILE="${repo_root}/state/${lane}/swarmd.pid"
  export LOG_FILE="${repo_root}/state/${lane}/swarmd.log"
  export SWARM_PORTS_DIR="${repo_root}/ports"
  export SWARM_PORT_RECORD="${repo_root}/ports/swarmd-${lane}.env"
  export SWARM_BYPASS_PERMISSIONS="false"
}
`)

	cmd := exec.Command("bash", "-lc", `source "./swarmd/scripts/lib-dev.sh"; printf "%s" "$SWARM_BYPASS_PERMISSIONS"`)
	cmd.Dir = tempRoot
	cmd.Env = append(os.Environ(), "SWARM_BYPASS_PERMISSIONS=true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source lib-dev.sh: %v\n%s", err, output)
	}

	if got := strings.TrimSpace(string(output)); got != "true" {
		t.Fatalf("expected explicit bypass override to survive lib-dev bootstrap, got %q", got)
	}
}

func TestLibDevFallsBackToLaneConfigBypassPermissions(t *testing.T) {
	repoRoot := repoRoot(t)
	tempRoot := t.TempDir()

	copyFile(t,
		filepath.Join(repoRoot, "swarmd", "scripts", "lib-dev.sh"),
		filepath.Join(tempRoot, "swarmd", "scripts", "lib-dev.sh"),
	)

	writeFile(t, filepath.Join(tempRoot, "scripts", "lib-lane.sh"), `#!/usr/bin/env bash
set -euo pipefail

swarm_lane_default() {
  printf "main\n"
}

swarm_lane_export_profile() {
  local lane="${1:-}"
  local repo_root="${2:-}"
  export SWARM_LANE="${lane}"
  export SWARM_BIN_DIR="${repo_root}/.bin/${lane}"
  export SWARMD_LISTEN="127.0.0.1:7781"
  export SWARMD_URL="http://127.0.0.1:7781"
  export SWARM_LANE_PORT="7781"
  export STATE_ROOT="${repo_root}/state/${lane}"
  export DATA_DIR="${repo_root}/data/${lane}"
  export DB_PATH="${repo_root}/data/${lane}/swarmd.pebble"
  export LOCK_PATH="${repo_root}/state/${lane}/swarmd.lock"
  export PID_FILE="${repo_root}/state/${lane}/swarmd.pid"
  export LOG_FILE="${repo_root}/state/${lane}/swarmd.log"
  export SWARM_PORTS_DIR="${repo_root}/ports"
  export SWARM_PORT_RECORD="${repo_root}/ports/swarmd-${lane}.env"
  export SWARM_BYPASS_PERMISSIONS="false"
}
`)

	cmd := exec.Command("bash", "-lc", `source "./swarmd/scripts/lib-dev.sh"; printf "%s" "$SWARM_BYPASS_PERMISSIONS"`)
	cmd.Dir = tempRoot
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source lib-dev.sh: %v\n%s", err, output)
	}

	if got := strings.TrimSpace(string(output)); got != "false" {
		t.Fatalf("expected lane-config bypass value when no explicit override is set, got %q", got)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, ".."))
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	writeFile(t, dst, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
