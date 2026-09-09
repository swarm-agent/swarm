package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: install.sh must use visible, bounded downloads and stop before
// extraction/provisioning on transfer or integrity failure. Running the complete
// shell entrypoint with fake transport and mutation sentinels is the narrowest
// hermetic layer proving ordering, curl arguments, diagnostics and postconditions.
func TestInstallerDownloadContract(t *testing.T) {
	for _, scenario := range []string{"archive-timeout", "checksum-http", "corrupt", "success"} {
		t.Run(scenario, func(t *testing.T) {
			tmp := t.TempDir()
			bin := filepath.Join(tmp, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"sh", "bash", "git", "cat", "uname", "sed", "grep", "awk", "head", "dirname", "pwd", "readlink", "mkdir", "chmod", "sleep", "mktemp", "id", "install", "sha256sum", "env", "rm"} {
				linkHostCommand(t, bin, name)
			}
			writeExecutable(t, filepath.Join(bin, "sudo"), "#!/bin/sh\necho unexpected-provisioning >&2\nexit 99\n")
			writeExecutable(t, filepath.Join(bin, "tar"), "#!/bin/sh\necho extraction-reached >&2\nexit 90\n")
			writeExecutable(t, filepath.Join(bin, "curl"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALLS"
[ "$1" = -q ] || exit 91
case "$*" in *--silent*|*--retry*|*--insecure*) exit 92 ;; esac
case "$*" in *'--connect-timeout 10 --speed-limit 1024 --speed-time 20 --max-time '*) ;; *) exit 93 ;; esac
out=''
while [ "$#" -gt 0 ]; do
 case "$1" in --output) out="$2"; shift ;; esac
 url="$1"
 shift
done
case "$url" in
 *.sha256)
  [ "$SCENARIO" != checksum-http ] || exit 22
  if [ "$SCENARIO" = corrupt ]; then
   printf '%064d  swarm-v1.2.3-linux-amd64.tar.gz\n' 0 > "$out"
  else
   (cd "$(dirname "$out")" && sha256sum swarm-v1.2.3-linux-amd64.tar.gz) > "$out"
  fi ;;
 *) [ "$SCENARIO" != archive-timeout ] || exit 28
    printf 'test archive bytes' > "$out" ;;
esac
`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(bin, "sh"), "../../install.sh", "--yes", "--no-service", "--version", "v1.2.3")
			calls := filepath.Join(tmp, "calls")
			cmd.Env = []string{"PATH=" + bin, "TMPDIR=" + tmp, "CALLS=" + calls, "SCENARIO=" + scenario}
			output, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("expected bounded fixture stop: %v, %s", err, output)
			}
			text := string(output)
			if strings.Contains(text, "unexpected-provisioning") {
				t.Fatal(text)
			}
			want := map[string]string{"archive-timeout": "Failed to download release archive (curl exit 28)", "checksum-http": "Failed to download release checksum (curl exit 22)", "corrupt": "FAILED", "success": "extraction-reached"}[scenario]
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
			if scenario != "success" && strings.Contains(text, "extraction-reached") {
				t.Fatal(text)
			}
			if scenario == "success" && !strings.Contains(text, "swarm-v1.2.3-linux-amd64.tar.gz: OK") {
				t.Fatal(text)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			count := 2
			if scenario == "archive-timeout" {
				count = 1
			}
			if len(lines) != count {
				t.Fatalf("unexpected retries or continuation: %s", data)
			}
			if !strings.Contains(lines[0], "--max-time 600") {
				t.Fatal(string(data))
			}
			if count == 2 && (!strings.Contains(lines[1], "--max-time 30") || !strings.Contains(text, "downloading release checksum")) {
				t.Fatal(text, string(data))
			}
			leftovers, err := filepath.Glob(filepath.Join(tmp, "tmp.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("download scratch not cleaned: %v %v", leftovers, err)
			}
		})
	}
}
