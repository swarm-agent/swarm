# Supported application image

One Linux amd64 container owns one durable daemon store. Node 22+ runs the trusted
application BFF as UID 10001 alongside the canonical Debian/glibc/FFF daemon.
Only the BFF uses `/var/lib/swarmd/local-transport/api.sock`. Never mount/export
that privileged socket or expose the daemon over a public listener.
`--container-sdk-port` is only the basic session boundary: it does not grant
apps, workers, sync, storage or publication APIs.

## One-line install on your own server (private, on your tailnet)

For a fresh Ubuntu 24.04 **x86_64** server you control, with Tailscale
MagicDNS and HTTPS certificates turned on for your tailnet. As root:

<copy label="Install">
curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/dev/containers/headless/app/install.sh \
  | bash -s -- --relay https://swarm-relay.YOU.workers.dev
</copy>

`install.sh` installs Docker, gVisor and Tailscale, prints one Tailscale login
link to approve the machine, turns on a firewall that admits only the tailnet
(and SSH until `--lock-ssh`), builds Swarm and this app from source
(`Dockerfile.host`, exported as files) and serves the app with
`tailscale serve` at `https://NAME.TAILNET.ts.net`; no provider key or other
secret is passed to it. On the first visit, from your own device on the
tailnet, you create your login (username, password, optional authenticator
code; see `examples/headless-app`). Setup then covers provider sign-in, models,
a workspace and **Connect to Claude**, which shows a pairing code for your
relay (see `packages/swarm-relay`).

**Layout.** Swarm runs on the server itself as two systemd services under a
dedicated `swarm` user: `swarm-headless` (the daemon, `--sandbox=required`)
and `swarm-headless-app` (this app on 127.0.0.1:8443). Binaries live in
`/opt/swarm/current`; Swarm's state in `/etc/swarmd`, `/var/lib/swarmd`,
`/var/cache/swarmd` and `/var/log/swarmd` (mode 0700, systemd state
directories); agent worktrees under `/var/lib/swarm`; projects in
`/var/lib/swarm-headless/project`. `swarmctl` (in `/usr/local/bin`) runs as
the `swarm` user, e.g. `sudo swarmctl remote approve CODE`. A 5-minute timer
runs `install.sh update`, which rebuilds only when the branch moves.
`install.sh reset-login` deletes the login (not Swarm or your work).
`install.sh reinstall` wipes Swarm (services, binaries, sandboxes, state:
owner, provider sign-in, AI keys, relay pairing, agent worktrees) and installs
it again with the saved `--relay`, `--name` and `--ref`; `install.sh uninstall`
only removes it. Both keep Docker, gVisor, Tailscale, ufw and the project
folder (unless `--delete-projects`), and remove the earlier
Swarm-in-a-container layout if present.

**Agent sandboxes.** Swarm keeps its keys, login and local socket to itself.
Agents' commands, and Swarm's own Git on their projects, run in one sandbox
per project (`swarmd/internal/sandbox`, image `containers/sandbox`): gVisor
when it runs correctly on the machine (else Docker's default runtime), all
capabilities dropped, no-new-privileges, the `swarm` user's uid, memory and
process limits, only that project and its worktrees mounted at the same paths,
nothing from Swarm's environment. The sandbox network (`swarm-sandbox`, bridge
`br-swarm-sbx`, rules from `containers/sandbox/firewall.sh`, re-applied at
boot by `swarm-sandbox-firewall`) reaches the internet but drops new
connections to this server, the tailnet, private ranges, loopback and cloud
metadata. File tools refuse Swarm's storage and credential paths outright.
`install.sh check-isolation` proves the network rules from inside a sandbox
(the installer runs it at the end); `install.sh check-sandbox` shows each
running sandbox's runtime, user, capabilities, limits and mounts.

**Agent permissions.** Setup asks how agents work: **Ask me first**
(default; agents pause before commands and file changes) or **On their own**
(the owner-only bypass setting: no ordinary prompts; plans to accept, agent
questions and hard denials still stop). No sandbox, no autonomy: bypass takes
effect only while the agent sandbox is active, and turning it on is refused
otherwise.

It also serves Swarm's **AI gateway** (Swarm Control MCP on the scoped-token
listener, bound to 127.0.0.1:7783) on the tailnet at
`https://NAME.TAILNET.ts.net:8444/mcp`; `tailscale serve` is its only way in.
It answers only to **AI keys** created in the app under **AI access over
Tailscale** (none exist at first): read only, or read and write (start
sessions, send messages, stop runs); never approve tool calls or manage
workers, limits or models, and never any route but `/mcp`. The page shows the
key once, lists keys with last use, revokes them, and shows the Tailscale
access rule and client settings. `--no-ai-access` skips the gateway. Clients:
Claude Code on a tailnet device (`claude mcp add --transport http`), or
`packages/swarm-fleet` for Claude Code on the web and routines.

This is a source build of an unpublished candidate, not the qualified release
path below. The sandbox is checked with real Docker by
`scripts/test-sandbox.sh` (a positive and negative suite, and a red-team run
through a real daemon); gVisor, a real Tailscale interface and a deployed
relay are exercised only on a real server.

## Build and launch

Obtain `SWARM_RUNTIME=ghcr.io/swarm-agent/swarm-headless@sha256:<digest>` from the
qualified GitHub release manifest. No real digest is supplied here: publication
must happen first. The recipe rejects mutable tags; a syntactically valid digest
is not proof of qualification. Match the SDK version to that release.

`Dockerfile` builds the checked-in Workshop BFF (see `examples/headless-app`).
`Dockerfile.consumer` is reusable without daemon source: copy it, `entrypoint.sh`
and `supervisor.mjs` into your application project. Supply your own `server.mjs`,
locked package.json/package-lock.json with the matching published `@swarm-agent/sdk`,
and a .dockerignore excluding credentials, logs, node_modules and .git.
Do not copy secrets into the image or pass them as build arguments.

<copy label="Build standalone application (from app project)">
docker build --platform linux/amd64 --build-arg SWARM_RUNTIME="$SWARM_RUNTIME" -f Dockerfile.consumer -t swarm-app:local .
docker run --name swarm-app --stop-timeout=15 --restart=no \
  --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=256 \
  --memory=4g --cpus=2 -p 127.0.0.1:8443:8443 \
  -v app-config:/etc/swarmd -v app-data:/var/lib/swarmd \
  -v app-cache:/var/cache/swarmd -v app-logs:/var/log/swarmd \
  -v app-project:/project swarm-app:local
</copy>

Never share these volumes with a second running container. Stop the previous
writer before upgrades; back up config/data/project consistently while stopped.
S3-compatible object storage is for explicit application exports, not a shared
Pebble filesystem, worker executor, or automatic recovery authority.

The supervisor waits at most 30 seconds for private `/readyz` before starting the
BFF. Either child's exit fails the container (even exit zero); TERM/INT stops both
process groups, with KILL after 10 seconds. Readiness is daemon readiness, not
provider credentials/inference qualification. Logs stay in the private log volume.

## Application trust boundary and bot topology

Reuse Workshop's `boundary.mjs` and `operations.mjs` patterns: exact loopback
Host/Origin, login, HttpOnly SameSite cookies, CSRF, bounded requests/streams,
operation allowlist and workspace/session ownership checks. It is not a generic
proxy. A custom BFF must implement its own reviewed auth and allowlist; the image
alone cannot supply application authorization. Do not forward arbitrary SDK
paths, raw credentials or socket access to browsers or webhook callers.

For a social/support bot: an authenticated application request enters a narrowly
allowlisted BFF operation, which reads canonical application conversations or an
already-approved worker. Deploy/configure workers only in Swarm Orchestrate;
keep publication review as an explicit authorized action. Observe real V3 session
streaming with `client.realtime.watchSession`, and repair from durable replay.
`packages/sdk/examples/social-media-worker.ts` reads an exact worker/run/output
and returns the backend receipt unchanged. It neither deploys nor approves,
generates no canned content/progress, and does not treat object upload or a
`published` label as proof of an external post. No private bot code is required.

Cloud Run services/jobs and legacy Compute Engine provisioning helpers now fail
with migration errors rather than constructing incompatible/public daemons.
This does not qualify any cloud deployment, live provider run or external post.
