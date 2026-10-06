package environments

import "testing"

// Purpose: code-owned build transport admission accepts valid SSH definitions,
// not capability flags on malformed connections; local Podman stays supported.
// Pure domain assertions are the narrowest layer for this pre-effect contract.
func TestManagedBuildConnectionCapability(t *testing.T) {
	ssh := &Connection{ID: "ssh", Name: "Remote", AccountScopeID: "account", WorkspaceID: "workspace", Kind: ConnectionKindSSH, SSH: &SSHConfig{Host: "example.invalid", User: "tester", Port: 22}}
	if err := ValidateManagedBuildConnection(ssh); err != nil {
		t.Fatal(err)
	}
	before := ConnectionTransportDigest(ssh)
	ssh.SSH.Host = "other.invalid"
	if before == ConnectionTransportDigest(ssh) {
		t.Fatal("transport edit not fenced")
	}
	if err := ValidateManagedBuildConnection(&Connection{Kind: ConnectionKindLocalPodman}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*Connection{nil, {Kind: ConnectionKindSSH}, {Kind: ConnectionKindLocalDocker}, {Kind: "unknown"}} {
		if err := ValidateManagedBuildConnection(conn); err == nil {
			t.Fatal("unsupported or malformed connection accepted")
		}
	}
}
