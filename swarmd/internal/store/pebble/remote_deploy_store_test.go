package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestRemoteDeploySessionStorePersistsDiskPreflightFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "remote-deploy.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	sessions := NewRemoteDeploySessionStore(store)
	created, err := sessions.Put(RemoteDeploySessionRecord{
		ID:   "remote-disk-test",
		Name: "remote disk test",
		RemoteDisk: RemoteDeployDiskRecord{
			Path:           " /var/lib/swarm ",
			AvailableBytes: 4 * 1024 * 1024 * 1024,
			RequiredBytes:  768 * 1024 * 1024,
		},
	})
	if err != nil {
		t.Fatalf("put session: %v", err)
	}
	if created.RemoteDisk.Path != "/var/lib/swarm" {
		t.Fatalf("created disk path = %q", created.RemoteDisk.Path)
	}

	loaded, ok, err := sessions.Get("remote-disk-test")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !ok {
		t.Fatalf("expected stored remote deploy session")
	}
	if loaded.RemoteDisk.Path != "/var/lib/swarm" {
		t.Fatalf("loaded disk path = %q", loaded.RemoteDisk.Path)
	}
	if loaded.RemoteDisk.AvailableBytes != 4*1024*1024*1024 {
		t.Fatalf("loaded available bytes = %d", loaded.RemoteDisk.AvailableBytes)
	}
	if loaded.RemoteDisk.RequiredBytes != 768*1024*1024 {
		t.Fatalf("loaded required bytes = %d", loaded.RemoteDisk.RequiredBytes)
	}
}
