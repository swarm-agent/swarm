# Swarm setup and MCP plugins

These packages guide setup and delegation to your own Swarm. They do not
launch one CLI per worker, start another daemon, publish a listener, or require
a relay. The setup skill guides installation only when Swarm is actually absent.

The plugin's Node adapter (local Codex, and Claude with a saved connection)
requires Node 22+ on the client computer. Choose native
Swarm, an existing Docker container, or an HTTPS Swarm endpoint on your tailnet.
Run setup and follow
`skills/setup/references/connection.md`. An AI key stays in a private file outside
the plugin; no credentials belong in its manifest or archive. Cloud clients
need their own network route; installing locally does not connect dots by itself.

For an existing local daemon reached over HTTPS, setup can create and connect a
short-lived write key after approval using `bin/authorize.mjs`. The person does
not copy a key. Service-account installations may require one OS password prompt
in their terminal. See `skills/setup/references/authorization.md`.

## Quick start (Claude Code, Ubuntu 24.04 x86_64)

```sh
curl -fsSL https://swarmagent.dev/install | sudo bash
claude plugin marketplace add swarm-agent/swarm
claude plugin install swarm@swarm
```

The installer gives you an AI key in `~/.config/swarm/ai-key`; with no
`connection.json`, the plugin's `bin/swarm-mcp` finds it and connects to the
box's MCP on `http://127.0.0.1:7783/mcp` through the Node adapter (Node 22+;
plain HTTP is accepted only for loopback). Then ask Claude to set up Swarm;
you type your model key on the setup page or set it by script, never in chat.
Without the box key, `bin/swarm-mcp` uses `swarm mcp` (the desktop and
terminal launcher install), or `swarmctl mcp` from it, and reports a Swarm too
old for AI connections instead of starting it.

## Choose the package and client

| Client/use | Package and connection |
| --- | --- |
| Local Codex | `swarm` skills plus Node stdio adapter; native, Docker or tailnet mode |
| ChatGPT Work/dot with private remote Swarm | `swarm-setup` skills plus a separately added per-user OpenAI tunnel MCP connection |
| Claude Code/Desktop local MCP | Claude manifest/shell wrapper; native, Docker or tailnet mode (a saved connection goes through the Node adapter) |
| Claude.ai | Its remote HTTPS connector; OpenAI tunnel IDs do not apply |

The setup skill routes to [ChatGPT private tunnels](skills/setup/references/chatgpt-tunnel.md),
[client-specific remote options](skills/setup/references/remote.md) and the
[Swarm MCP protocol contract](skills/setup/references/mcp-protocol.md).
Adding a custom MCP server exposes tools but does not install the setup skill.
Installing skills does not deploy Swarm or create the user's authenticated route.

## Package the current instructions

Build a distributable local marketplace from the repository root:

```sh
node scripts/package-swarm-codex.mjs /absolute/new/output-folder
codex plugin marketplace add /absolute/new/output-folder
codex plugin add swarm@swarm-local
```

For ChatGPT setup with a separately configured tunnel connection, build the
portable skills-only package instead:

```sh
node scripts/package-swarm-codex.mjs /absolute/new/chatgpt-output-folder --skills-only
```

It is named `swarm-setup` and includes root `plugin.json`, onboarding/delegation
skills, references and local setup helpers. It declares no MCP server, avoiding
an attempted localhost/stdio launch in ChatGPT cloud. Each person connects their
own tunnel using the setup skill. The helper scripts require execution on their
selected host; uploading a ZIP does not provide that host access. Use the
client's supported plugin import/install flow; don't confuse a local install
with publication or assume local skill visibility on a cloud dot.

The packager includes setup/delegation skills and helpers; the local variant
also registers the MCP adapter. It excludes the separate app-building skill,
tests and personal configuration. Zip the contents
of `output-folder/plugins/swarm` to share the plugin. If `swarm-local` is already
registered at another path, inspect its existing registration before replacing it.
Source edits do not update earlier ZIPs or installed cache copies. Rebuild and
refresh/reinstall the intended package, then verify skill availability in the
actual client. The custom tunnel MCP connection remains a separate installation.

The local MCP configuration uses `cwd: "."` with a relative Node script path. Codex
resolves this against the plugin root. Do not use literal `${CLAUDE_PLUGIN_ROOT}`
or `${PLUGIN_ROOT}` in command arguments: the tested Codex 0.154.0 did not expand
them. Installation and skill discovery alone do not prove MCP connectivity.

Local mode is a manual/local plugin installation. ChatGPT public-directory MCP
submission currently has a separate remote HTTPS requirement; uploading this
stdio package is not a claim of public-directory eligibility. A private custom
tunnel is separately supported and tested; it is not a public MCP listing.
An optional relay can be configured later with native client OAuth. See
https://developers.openai.com/plugins/build/plugins and
https://developers.openai.com/plugins/deploy/submission.

## Qualification boundaries

A real ChatGPT dot delegated a Finder task through an OpenAI private tunnel to
Linux amd64 headless Swarm at revision `0dfbc4c1d`, using Codex provider sign-in.
The agent listed/read the test workspace, finished, and left it unchanged.
The tunnel guide records the setup and distinguishes that proof from service
health, optional discovery warnings and automatic completion notifications.
The MCP endpoint fix and required fast gate were tested; the documentation and
packager do not themselves establish further runtime qualifications.

A separate agent with no conversation history followed packaged skill 0.2.2 at
revision `5a4d3d996`, deployed fresh Swarm state, completed a new human-approved
Codex sign-in, issued a scoped key and verified a real Finder task through the
ChatGPT connector. It received source and SSH access to an existing Docker VM,
a tunnel ID and private runtime-key file, and reused an existing ChatGPT MCP
registration. This tested fresh Swarm onboarding, not cloud-account creation,
plugin upload or a separate dot UI flow. The task left the workspace unchanged.

Mac/Windows Docker Desktop and Claude.ai relay use remain unqualified by this
test. Anthropic's Console tunnels currently exclude Claude.ai connectors; see
its [official tunnel docs](https://platform.claude.com/docs/en/agents-and-tools/mcp-tunnels/overview).
Token-file permissions are checked on POSIX; Windows token-file mode is refused
until ACL checks are qualified.
Package tests check transport configuration; they do not qualify other platforms
or clients. Confirm a real delegated task on the intended installation.
