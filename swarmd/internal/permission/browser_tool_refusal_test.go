package permission

import (
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Without a session store, the refusal reads as no lease: manage_browser is
// refused with the way to ask, and the request itself goes through.
func TestBrowserToolRefusalWithoutLease(t *testing.T) {
	var s *Service
	if reason := s.browserToolRefusal("s1", pebblestore.BrowserToolManage, 1); !strings.Contains(reason, "request_browser_access") {
		t.Fatalf("manage_browser without a lease: %q", reason)
	}
	if reason := s.browserToolRefusal("s1", pebblestore.BrowserToolRequestAccess, 1); reason != "" {
		t.Fatalf("the request without a lease was refused: %q", reason)
	}
}
