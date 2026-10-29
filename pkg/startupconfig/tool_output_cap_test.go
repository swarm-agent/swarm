package startupconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: tool_output_cap_bytes is the one persisted setting for the tool
// output cap: it round-trips through Format/parse, rejects negative or
// duplicate values, and a file without it gains it exactly once on load
// (default 0) without changing on a second load. Threat: a typo silently
// disables the cap, or migration writes a duplicate key that blocks the next
// startup. Format, parseEntries and Load own the persisted contract; a real
// temporary config load is the narrowest layer that proves the migration.
func TestToolOutputCapConfig(t *testing.T) {
	t.Setenv(childStartupConfigEnv, "")
	cfg := Default("")
	cfg.ToolOutputCapBytes = 65536
	parsed, _, err := parseEntries(Format(cfg), Default(""))
	if err != nil || parsed.ToolOutputCapBytes != 65536 {
		t.Fatalf("roundtrip: %d %v", parsed.ToolOutputCapBytes, err)
	}
	for _, s := range []string{"tool_output_cap_bytes = -1", "tool_output_cap_bytes = big", "tool_output_cap_bytes = 1\ntool_output_cap_bytes = 2"} {
		if _, _, err := parseEntries(s, Default("")); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	path := filepath.Join(t.TempDir(), "swarm.conf")
	if err := os.WriteFile(path, []byte("peer_transport_port = 7379\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var first []byte
	for load := 0; load < 2; load++ {
		loaded, err := Load(path)
		if err != nil || loaded.ToolOutputCapBytes != 0 {
			t.Fatalf("load %d: %d %v", load+1, loaded.ToolOutputCapBytes, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), "tool_output_cap_bytes ="); n != 1 {
			t.Fatalf("tool_output_cap_bytes occurs %d times after load %d", n, load+1)
		}
		if load == 0 {
			first = data
		} else if string(data) != string(first) {
			t.Fatal("second load changed the migrated file")
		}
	}
}
