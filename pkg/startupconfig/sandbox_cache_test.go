package startupconfig

import "testing"

// Purpose: parseEntries must reject the removed cache setting so startup config
// cannot silently become a second authority beside the durable box limits.
func TestSandboxCacheConfig(t *testing.T) {
	if _, _, err := parseEntries("sandbox_project_cache_bytes = 123456", Default("")); err == nil {
		t.Fatal("accepted removed cache setting")
	}
}
