# ChatGPT and dots: private OpenAI tunnel

Use for requested private access from ChatGPT Work or a dot. Local Codex stdio
does not need this. Claude.ai uses a [different connection](remote.md#claude-clients).
The qualified path is ChatGPT -> OpenAI Secure MCP Tunnel -> tunnel-client on
the chosen host -> loopback HTTP /mcp -> headless Swarm -> provider-backed agent.

Contents: host and daemon; credentials; tunnel service; ChatGPT connection;
acceptance test; renewal and failure diagnosis.

## 1. Choose the host and prepare Swarm

Reuse the person's existing reachable Swarm first. A laptop must stay awake for
its agents and tunnel to remain available. Only provision a cloud machine when
requested; establish the account, location, size and recurring cost, preserve
the resource IDs privately, and verify the new host's SSH identity. A stale SSH
alias or changed host key is not permission to bypass host verification.

Our live qualification used a dedicated Linux amd64 VM, Docker, persistent
Swarm volumes and one disposable Git project. A 2-vCPU/4-GB VM handled this
small test; that is not a capacity recommendation for arbitrary swarms.
Only SSH needed inbound access, restricted to the operator. The tunnel needs
outbound HTTPS and local access to Swarm. No public Swarm port is needed.

Follow [local-deployment.md](local-deployment.md) for the headless image, mounts
and onboarding. If building source, use a reviewed revision and the repository's
required fast build gate. Record the commit and image ID. The qualified daemon
includes commit `0dfbc4c1d` (OAuth discovery absence on the scoped listener).
Select unique container/volume names. Keep config, data, cache and log volumes;
only the selected project is mounted at /project. Do not mount the Docker socket
or the owner's private socket into a tunnel process.

For host-side tunnel-client HTTP access, add BOTH options to the Docker example:

```sh
# Before the image name:
--publish 127.0.0.1:7783:7783
# After the image name (daemon argument):
--container-sdk-port=7783
```

The normal daemon's container-loopback listener is insufficient: Docker port
publishing alone does not enable this scoped listener. Retain the image's
permission-policy lock, dropped capabilities and no-new-privileges setting.
Use `--restart unless-stopped` for an authorized persistent deployment. If
replacing an existing container, stop the old writer before reusing its volumes.

Complete only missing owner, provider and workspace setup. For a ChatGPT plan,
use the [headless Codex device login](local-deployment.md#chatgptcodex-provider-sign-in-on-a-headless-host).
Preserve catalog-derived role defaults. Check `setup complete` and `setup status`.

## 2. Keep the three credentials separate

| Credential | Purpose | How the AI facilitates it |
| --- | --- | --- |
| Swarm provider sign-in/key | Runs the delegated model | Device sign-in or private stdin on the Swarm host |
| OpenAI tunnel runtime API key | Connects tunnel-client to OpenAI | User creates it in the intended Platform organization; hidden local entry, private transfer |
| Swarm scoped AI key | Lets the tunnel invoke Swarm tools | Owner CLI on the Swarm host; save output privately |

The operator creates a tunnel at [Platform tunnel settings](https://platform.openai.com/settings/organization/tunnels)
and associates the intended ChatGPT workspace and Platform organization. Creating
or managing it needs Tunnels Read + Manage; running/selecting it needs Read + Use.
Create the runtime key in that organization's [API keys settings](https://platform.openai.com/settings/organization/api-keys)
with Read + Use.
If that permission is unavailable, resolve organization/RBAC access instead of
substituting a Codex login token. Never request any key or password in chat.

Install the official client from [openai/tunnel-client releases](https://github.com/openai/tunnel-client/releases/latest),
select the correct host architecture and verify its published digest when
available. Record version and digest. Version 0.0.16 was exercised; check the
selected binary's help before applying these flags to another version.

Prepare a dedicated tunnel service account with an owned 0700 configuration
directory outside the project. Keep key/header files at 0600 and private across
transfer. It does not need Docker membership or access to daemon data. The
owner/operator, not the service account, performs key issuance.

Bash example on the daemon host, with variables set to the selected container and a
new private staging directory, shell tracing disabled:

```sh
(
set -euo pipefail
umask 077
set -o noclobber
docker exec "$SWARM_CONTAINER" swarmctl setup sdk-token \
  --name chatgpt-tunnel --ai-access write --expires-in-seconds 86400 \
  > "$PRIVATE_STAGING/grant-secret.json"
jq -er '.token | "Bearer " + .' "$PRIVATE_STAGING/grant-secret.json" \
  > "$PRIVATE_STAGING/swarm-authorization"
jq '{id}' "$PRIVATE_STAGING/grant-secret.json" > "$PRIVATE_STAGING/grant.json"
)
```

Run sequentially and check each exit status; retain uncertain issuance evidence
instead of blindly minting again. `grant-secret.json` contains the secret;
`grant.json` is the nonsecret revocation record. Privately install the header file
for the service account; keep the grant record with the operator. Save the
runtime API key through hidden terminal input or a secret manager, never argv,
history, project files, environment metadata or a plugin archive. Prepare the
hidden-input helper for the person: read from their terminal with echo disabled,
create an owned 0600 file in an owned 0700 directory without overwriting an
existing key, and print only success plus its nonsecret path. Give one command
to run, not instructions to write their own key-management script.

The one-day lifetime is a test choice, not automatic renewal. Choose an explicit
approved production lifetime/rotation arrangement. `write` permits delegation,
not permission approval or full administration. Do not replace it with `full`
to make a missing tool appear. All allowed tunnel callers share this Swarm
identity/grant; restrict the connection accordingly. This setup is not per-user
OAuth or a multi-tenant public service.

## 3. Start and persist the tunnel

If reusing an occupied tunnel ID, prepare the replacement while it is stopped.
Coordinate the switch with the existing service's operator: stop and verify the
old client is inactive before starting the replacement. Use one active client
for this workflow. Preserve the original service and state for rollback. After
a test, stop the trial client before restoring the original, and verify the
expected workspace through the ChatGPT connection. Leave the trial service
boot-disabled so it cannot reclaim the shared ID after a restart.

Set the following nonsecret variables to the actual ID, binary and private file
paths on the tunnel host. The authorization file contains the entire `Bearer ...`
value. The runtime-key file contains only the OpenAI key.

```sh
"$TUNNEL_CLIENT" run \
  --control-plane.tunnel-id "$TUNNEL_ID" \
  --control-plane.api-key "file:$RUNTIME_KEY_FILE" \
  --mcp.server-url http://127.0.0.1:7783/mcp \
  --mcp.extra-headers "Authorization: file:$SWARM_HEADER_FILE" \
  --mcp.discovery-extra-headers "Authorization: file:$SWARM_HEADER_FILE" \
  --health.listen-addr 127.0.0.1:18787 \
  --log.format json --log.level warn
```

Both header flags are needed: discovery also crosses Swarm's authentication
boundary. Use JSON logging with the warn level; the tested CLI rejected that
level with its default pretty format. Never print raw key/header files.

On a Linux server, create a systemd unit with these resolved arguments, its
dedicated User/Group, network-online ordering, Restart=on-failure, UMask=0077,
NoNewPrivileges=yes, PrivateTmp=yes, ProtectSystem=strict and ProtectHome=yes.
Grant writes only to its chosen service working directory; keep secrets readable
by that user. Order after Docker when Docker owns Swarm. Enable boot startup
when the person requested persistent operation. Record the service name and
grant expiry; a background terminal alone is not a persistent deployment.

Check local `/readyz` (plain text plus HTTP status) and `/health?details=true`
(JSON), with health access kept on loopback. Require HTTP 200, ready=true and
successful control-plane polling. A successful startup probe does not yet prove
ChatGPT discovery, inference, or a dot call.

## 4. Connect ChatGPT

In ChatGPT Plugins, add a custom MCP server, choose Connection = Tunnel and
select/paste the tunnel ID. For this service-held Swarm key setup, choose
Authentication = None: the tunnel controls access and injects the upstream key.
Do not paste the localhost URL into the public Server URL field. Install/enable
the resulting plugin and use it in the intended ChatGPT workspace and dot.

This creates the MCP connection only. Install the Swarm skills package separately
for onboarding/delegation instructions; a connection cannot fetch SKILL.md from
the daemon automatically. A skills-only package can guide a per-user connection
without bundling a personal tunnel ID. A public-directory MCP listing has a
separate public HTTPS requirement; a successful private test is not publication.

If UI automation is unavailable or denied, do the authorized server work and
give the person the exact remaining UI fields. Don't route around browser denial.

## 5. Prove the complete path

Discover the actual tools, then list workspaces and models through the new
ChatGPT connection. Ask the dot to start a Finder session on the selected
workspace to read an agreed harmless file, wait for completion, and return what
it read without edits. Don't give it the expected answer. Inspect that same
session's real tool results, final state and file/diff evidence. Keep the session
ID privately for follow-up and continue it after timeouts; don't duplicate starts.

The 2026-10-11 Linux test at daemon revision `0dfbc4c1d` completed this path: a
ChatGPT dot started Finder, which listed the test workspace, read its README,
and returned a finished result; the workspace was unchanged. A separate direct
MCP proof read survived container replacement. This does not qualify native
macOS/Windows, automatic wakeups, multi-user isolation, or large-agent scale.

## 6. Renewal, diagnosis and shutdown

- Swarm 401: distinguish an expired/wrong scoped key from tunnel-control-plane
  authentication. Record issuance time plus lifetime; restarting doesn't renew.
  Through owner execution, create a replacement with the approved scope/lifetime,
  install its header atomically with the service owner/mode preserved, restart,
  verify a real call, then revoke the previous grant by its recorded ID using
  `swarmctl setup revoke-sdk-token --id ...`. Keep the old grant until the new
  connection works; don't print either secret.
- Tunnel invisible: check target ChatGPT workspace association and Tunnels Use
  permission. Platform organization access alone isn't workspace association.
- Connection timeout just after a service switch: verify the intended client is
  the only active one and that control-plane polling has succeeded, then retry
  a read-only workspace listing. Verify its identity before delegation. Do not
  blindly retry a session-start request; inspect whether it already created work.
- OAuth discovery degraded or “missing resource”: with the same upstream key,
  probe both protected-resource metadata paths in [mcp-protocol.md](mcp-protocol.md).
  They must report absent metadata as 404. Older Swarm's 403 caused readiness
  failure. Deploy the fix preserving volumes; don't publish a port or remove auth.
- Some optional discovery probes (including server/discover) were unsupported in
  the live test. Judge standard initialization, tools/list, tools/call and the
  actual task, not isolated warning counts. Persistent failures remain failures.
- Agent waits for permission: a write key can't approve. Use authorized owner
  controls; keep the existing permission policy and report the pending decision.
- Report whether the host is left running and billed. On requested teardown,
  preserve needed work, stop/revoke this connection, remove only its dedicated
  resources and confirm any residual IP/storage charges. Don't delete volumes
  merely to troubleshoot or automatically remove an active testbench.

Official transport/UI reference: [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels).
Use [Swarm's MCP contract](mcp-protocol.md) for protocol details from the code.
