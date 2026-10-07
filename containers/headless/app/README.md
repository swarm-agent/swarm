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
curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/swarm-control/containers/headless/app/install.sh \
  | bash -s -- --relay https://swarm-relay.YOU.workers.dev
</copy>

`install.sh` installs Docker and Tailscale, prints one Tailscale login link to
approve the machine, turns on a firewall that admits only the tailnet (and
SSH until `--lock-ssh`), builds the runtime and this app from source
(`Dockerfile.local`), and serves the app with `tailscale serve` at
`https://NAME.TAILNET.ts.net`. It prints that URL and the unlock secret; no
provider key or other secret is passed to it. The app's guided setup then
covers owner, provider sign-in, models, a workspace and **Connect to Claude**,
which shows a pairing code for your relay (see `packages/swarm-relay`).
State lives in the usual named volumes plus `/var/lib/swarm-headless`. A
5-minute timer runs `install.sh update`, which rebuilds only when the branch
moves; `install.sh secret` prints the unlock secret again. `--relay` only
prefills the Connect step. `--name` sets the tailnet and machine name.

This is a source build of an unpublished candidate, not the qualified release
path below. Validated in a container with a local relay and a stand-in for
Tailscale Serve; a real server, Tailscale and a deployed relay are not yet
exercised by its checks.

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
