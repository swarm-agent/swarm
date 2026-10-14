# Choose the existing MCP connection

This reference configures the plugin's Node adapter, for local Codex and for
Claude Code/Desktop (Claude starts `bin/swarm-mcp`, which hands a saved
connection to the adapter; see [remote.md](remote.md#claude-clients)). ChatGPT
cloud/dot connections use [chatgpt-tunnel.md](chatgpt-tunnel.md).

The adapter uses Node 22+ and `bin/swarm-mcp.mjs`. Check `node --version`
on the execution computer. The adapter forwards Swarm's existing tools; it does
not start agents, install the daemon, expose ports, or provide its own tools.

Connection metadata lives in `swarm-mcp/connection.json` under XDG_CONFIG_HOME
(default `~/.config`) on Linux/macOS, or LOCALAPPDATA on Windows. An explicit
`SWARM_MCP_CONFIG` selects another file. With no default file, native mode is
used. A missing explicit file is an error. Inspect existing metadata before
editing; the setup helper refuses to overwrite it.

The adapter reads this file when its MCP process starts. Before running either
helper, inspect the active plugin/client MCP environment for `SWARM_MCP_CONFIG`
and use the same value. Setting an override only in a terminal does not change
the installed MCP process. With no override, both use the default client config
directory above. Refresh MCP after a change; do not edit a different copy.

Run the packaged helper from the plugin folder after choosing a mode:

```sh
node bin/connect.mjs native
node bin/connect.mjs docker swarm-headless
node bin/connect.mjs http https://YOUR-SWARM.YOUR-TAILNET.ts.net:8444/mcp
node bin/connect.mjs http https://YOUR-SWARM.YOUR-TAILNET.ts.net:8444/mcp --token-file /absolute/private/key-file
```

Run ONE choice. Native mode also accepts a selected swarmctl binary path; a
`socket` field can select its private socket. Docker mode uses `docker exec -i`
as the container's configured user. The user must already have Docker access.
Do not grant Docker access or change socket permissions as a silent fix.

For Tailscale, the execution computer must be connected to the tailnet. Keyless
access requires an explicit `swarmagent.dev/cap/swarm` grant AND Tailscale Serve
with app capabilities configured on the server. Merely being on the tailnet
is not enough. A 401 means the endpoint is reachable but authentication is
missing; a 403 means the operation is outside the grant. Neither warrants
reinstallation or a public relay. Read/write tailnet grants do not authorize
approving tool calls or managing models/workers; handle those in Swarm itself.

For the policy path, follow the maintained server guide at
https://github.com/swarm-agent/swarm/blob/dev/containers/headless/app/TAILNET.md
and its linked policy example. An administrator must merge a narrowly scoped
grant into the existing policy, not replace unrelated rules. If the person does
not administer that policy, prefer the dedicated AI-key option below on POSIX.

If using an AI key instead, facilitate creation and private handoff using
[AI-assisted authorization](authorization.md). When the daemon is on this
computer, the bundled helper can create a dedicated write key after approval;
the person does not need to find, copy or paste a key. On POSIX the key file must
be owned by that user and mode 0600. This adapter refuses token files on Windows
until ACL validation is qualified; use a keyless tailnet grant or native-client
OAuth there. Do not take keys in chat, put
them in shell arguments, save them in a project, or bundle them in the ZIP.
`tokenFile` is a path, never the secret. HTTPS is required; redirects are refused
so credentials cannot be forwarded to a different destination. This adapter
supports Swarm's stateless JSON HTTP transport, not arbitrary SSE MCP servers.

Refresh/reconnect the plugin after configuration. A useful proof is a successful
`swarm_list_models` or `swarm_list_workspaces` call, followed by the person's
requested small task. Never call an installed plugin "connected" on manifest
inspection alone. Preserve IDs after ambiguous writes and inspect existing
sessions before retries.

For an OAuth relay use the client's native HTTP/OAuth MCP connection, as covered
in remote.md. This adapter does not store OAuth grants or implement login.
