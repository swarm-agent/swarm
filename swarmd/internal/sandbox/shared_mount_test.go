package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

// Purpose: host and concurrent lease writes require shared gofer caching on
// workspace bind mounts. runArgsImage is the launch authority; argument-level
// tests prove per-source hints and unchanged isolation without a host runtime.
// The runc case prevents leaking runtime-specific options to other backends.
func TestWorkspaceMountSharedAccess(t *testing.T) {
	for _, runtime := range []string{gvisorRuntime, "runc"} {
		t.Run(runtime, func(t *testing.T) {
			runner := readyRunner()
			runner.out["info --format"] = `{"runsc":{},"runc":{}}`
			m := newTestManager(t, ModeRequired, runner, Config{Runtime: runtime})
			root, linked := t.TempDir(), t.TempDir()
			args := m.RunArgs("fixture", root, "spec", []string{root, linked}, SecretSetup{})
			pairs := map[string][]string{}
			for i := 0; i+1 < len(args); i++ {
				if strings.HasPrefix(args[i], "--") {
					pairs[args[i]] = append(pairs[args[i]], args[i+1])
				}
			}
			if runtime == gvisorRuntime {
				if len(pairs["--annotation"]) != 6 {
					t.Fatalf("annotations: %v", pairs["--annotation"])
				}
				for i, path := range []string{root, linked} {
					for _, field := range []string{"source=" + path, "type=bind", "share=shared"} {
						want := fmt.Sprintf("dev.gvisor.spec.mount.workspace%d.%s", i, field)
						if !containsMountArg(pairs["--annotation"], want) {
							t.Fatalf("missing %s", want)
						}
					}
				}
			} else if len(pairs["--annotation"]) != 0 {
				t.Fatal("annotations leaked to runc")
			}
			for flag, value := range map[string]string{"--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--ipc": "private", "--runtime": runtime} {
				if !containsMountArg(pairs[flag], value) {
					t.Fatalf("lost hardening %s %s", flag, value)
				}
			}
			for _, path := range []string{root, linked} {
				if !containsMountArg(pairs["--mount"], "type=bind,src="+path+",dst="+path) {
					t.Fatal("workspace mount changed")
				}
			}
			if len(pairs["--privileged"]) != 0 {
				t.Fatal("privileged container")
			}
		})
	}
}

func containsMountArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
