package environments

import "errors"

// ErrSSHManagedBuildUnavailable is a capability rejection, not an SSH auth error.
// Retained for callers distinguishing unsupported configurations. Interrupted
// SSH builds instead report explicit unconfirmed cleanup; successful SSH builds
// use observed immutable images and fenced saved connection definitions.
var ErrSSHManagedBuildUnavailable = errors.New("SSH managed builds unavailable: remote build supervision, durable owned cleanup receipts and immutable connection provenance are required; no local or remote-path fallback is supported")

// ValidateManagedBuildConnection checks the code-owned build transport contract.
// Saved capability booleans and caller-supplied image receipts are not authority.
// Callers must invoke it before admitting operations or allocating resources.
func ValidateManagedBuildConnection(conn *Connection) error {
	if conn == nil {
		return errors.New("managed build connection is required")
	}
	switch conn.Kind {
	case ConnectionKindLocalPodman:
		return nil
	case ConnectionKindSSH:
		return conn.Clone().Validate()
	default:
		return errors.New("managed build requires local_podman or SSH Docker connection")
	}
}
