package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestSwarmNodeStoreCRUDAndNormalization(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "swarm-nodes.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	nodes := NewSwarmNodeStore(store)
	created, err := nodes.Put(SwarmNodeRecord{
		SwarmID:      " swarm-child-1 ",
		Name:         " GPU Box ",
		Role:         "master",
		Kind:         "remote",
		Transport:    "TAILSCALE",
		BackendURL:   " http://swarm-gpu.tailnet.ts.net:8421/ ",
		DesktopURL:   " https://swarm-gpu.tailnet.ts.net/ ",
		MagicDNSName: " swarm-gpu ",
		TailnetFQDN:  " swarm-gpu.tailnet.ts.net ",
		TailscaleIP:  " 100.64.1.2 ",
		DeploymentID: " deploy-1 ",
		Source:       "ssh_deploy",
		Status:       "registered",
		LastSeenAt:   -1,
	})
	if err != nil {
		t.Fatalf("put node: %v", err)
	}
	if created.SwarmID != "swarm-child-1" {
		t.Fatalf("swarm id = %q", created.SwarmID)
	}
	if created.Role != "controller" {
		t.Fatalf("role = %q", created.Role)
	}
	if created.Transport != "tailscale" {
		t.Fatalf("transport = %q", created.Transport)
	}
	if created.BackendURL != "http://swarm-gpu.tailnet.ts.net:8421" {
		t.Fatalf("backend url = %q", created.BackendURL)
	}
	if created.DesktopURL != "https://swarm-gpu.tailnet.ts.net" {
		t.Fatalf("desktop url = %q", created.DesktopURL)
	}
	if created.Source != "ssh-deploy" {
		t.Fatalf("source = %q", created.Source)
	}
	if created.Status != "registered" {
		t.Fatalf("status = %q", created.Status)
	}
	if created.LastSeenAt != 0 {
		t.Fatalf("last seen at = %d", created.LastSeenAt)
	}
	if created.CreatedAt <= 0 || created.UpdatedAt <= 0 {
		t.Fatalf("timestamps not populated: %+v", created)
	}

	loaded, ok, err := nodes.Get("swarm-child-1")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !ok {
		t.Fatal("expected node to be stored")
	}
	if loaded.BackendURL != created.BackendURL {
		t.Fatalf("loaded backend url = %q", loaded.BackendURL)
	}

	items, err := nodes.List(100)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("node count = %d", len(items))
	}
	if items[0].SwarmID != "swarm-child-1" {
		t.Fatalf("listed swarm id = %q", items[0].SwarmID)
	}

	if err := nodes.Delete("swarm-child-1"); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, ok, err := nodes.Get("swarm-child-1"); err != nil || ok {
		t.Fatalf("expected deleted node, ok=%v err=%v", ok, err)
	}
}

func TestSwarmNodeStoreRequiresBackendURL(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "swarm-nodes.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	nodes := NewSwarmNodeStore(store)
	if _, err := nodes.Put(SwarmNodeRecord{SwarmID: "swarm-child-1", Name: "child"}); err == nil {
		t.Fatal("expected backend url validation error")
	}
}
