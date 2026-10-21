# Facilitate authorization

Handle key creation and connection for the person. Prefer an already working
local stdio connection or tailnet grant; those do not need a new key. For an
HTTPS connection that needs an AI key, explain the authority and lifetime and
obtain any required approval. Do not tell the person to figure out key creation.

## Daemon on the execution computer (POSIX)

Verify the configured HTTPS URL belongs to this local daemon before issuing a
key. Check its documented Tailscale identity and selected swarmctl installation;
do not infer ownership from a reachable URL alone. The helper issues through
the daemon's existing private owner socket, not through unauthenticated HTTP.

Use the normal private client config directory from connection.md, outside a
repository, marketplace or plugin cache. The connection file must be mode 0600
inside an owned 0700 directory. Initially configure HTTP mode without tokenFile.
Inspect existing settings first; neither helper silently replaces a credential.

After approval, run as the ordinary desktop user, using the discovered binary:

```sh
node bin/authorize.mjs --swarmctl /absolute/path/to/swarmctl
```

For a service-account installation whose wrapper requires root, add `--sudo`
only when authorized. It elevates only the owner CLI; the plugin and credential
file stay owned by the desktop user. The key works while it is used and stops
after 90 days unused; nothing needs renewing. Add `--expires-in-seconds` (60 to
31536000) only if the person wants a fixed end date.
The key allows reading and starting work; it does not approve tool calls or
grant model/worker administration. Never silently upgrade to full access.

Sudo is noninteractive. If the OS requires a password, give the person one
terminal command that runs `sudo -v` and then the exact helper above with
`--sudo`, preserving the selected SWARM_MCP_CONFIG if set. A password entered
in their own terminal is the only manual step; do not ask for it in chat.
If the agent's execution sandbox sets `no new privileges`, sudo cannot work
there even after approval. Use that same terminal handoff; do not keep retrying
sudo or switch to another execution tool to bypass the sandbox.
The helper process itself must not run under sudo. Do not change socket
permissions, add broad sudo rules, or run the AI client as root.

The helper privately captures `swarmctl setup sdk-token --ai-access write`,
writes the key at mode 0600, and adds only its file path to connection.json.
That is the same connection file the active plugin's `swarm-mcp.mjs` loads,
selected by its `SWARM_MCP_CONFIG` or the documented default location. The AI
should resolve this path and pass it to the helper; do not make the person
choose a configuration file. For example, the resulting nonsecret metadata is:

```json
{"mode":"http","url":"https://YOUR-SWARM.YOUR-TAILNET.ts.net:8444/mcp","tokenFile":"/absolute/private/credential-DIRECTORY/ai-key"}
```

It prints the nonsecret grant record path, access level and lifetime. Refresh
MCP and verify an actual tool call. A later 401 saying the key expired means it
went unused for 90 days (or passed a fixed end date): make a new one only with
the person's authority.

## Failures and cleanup

Do not repeatedly mint keys to debug connectivity. A failed configuration
attempt revokes its newly issued key when its ID is known. An uncertain issuance
or failed revocation retains a `.authorize-lock` and `credential-*/grant.json`
for recovery. Read only those nonsecret records, never `ai-key`. A `ready` state
means files were prepared; confirm the selected connection points to the same
key and a real MCP call succeeds before treating it as connected. After a crash,
inspect/revoke the recorded grant before clearing a stale lock and retrying.

For explicit revocation, use the same owner CLI:
`swarmctl setup revoke-sdk-token --id ID_FROM_GRANT_RECORD`.
The record contains no key. Preserve existing unrelated grants and settings.

## Daemon on another computer

This helper cannot create a local daemon's key for an unrelated remote Swarm.
Use authorized owner execution on the actual daemon host, or help the person
create a dedicated write key through its **AI access over Tailscale** UI and
transfer it through their secret manager/private terminal. Keep the secret out
of chat and tool output. If neither owner access nor a keyless grant is available,
explain that the owner must approve access; a plugin cannot grant itself access.
