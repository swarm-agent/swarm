package localcontainers

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentRuntimeMountDoesNotUseHomeOrXDGSharedRuntimeFallbacks(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("SWARM_SHARED_RUNTIME_ROOT", "")
	t.Setenv("SWARM_ROOT", "")
	t.Setenv("SWARM_GO_ROOT", "")
	t.Setenv("STARTUP_CWD", "")
	t.Setenv("SWARM_WEB_DIR", "")

	mount := CurrentRuntimeMount()
	if mount == nil {
		return
	}
	for _, path := range []string{mount.BinDir, mount.ToolBinDir, mount.WebDistDir, mount.FFFLibPath} {
		if strings.HasPrefix(path, home) {
			t.Fatalf("runtime mount path %q is under HOME", path)
		}
	}
}
