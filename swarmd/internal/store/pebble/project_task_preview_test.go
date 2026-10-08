package pebblestore

import (
	"fmt"
	"testing"
)

// Purpose: derivative caching must have bounded key/byte growth, TTL, collision
// isolation and restart cleanup without deleting originals. Store methods own
// these postconditions; no media codecs or live dataset are needed at this layer.
func TestProjectPreviewCacheBoundsAndExpiry(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewSessionStore(db)
	for i := 0; i < 1100; i++ {
		if err = s.PutProjectTaskPreview("account", "project", "task", "d", fmt.Sprint(i), []byte("jpeg")); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	err = scanRangeFromReader(db.db, scanRangeOptions{Prefix: "project_task_preview/v2/"}, func(_ string, raw []byte) (bool, error) {
		count++
		if len(raw) > 120000 {
			t.Fatal("oversized cache row")
		}
		return true, nil
	})
	if err != nil || count > 512 {
		t.Fatal("unbounded keys", count, err)
	}
	if err = s.PutProjectTaskPreview("account", "project", "task", "d", "latest", []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	key := taskPreviewKey("account", "project", "task", "d", "latest")
	// Force an identical bucket with foreign identity; it must be a cache miss.
	entry := taskPreviewEntry{Identity: []string{"foreign", "project", "task", "d", "latest"}, Expires: 1 << 62, Data: []byte("secret")}
	if err = db.PutJSON(key, entry); err != nil {
		t.Fatal(err)
	}
	if data, found, err := s.GetProjectTaskPreview("account", "project", "task", "d", "latest"); err != nil || found || data != nil {
		t.Fatal("cache collision disclosed bytes")
	}
	entry.Identity[0] = "account"
	entry.Expires = 1
	if err = db.PutJSON(key, entry); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetProjectTaskPreview("account", "project", "task", "d", "latest"); err != nil || found {
		t.Fatal("expired cache accepted")
	}
	if err = s.PutProjectTaskPreview("account", "project", "task", "d", "large", make([]byte, 80<<10+1)); err == nil {
		t.Fatal("oversized preview cached")
	}
	if err = db.PutBytes("project_task_preview/v1/old", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err = db.PutBytes("canonical-media", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err = s.prepareProjectPreviewCache(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.GetBytes("project_task_preview/v1/old"); err != nil || found {
		t.Fatal("old unbounded namespace retained")
	}
	if data, found, err := db.GetBytes("canonical-media"); err != nil || !found || string(data) != "original" {
		t.Fatal("original changed")
	}
}
