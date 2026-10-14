package sandbox

// sandboxUmaskScript keeps new files writable by the shared daemon group and
// inaccessible to others. Shells and their descendants inherit this mask.
const sandboxUmaskScript = `umask 0007 && exec "$@"`

// withSandboxUmask preserves argv literally; command text is never interpolated
// into the wrapper. Every sandbox payload starts at this boundary.
func withSandboxUmask(command ...string) []string {
	return append([]string{"sh", "-c", sandboxUmaskScript, "swarm-exec"}, command...)
}
