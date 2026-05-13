package pebblestore

import (
	"path/filepath"
	"testing"
)

func TestSwarmNodeStoreSyncsCanonicalRuntime(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "swarm-node-topology.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	nodes := NewSwarmNodeStore(store, topology)

	created, err := nodes.Put(SwarmNodeRecord{
		SwarmID:    "child-1",
		Name:       "Child One",
		Role:       "child",
		Transport:  "TAILSCALE",
		BackendURL: "http://child.example:7781/",
		DesktopURL: "https://child.example/",
		Status:     "online",
		Source:     "mirror",
	})
	if err != nil {
		t.Fatalf("put node: %v", err)
	}

	runtimeRecord, ok, err := topology.GetRuntime(created.SwarmID)
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.BackendURL != "http://child.example:7781" {
		t.Fatalf("runtime backend url = %q", runtimeRecord.BackendURL)
	}
	if runtimeRecord.Relationship != "child" {
		t.Fatalf("runtime relationship = %q", runtimeRecord.Relationship)
	}
	if !topologyObservedSourcePresent(runtimeRecord.ObservedSources, topologyRuntimeSourceNode) {
		t.Fatalf("runtime observed sources = %+v", runtimeRecord.ObservedSources)
	}

	if err := nodes.Delete(created.SwarmID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, ok, err := topology.GetRuntime(created.SwarmID); err != nil || ok {
		t.Fatalf("runtime after node delete ok=%t err=%v", ok, err)
	}
}

func TestSwarmStoreSyncsCanonicalRuntimeSources(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "swarm-store-topology.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	topology := NewTopologyStore(store)
	swarms := NewSwarmStore(store, topology)

	if _, err := swarms.PutLocalNode(SwarmLocalNodeRecord{
		SwarmID: "self-1",
		Name:    "Primary",
		Role:    "master",
		Transports: []SwarmTransportRecord{
			{Kind: "tailscale"},
		},
	}); err != nil {
		t.Fatalf("put local node: %v", err)
	}
	if _, err := swarms.PutTrustedPeer(SwarmTrustedPeerRecord{
		SwarmID:       "self-1",
		Name:          "Primary",
		Relationship:  "manager",
		TransportMode: "tailscale",
	}); err != nil {
		t.Fatalf("put trusted peer: %v", err)
	}

	runtimeRecord, ok, err := topology.GetRuntime("self-1")
	if err != nil || !ok {
		t.Fatalf("get runtime ok=%t err=%v", ok, err)
	}
	if runtimeRecord.Relationship != "self" {
		t.Fatalf("runtime relationship = %q", runtimeRecord.Relationship)
	}
	if runtimeRecord.Transport != "tailscale" {
		t.Fatalf("runtime transport = %q", runtimeRecord.Transport)
	}
	if !topologyObservedSourcePresent(runtimeRecord.ObservedSources, topologyRuntimeSourceLocalNode) {
		t.Fatalf("missing local source: %+v", runtimeRecord.ObservedSources)
	}
	if !topologyObservedSourcePresent(runtimeRecord.ObservedSources, topologyRuntimeSourceTrustedPeer) {
		t.Fatalf("missing trusted peer source: %+v", runtimeRecord.ObservedSources)
	}

	if err := swarms.DeleteTrustedPeer("self-1"); err != nil {
		t.Fatalf("delete trusted peer: %v", err)
	}
	runtimeRecord, ok, err = topology.GetRuntime("self-1")
	if err != nil || !ok {
		t.Fatalf("get runtime after trusted peer delete ok=%t err=%v", ok, err)
	}
	if runtimeRecord.Relationship != "self" {
		t.Fatalf("runtime relationship after trusted peer delete = %q", runtimeRecord.Relationship)
	}
	if topologyObservedSourcePresent(runtimeRecord.ObservedSources, topologyRuntimeSourceTrustedPeer) {
		t.Fatalf("trusted peer source still present: %+v", runtimeRecord.ObservedSources)
	}
}
