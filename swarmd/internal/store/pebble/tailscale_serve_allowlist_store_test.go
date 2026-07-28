package pebblestore

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeTailscaleServeOrigin(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"https://desk.tailnet.ts.net":  "https://desk.tailnet.ts.net",
		" HTTPS://DESK.TAILNET.TS.NET/ ": "https://desk.tailnet.ts.net",
	}
	for input, want := range valid {
		got, err := NormalizeTailscaleServeOrigin(input)
		if err != nil {
			t.Errorf("NormalizeTailscaleServeOrigin(%q) error = %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeTailscaleServeOrigin(%q) = %q, want %q", input, got, want)
		}
	}

	invalid := []string{
		"",
		"desk.tailnet.ts.net",
		"http://desk.tailnet.ts.net",
		"https://ts.net",
		"https://desk.example.com",
		"https://desk.tailnet.ts.net:443",
		"https://user@desk.tailnet.ts.net",
		"https://desk.tailnet.ts.net/admin",
		"https://desk.tailnet.ts.net?mode=desktop",
		"https://desk.tailnet.ts.net#desktop",
		"https://-desk.tailnet.ts.net",
		"https://desk_.tailnet.ts.net",
	}
	for _, input := range invalid {
		if got, err := NormalizeTailscaleServeOrigin(input); err == nil {
			t.Errorf("NormalizeTailscaleServeOrigin(%q) = %q, want error", input, got)
		}
	}
}

func TestTailscaleServeAllowlistStorePersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tailscale-allowlist.pebble")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	allowlist := NewTailscaleServeAllowlistStore(store)
	first, changed, err := allowlist.Add("https://zebra.tailnet.ts.net/")
	if err != nil {
		t.Fatalf("Add(first) error = %v", err)
	}
	if !changed || first.Revision != 1 || first.CreatedAt == 0 || first.UpdatedAt == 0 {
		t.Fatalf("Add(first) record = %+v, changed = %v", first, changed)
	}
	second, changed, err := allowlist.Add("https://ALPHA.tailnet.ts.net")
	if err != nil {
		t.Fatalf("Add(second) error = %v", err)
	}
	if !changed || second.Revision != 2 {
		t.Fatalf("Add(second) revision = %d, changed = %v, want 2, true", second.Revision, changed)
	}
	wantOrigins := []string{"https://alpha.tailnet.ts.net", "https://zebra.tailnet.ts.net"}
	if !reflect.DeepEqual(second.Origins, wantOrigins) {
		t.Fatalf("Add(second) origins = %#v, want %#v", second.Origins, wantOrigins)
	}
	unchanged, changed, err := allowlist.Add("https://alpha.tailnet.ts.net/")
	if err != nil {
		t.Fatalf("Add(duplicate) error = %v", err)
	}
	if changed || unchanged.Revision != second.Revision {
		t.Fatalf("Add(duplicate) revision = %d, changed = %v, want %d, false", unchanged.Revision, changed, second.Revision)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store, err = Open(dbPath)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	defer func() { _ = store.Close() }()
	allowlist = NewTailscaleServeAllowlistStore(store)

	persisted, ok, err := allowlist.Get()
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if !reflect.DeepEqual(persisted, unchanged) {
		t.Fatalf("Get() record = %+v, want %+v", persisted, unchanged)
	}

	removed, changed, err := allowlist.Remove("https://ZEBRA.tailnet.ts.net")
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if !changed || removed.Revision != 3 {
		t.Fatalf("Remove() revision = %d, changed = %v, want 3, true", removed.Revision, changed)
	}
	if want := []string{"https://alpha.tailnet.ts.net"}; !reflect.DeepEqual(removed.Origins, want) {
		t.Fatalf("Remove() origins = %#v, want %#v", removed.Origins, want)
	}
}
