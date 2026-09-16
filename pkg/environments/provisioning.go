package environments

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// SourceStrategyKind identifies how workspace code or assets are provisioned into the environment.
type SourceStrategyKind string

const (
	// SourceStrategyKindLocalMount mounts a directory directly from the local host filesystem into the container.
	SourceStrategyKindLocalMount SourceStrategyKind = "local_mount"

	// SourceStrategyKindRemoteExistingPath uses a pre-existing directory path on a remote SSH host to mount into the container.
	SourceStrategyKindRemoteExistingPath SourceStrategyKind = "remote_existing_path"

	// SourceStrategyKindSync syncs files from local host to remote environment via rsync/sftp (extensible).
	SourceStrategyKindSync SourceStrategyKind = "sync"

	// SourceStrategyKindRegistryImage provisions the environment strictly from a pre-built container registry image (extensible).
	SourceStrategyKindRegistryImage SourceStrategyKind = "registry_image"

	// SourceStrategyKindGitCheckout clones a repository ref directly inside the environment (extensible).
	SourceStrategyKindGitCheckout SourceStrategyKind = "git_checkout"
)

// LocalMountConfig configures local host directory bind-mounting.
type LocalMountConfig struct {
	// HostPath is the path on the host to mount. If empty, defaults to the active workspace path.
	HostPath string `json:"host_path,omitempty"`
	// ContainerPath is the mount target inside the container (e.g. "/workspace"). Must be absolute.
	ContainerPath string `json:"container_path"`
	// ReadOnly specifies whether the mount is read-only.
	ReadOnly bool `json:"read_only,omitempty"`
}

// RemoteExistingPathConfig configures mounting an existing directory on a remote SSH host into the container.
type RemoteExistingPathConfig struct {
	// RemotePath is the existing directory on the remote host. Must be absolute.
	RemotePath string `json:"remote_path"`
	// ContainerPath is the mount target inside the container (e.g. "/workspace"). Must be absolute.
	ContainerPath string `json:"container_path"`
	// ReadOnly specifies whether the mount is read-only.
	ReadOnly bool `json:"read_only,omitempty"`
}

// SyncConfig configures syncing workspace files to the environment.
type SyncConfig struct {
	SourcePath      string   `json:"source_path,omitempty"`
	RemotePath      string   `json:"remote_path,omitempty"`
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`
	DeleteOrphaned  bool     `json:"delete_orphaned,omitempty"`
}

// RegistryImageConfig configures provisioning purely from a container registry image.
type RegistryImageConfig struct {
	Image      string `json:"image"`
	PullPolicy string `json:"pull_policy,omitempty"` // "always", "if_not_present", "never"
}

// GitCheckoutConfig configures cloning a git repository into the environment.
// Credentials in the URL (user:pass@) are strictly forbidden to prevent secret leaks.
type GitCheckoutConfig struct {
	RepositoryURL string `json:"repository_url"`
	Ref           string `json:"ref,omitempty"` // branch, tag, or commit OID
	Depth         int    `json:"depth,omitempty"`
}

// AdditionalMount specifies an extra volume or bind mount into the container.
type AdditionalMount struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only,omitempty"`
}

// SourceStrategy specifies how code/assets are provided to the environment.
type SourceStrategy struct {
	Kind               SourceStrategyKind        `json:"kind"`
	LocalMount         *LocalMountConfig         `json:"local_mount,omitempty"`
	RemoteExistingPath *RemoteExistingPathConfig `json:"remote_existing_path,omitempty"`
	Sync               *SyncConfig               `json:"sync,omitempty"`
	RegistryImage      *RegistryImageConfig      `json:"registry_image,omitempty"`
	GitCheckout        *GitCheckoutConfig        `json:"git_checkout,omitempty"`
}

// WorkspaceProvisioning defines the full workspace provisioning model for an environment.
type WorkspaceProvisioning struct {
	Strategy            SourceStrategy    `json:"strategy"`
	ContainerWorkingDir string            `json:"container_working_dir,omitempty"`
	Mounts              []AdditionalMount `json:"mounts,omitempty"`
}

// Validate checks that the SourceStrategy configuration is valid.
func (s *SourceStrategy) Validate() error {
	if s == nil {
		return errors.New("source strategy is nil")
	}

	switch s.Kind {
	case SourceStrategyKindLocalMount:
		if s.LocalMount == nil {
			return errors.New("local_mount configuration is required for local_mount strategy")
		}
		if s.RemoteExistingPath != nil || s.Sync != nil || s.RegistryImage != nil || s.GitCheckout != nil {
			return errors.New("only local_mount configuration must be set for local_mount strategy")
		}
		s.LocalMount.ContainerPath = strings.TrimSpace(s.LocalMount.ContainerPath)
		if s.LocalMount.ContainerPath == "" {
			return errors.New("local_mount container_path cannot be empty")
		}
		if !strings.HasPrefix(s.LocalMount.ContainerPath, "/") {
			return errors.New("local_mount container_path must be an absolute path")
		}
		if s.LocalMount.HostPath != "" {
			s.LocalMount.HostPath = strings.TrimSpace(s.LocalMount.HostPath)
			if !filepath.IsAbs(s.LocalMount.HostPath) {
				return errors.New("local_mount host_path must be an absolute path when specified")
			}
		}

	case SourceStrategyKindRemoteExistingPath:
		if s.RemoteExistingPath == nil {
			return errors.New("remote_existing_path configuration is required for remote_existing_path strategy")
		}
		if s.LocalMount != nil || s.Sync != nil || s.RegistryImage != nil || s.GitCheckout != nil {
			return errors.New("only remote_existing_path configuration must be set for remote_existing_path strategy")
		}
		s.RemoteExistingPath.RemotePath = strings.TrimSpace(s.RemoteExistingPath.RemotePath)
		if s.RemoteExistingPath.RemotePath == "" {
			return errors.New("remote_existing_path remote_path cannot be empty")
		}
		if !strings.HasPrefix(s.RemoteExistingPath.RemotePath, "/") {
			return errors.New("remote_existing_path remote_path must be an absolute path")
		}
		s.RemoteExistingPath.ContainerPath = strings.TrimSpace(s.RemoteExistingPath.ContainerPath)
		if s.RemoteExistingPath.ContainerPath == "" {
			return errors.New("remote_existing_path container_path cannot be empty")
		}
		if !strings.HasPrefix(s.RemoteExistingPath.ContainerPath, "/") {
			return errors.New("remote_existing_path container_path must be an absolute path")
		}

	case SourceStrategyKindSync:
		if s.Sync == nil {
			return errors.New("sync configuration is required for sync strategy")
		}
		if s.LocalMount != nil || s.RemoteExistingPath != nil || s.RegistryImage != nil || s.GitCheckout != nil {
			return errors.New("only sync configuration must be set for sync strategy")
		}

	case SourceStrategyKindRegistryImage:
		if s.RegistryImage == nil {
			return errors.New("registry_image configuration is required for registry_image strategy")
		}
		if s.LocalMount != nil || s.RemoteExistingPath != nil || s.Sync != nil || s.GitCheckout != nil {
			return errors.New("only registry_image configuration must be set for registry_image strategy")
		}
		s.RegistryImage.Image = strings.TrimSpace(s.RegistryImage.Image)
		if s.RegistryImage.Image == "" {
			return errors.New("registry_image image cannot be empty")
		}

	case SourceStrategyKindGitCheckout:
		if s.GitCheckout == nil {
			return errors.New("git_checkout configuration is required for git_checkout strategy")
		}
		if s.LocalMount != nil || s.RemoteExistingPath != nil || s.Sync != nil || s.RegistryImage != nil {
			return errors.New("only git_checkout configuration must be set for git_checkout strategy")
		}
		s.GitCheckout.RepositoryURL = strings.TrimSpace(s.GitCheckout.RepositoryURL)
		if s.GitCheckout.RepositoryURL == "" {
			return errors.New("git_checkout repository_url cannot be empty")
		}
		// Security invariant: never allow embedded credentials in Git URLs
		if parsed, err := url.Parse(s.GitCheckout.RepositoryURL); err == nil && parsed.User != nil {
			return errors.New("git_checkout repository_url must not contain embedded userinfo or credentials")
		}

	default:
		return fmt.Errorf("unsupported source strategy kind: %q", s.Kind)
	}

	return nil
}

// Validate checks the WorkspaceProvisioning configuration.
func (p *WorkspaceProvisioning) Validate() error {
	if p == nil {
		return errors.New("workspace provisioning is nil")
	}
	if err := p.Strategy.Validate(); err != nil {
		return fmt.Errorf("invalid provisioning strategy: %w", err)
	}

	if p.ContainerWorkingDir != "" {
		p.ContainerWorkingDir = strings.TrimSpace(p.ContainerWorkingDir)
		if !strings.HasPrefix(p.ContainerWorkingDir, "/") {
			return errors.New("container_working_dir must be an absolute path")
		}
	}

	for i, m := range p.Mounts {
		if strings.TrimSpace(m.ContainerPath) == "" || !strings.HasPrefix(m.ContainerPath, "/") {
			return fmt.Errorf("mount[%d] container_path must be a non-empty absolute path", i)
		}
		if strings.TrimSpace(m.HostPath) == "" || !filepath.IsAbs(m.HostPath) {
			return fmt.Errorf("mount[%d] host_path must be a non-empty absolute path", i)
		}
	}

	return nil
}

// Clone returns a deep copy of SourceStrategy.
func (s *SourceStrategy) Clone() SourceStrategy {
	if s == nil {
		return SourceStrategy{}
	}
	cp := *s
	if s.LocalMount != nil {
		lm := *s.LocalMount
		cp.LocalMount = &lm
	}
	if s.RemoteExistingPath != nil {
		rep := *s.RemoteExistingPath
		cp.RemoteExistingPath = &rep
	}
	if s.Sync != nil {
		sc := *s.Sync
		if s.Sync.ExcludePatterns != nil {
			sc.ExcludePatterns = append([]string(nil), s.Sync.ExcludePatterns...)
		}
		cp.Sync = &sc
	}
	if s.RegistryImage != nil {
		ri := *s.RegistryImage
		cp.RegistryImage = &ri
	}
	if s.GitCheckout != nil {
		gc := *s.GitCheckout
		cp.GitCheckout = &gc
	}
	return cp
}

// Clone returns a deep copy of WorkspaceProvisioning.
func (p *WorkspaceProvisioning) Clone() WorkspaceProvisioning {
	if p == nil {
		return WorkspaceProvisioning{}
	}
	cp := *p
	cp.Strategy = p.Strategy.Clone()
	if p.Mounts != nil {
		cp.Mounts = make([]AdditionalMount, len(p.Mounts))
		copy(cp.Mounts, p.Mounts)
	}
	return cp
}
