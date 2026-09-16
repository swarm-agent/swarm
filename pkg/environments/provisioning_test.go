package environments

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProvisioning_LocalMount_Valid(t *testing.T) {
	prov := &WorkspaceProvisioning{
		Strategy: SourceStrategy{
			Kind: SourceStrategyKindLocalMount,
			LocalMount: &LocalMountConfig{
				HostPath:      "/workspaces/project",
				ContainerPath: "/workspace",
				ReadOnly:      false,
			},
		},
		ContainerWorkingDir: "/workspace",
		Mounts: []AdditionalMount{
			{
				HostPath:      "/var/run/docker.sock",
				ContainerPath: "/var/run/docker.sock",
				ReadOnly:      true,
			},
		},
	}

	if err := prov.Validate(); err != nil {
		t.Fatalf("expected valid provisioning, got: %v", err)
	}

	data, err := json.Marshal(prov)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var roundtrip WorkspaceProvisioning
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if roundtrip.Strategy.Kind != SourceStrategyKindLocalMount {
		t.Fatalf("kind mismatch: %s", roundtrip.Strategy.Kind)
	}
	if roundtrip.Strategy.LocalMount.ContainerPath != "/workspace" {
		t.Fatalf("container path mismatch: %s", roundtrip.Strategy.LocalMount.ContainerPath)
	}
	if len(roundtrip.Mounts) != 1 || roundtrip.Mounts[0].HostPath != "/var/run/docker.sock" {
		t.Fatalf("mounts mismatch: %+v", roundtrip.Mounts)
	}
}

func TestProvisioning_RemoteExistingPath_Valid(t *testing.T) {
	strat := &SourceStrategy{
		Kind: SourceStrategyKindRemoteExistingPath,
		RemoteExistingPath: &RemoteExistingPathConfig{
			RemotePath:    "/srv/repos/swarm-repo",
			ContainerPath: "/workspace",
			ReadOnly:      true,
		},
	}

	if err := strat.Validate(); err != nil {
		t.Fatalf("expected valid strategy, got: %v", err)
	}
}

func TestProvisioning_ExtensibleStrategies_Valid(t *testing.T) {
	// Sync
	syncStrat := &SourceStrategy{
		Kind: SourceStrategyKindSync,
		Sync: &SyncConfig{
			SourcePath:      "/local/src",
			RemotePath:      "/remote/src",
			ExcludePatterns: []string{".git", "node_modules"},
			DeleteOrphaned:  true,
		},
	}
	if err := syncStrat.Validate(); err != nil {
		t.Fatalf("expected valid sync strategy, got: %v", err)
	}

	// Registry Image
	regStrat := &SourceStrategy{
		Kind: SourceStrategyKindRegistryImage,
		RegistryImage: &RegistryImageConfig{
			Image:      "golang:1.26",
			PullPolicy: "if_not_present",
		},
	}
	if err := regStrat.Validate(); err != nil {
		t.Fatalf("expected valid registry image strategy, got: %v", err)
	}

	// Git Checkout
	gitStrat := &SourceStrategy{
		Kind: SourceStrategyKindGitCheckout,
		GitCheckout: &GitCheckoutConfig{
			RepositoryURL: "https://github.com/example/repo.git",
			Ref:           "main",
			Depth:         1,
		},
	}
	if err := gitStrat.Validate(); err != nil {
		t.Fatalf("expected valid git checkout strategy, got: %v", err)
	}
}

func TestProvisioning_GitCheckout_RejectsCredentials(t *testing.T) {
	gitStrat := &SourceStrategy{
		Kind: SourceStrategyKindGitCheckout,
		GitCheckout: &GitCheckoutConfig{
			RepositoryURL: "https://user:secretpassword@github.com/example/repo.git",
			Ref:           "main",
		},
	}

	err := gitStrat.Validate()
	if err == nil {
		t.Fatalf("expected error rejecting credentials in git URL, got nil")
	}
	if !strings.Contains(err.Error(), "userinfo or credentials") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestProvisioning_Validation_Errors(t *testing.T) {
	tests := []struct {
		name    string
		strat   *SourceStrategy
		wantErr string
	}{
		{
			name: "local mount missing config",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindLocalMount,
			},
			wantErr: "local_mount configuration is required",
		},
		{
			name: "local mount relative container path",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindLocalMount,
				LocalMount: &LocalMountConfig{
					ContainerPath: "workspace/relative",
				},
			},
			wantErr: "must be an absolute path",
		},
		{
			name: "local mount relative host path",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindLocalMount,
				LocalMount: &LocalMountConfig{
					HostPath:      "relative/host",
					ContainerPath: "/workspace",
				},
			},
			wantErr: "must be an absolute path when specified",
		},
		{
			name: "remote existing path relative remote path",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindRemoteExistingPath,
				RemoteExistingPath: &RemoteExistingPathConfig{
					RemotePath:    "relative/remote",
					ContainerPath: "/workspace",
				},
			},
			wantErr: "remote_path must be an absolute path",
		},
		{
			name: "registry image empty image",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindRegistryImage,
				RegistryImage: &RegistryImageConfig{
					Image: "",
				},
			},
			wantErr: "image cannot be empty",
		},
		{
			name: "git checkout empty url",
			strat: &SourceStrategy{
				Kind: SourceStrategyKindGitCheckout,
				GitCheckout: &GitCheckoutConfig{
					RepositoryURL: "",
				},
			},
			wantErr: "repository_url cannot be empty",
		},
		{
			name: "unsupported strategy kind",
			strat: &SourceStrategy{
				Kind: "quantum_teleportation",
			},
			wantErr: "unsupported source strategy kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.strat.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestProvisioning_CloneImmutability(t *testing.T) {
	orig := &WorkspaceProvisioning{
		Strategy: SourceStrategy{
			Kind: SourceStrategyKindSync,
			Sync: &SyncConfig{
				SourcePath:      "/src",
				RemotePath:      "/dst",
				ExcludePatterns: []string{"a", "b"},
			},
		},
		ContainerWorkingDir: "/work",
		Mounts: []AdditionalMount{
			{HostPath: "/host/a", ContainerPath: "/cont/a"},
		},
	}

	cloned := orig.Clone()
	cloned.Strategy.Sync.ExcludePatterns[0] = "mutated"
	cloned.Mounts[0].ContainerPath = "/mutated"

	if orig.Strategy.Sync.ExcludePatterns[0] != "a" {
		t.Fatalf("original ExcludePatterns mutated: %v", orig.Strategy.Sync.ExcludePatterns)
	}
	if orig.Mounts[0].ContainerPath != "/cont/a" {
		t.Fatalf("original Mounts mutated: %s", orig.Mounts[0].ContainerPath)
	}
}
