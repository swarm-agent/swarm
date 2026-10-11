# Local deployment and connection

The OpenAI MCP entry launches `bin/swarm-mcp.mjs`; configure it with
`bin/connect.mjs` as described in connection.md. Native mode delegates to the
existing shell bridge. No inbound port,
OAuth relay or pasted MCP key is needed. Run it with the identity permitted to
access Swarm's private owner socket. Never chmod that socket to make it public,
run the whole AI application as root, or put interactive sudo inside MCP startup.

## Existing native Linux installation

Use `swarm mcp` through the packaged launcher. It should produce MCP protocol on
stdout and diagnostics on stderr. `SWARM_MCP_BIN` selects an explicit swarmctl
binary and `SWARM_MCP_SOCKET` selects an explicit private socket. These are paths,
not credentials. A service-user mismatch needs the installation's supported
launcher/identity setup; repeatedly reinstalling is not a remedy.

If the daemon is stopped, inspect `swarm --help` and the installation's service
status before restarting it. Missing permission, missing daemon and missing
binary are different failures. Preserve existing configuration and work.

## New native installation: Linux x86_64

Use the maintained installer from
https://github.com/swarm-agent/swarm/blob/main/install.sh and its README.
Download it to a selected temporary directory, inspect it and its install plan,
then run `sh install.sh` after authorization for its system changes. It installs
under `/usr/local` and offers service or files-only installation. Do not silently
accept sudo or reinstall an existing deployment. Do not run the headless server
installer on someone's desktop: that changes firewall, Tailscale and services.

Complete owner/provider/workspace setup in Swarm. Refresh this plugin's MCP and
call `swarm_list_providers` to verify connection. Never confuse having installed
files with a running, configured daemon.

## Docker: local headless alternative

Use this when the person chooses Docker. Docker Desktop must be installed and
running on Mac/Windows; use Linux containers. The current image is Linux amd64.
Apple Silicon requires amd64 emulation; this plugin has not qualified Docker
Desktop on Mac or Windows. Do not claim a native Mac/Windows daemon exists.

Use a qualified published image digest if one is available in the actual Swarm
release. Never invent a digest or claim an unpublished package is installable.
For a source build, obtain a reviewed checkout of
https://github.com/swarm-agent/swarm and run from its root:

```sh
docker buildx build --platform linux/amd64 --load -t swarm-headless:local .
```

Select one project checkout (not a linked Git worktree), with a commit and
permissions suitable for container UID 10001. Do not change ownership of the
person's home. Confirm container/volume names are unused or belong to the
intended installation; do not replace them or share live daemon volumes.
Set `PROJECT_DIR` to the selected absolute project path using the current shell.
For a POSIX shell:

```sh
docker run -d --platform linux/amd64 --name swarm-headless \
  --cap-drop ALL --security-opt no-new-privileges \
  --mount type=volume,src=swarm-headless-config,dst=/etc/swarmd \
  --mount type=volume,src=swarm-headless-data,dst=/var/lib/swarmd \
  --mount type=volume,src=swarm-headless-cache,dst=/var/cache/swarmd \
  --mount type=volume,src=swarm-headless-logs,dst=/var/log/swarmd \
  --mount "type=bind,src=${PROJECT_DIR:?select a project},dst=/project" \
  swarm-headless:local
docker exec swarm-headless swarmctl setup status
```

Use PowerShell argument syntax on Windows rather than pasting POSIX continuations.
Mount only that project; never mount the Docker socket, daemon host storage, or
home directory, and never add privileged mode. This container does not provide
the host installation's separate agent sandbox; keep command permissions on.

Read the checkout's `containers/headless/README.md`, section **Fresh setup without
Desktop**, before onboarding. Execute only missing steps: identity, provider via
private stdin (`setup credential --api-key-stdin`), model choices from the account
catalog, workspace `/project`, and `setup complete`. The user supplies secrets
through a secret manager or an existing private file outside the project; do not
read it into the conversation. Do not hardcode role models. Check status again.

For this plugin run `node bin/connect.mjs docker swarm-headless` from its
folder. For a client without this plugin, the equivalent direct MCP command is:

```json
{"command":"docker","args":["exec","-i","swarm-headless","swarmctl","mcp"]}
```

This direct command also works on Windows; it requires no POSIX wrapper. Do not
use `-t`: a pseudo-terminal corrupts stdio protocol transport. In clients that
support plugin MCP environment overrides, the bundled launcher alternatively
accepts `SWARM_MCP_CONTAINER=swarm-headless`. Choose one connection, not duplicate
servers. Refresh/reconnect, then verify an actual MCP call.

Docker startup does not publish any network ports. Stop/start the same container
to preserve state; deleting volumes deletes Swarm's account and work. Upgrades
need a backup and a stopped old writer before another daemon uses those volumes.
