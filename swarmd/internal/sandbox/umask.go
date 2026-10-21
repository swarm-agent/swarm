package sandbox

// sandboxUmaskScript keeps new files writable by the shared daemon group and
// inaccessible to others. Shells and their descendants inherit this mask.
const sandboxUmaskScript = `umask 0007 && exec "$@"`

// withSandboxUmask preserves argv literally; command text is never interpolated
// into the wrapper. Worker and routed sandbox payloads start at this boundary.
func withSandboxUmask(command ...string) []string {
	return append([]string{"sh", "-c", sandboxUmaskScript, "swarm-exec"}, command...)
}

// Image builds install root-owned files for users outside root's group. Unlike
// private worker files, these tools and profiles must remain readable/executable
// by other UIDs. Only BuildEnvironment's root setup/profile steps use this mask.
const imageBuildUmaskScript = `umask 022 && exec "$@"`

func withImageBuildUmask(command ...string) []string {
	return append([]string{"sh", "-c", imageBuildUmaskScript, "swarm-image-build"}, command...)
}
