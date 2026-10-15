package pebblestore

import (
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: GetLimits/SetLimits must retain bounded helper policy across restart,
// initialize legacy records safely, and reject invalid edits without mutation.
// Real Pebble is the narrowest layer proving the canonical policy's durability;
// this does not prove helper admission or execution enforcement.
func TestHelperLimitsDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Older records omit helper fields, which must inherit nonzero defaults.
	if err := db.PutJSON("limits/"+keyPart("account"), map[string]any{"revision": 2, "worker_job_concurrency": 100, "delegation_waves": 5, "active_children": 5, "swarm_children": 5}); err != nil {
		t.Fatal(err)
	}
	l, err := db.GetLimits("account")
	if err != nil {
		t.Fatal(err)
	}
	if l.HelperConcurrency != 4 || l.HelpersPerJob != 20 || l.HelperTimeoutSeconds != 300 || l.HelperTokens != 32000 {
		t.Fatalf("unsafe defaults: %+v", l)
	}
	for _, tc := range []struct {
		name   string
		change func(*Limits)
	}{
		{"helper_concurrency", func(l *Limits) { l.HelperConcurrency = 0 }},
		{"helpers_per_job", func(l *Limits) { l.HelpersPerJob = 10001 }},
		{"helper_timeout_seconds", func(l *Limits) { l.HelperTimeoutSeconds = 0 }},
		{"helper_tokens", func(l *Limits) { l.HelperTokens = -1 }},
	} {
		bad := l
		tc.change(&bad)
		if _, err := db.SetLimits("account", l.Revision, bad); err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Fatalf("%s refusal: %v", tc.name, err)
		}
		got, err := db.GetLimits("account")
		if err != nil || got != l {
			t.Fatalf("rejected edit mutated policy: %+v %v", got, err)
		}
	}
	l.HelperConcurrency = 5
	l.HelpersPerJob = 21
	l, err = db.SetLimits("account", l.Revision, l)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.GetLimits("other")
	if err != nil || other != DefaultLimits() {
		t.Fatalf("cross-account policy leak: %+v %v", other, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.GetLimits("account")
	if err != nil || got != l {
		t.Fatalf("restart: %+v %v", got, err)
	}
}
