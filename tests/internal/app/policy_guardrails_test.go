package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runLocalCommand(t *testing.T, name string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", string(out), err
	}
	return string(out), "", nil
}

func TestCheckPolicyGuardrails_PassesForCompliantRuntime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal", "app"), 0o755); err != nil {
		t.Fatalf("mkdir internal/app: %v", err)
	}

	script := `#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"
has_failures=0
silent_fallback_hits="$({
  rg -n --glob '!.git/**' --glob '*.go' --glob '*.sh' --glob '!**/*_test.go' --glob '!scripts/check-policy-guardrails.sh' '(?i)silent fallback|silently fallback|fallback silently|best effort|ignore error|swallow error|discard error|noop on error'
} || true)"
if [[ -n "${silent_fallback_hits}" ]]; then
  has_failures=1
fi
missing_commands=()
if ! rg -q 'Command: "/quit"' internal/app/app.go; then
  missing_commands+=("/quit command suggestion")
fi
if ! rg -q 'case "quit", "exit":' internal/app/app.go; then
  missing_commands+=("/quit command handler")
fi
if (( ${#missing_commands[@]} > 0 )); then
  has_failures=1
fi
if [[ "${has_failures}" -ne 0 ]]; then
  exit 1
fi
echo "[policy-check] PASS"
`
	if err := os.WriteFile(filepath.Join(root, "scripts", "check-policy-guardrails.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write guardrail script: %v", err)
	}

	appSource := `package app
var _ = []struct{ Command string }{
	{Command: "/quit"},
}
func f(cmd string) {
	switch cmd {
	case "quit", "exit":
	}
}
`
	if err := os.WriteFile(filepath.Join(root, "internal", "app", "app.go"), []byte(appSource), 0o644); err != nil {
		t.Fatalf("write app.go: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	runOut, runErrOut, runErr := runLocalCommand(t, "bash", "scripts/check-policy-guardrails.sh")
	if runErr != nil {
		t.Fatalf("check-policy-guardrails failed: %v\nstdout=%s\nstderr=%s", runErr, runOut, runErrOut)
	}
	if !strings.Contains(runOut, "[policy-check] PASS") {
		t.Fatalf("stdout missing PASS marker: %q", runOut)
	}
}
