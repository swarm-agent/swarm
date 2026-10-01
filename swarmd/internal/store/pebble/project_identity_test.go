package pebblestore

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func projectIconFixture(t *testing.T, width int) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, width, 1))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

// Purpose: PutProject/UpdateProject must durably preserve valid project identity,
// reject invalid PNGs without partial writes, and isolate accounts. Store-level
// reopen and mutation assertions are the narrowest proof of this persistence boundary.
func TestProjectIconPersistence(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewSessionStore(db)
	icon := projectIconFixture(t, 2)
	p := &ProjectRecord{Name: "Identity", IconPNGDataURL: icon}
	if err := store.PutProject("account-a", p); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store = NewSessionStore(db)
	got, found, err := store.GetProject("account-a", p.ID)
	if err != nil || !found || got.IconPNGDataURL != icon {
		t.Fatalf("reopen: %+v, %v", got, err)
	}
	if _, found, err := store.GetProject("account-b", p.ID); err != nil || found {
		t.Fatalf("cross-account read: found=%v err=%v", found, err)
	}
	if _, err := store.UpdateProject("account-b", p.ID, func(p *ProjectRecord) error { p.IconPNGDataURL = ""; return nil }); err == nil {
		t.Fatal("foreign update succeeded")
	}
	if _, err := store.UpdateProject("account-a", p.ID, func(p *ProjectRecord) error { p.Name = "Renamed"; return nil }); err != nil {
		t.Fatal(err)
	}
	invalid := []string{
		"https://example.invalid/icon.png", "data:image/svg+xml;base64,PHN2Zz4=",
		"data:image/png;base64,!!!!", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a PNG")),
		"data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(1<<20)+4),
		projectIconFixture(t, 1025), icon[:len(icon)-8],
	}
	for _, value := range invalid {
		if _, err := store.UpdateProject("account-a", p.ID, func(p *ProjectRecord) error { p.IconPNGDataURL = value; return nil }); err == nil {
			t.Fatal("invalid icon accepted")
		}
		got, found, err := store.GetProject("account-a", p.ID)
		if err != nil || !found || got.IconPNGDataURL != icon || got.Name != "Renamed" {
			t.Fatalf("rejected write changed project: %+v %v", got, err)
		}
	}
	list, err := store.ListProjects("account-a", 10)
	if err != nil || len(list) != 1 || list[0].IconPNGDataURL != icon {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := store.UpdateProject("account-a", p.ID, func(p *ProjectRecord) error { p.IconPNGDataURL = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.GetProject("account-a", p.ID)
	if err != nil || got.IconPNGDataURL != "" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	if err := store.PutProject("account-a", &ProjectRecord{Name: "Legacy without icon"}); err != nil {
		t.Fatal(err)
	}
}
