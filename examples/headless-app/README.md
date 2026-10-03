# Workshop: a real SDK-powered headless app

Custom browser UI → allowlisted Node BFF → `@swarmagent/sdk` → private swarmd Unix
socket. Both processes run as UID 10001 in one container. This is not Desktop,
a mock assistant, a generic daemon proxy, or a worker-deployment surface.

## One local launch path

Prerequisites: Linux amd64 Docker/BuildKit, network for build dependencies and
providers, and five dedicated named volumes. Run from repository root. These
commands build the checked-in SDK/BFF on a qualified release daemon image.
Set SWARM_RUNTIME to ghcr.io/swarm-agent/swarm-headless@sha256:<digest> from
that release manifest. Publication must precede using this recipe.
For Podman replace `docker` with `podman` and use the same qualified digest.
Use `localhost/swarm-workshop:local` when running with Podman.
See [standalone application recipe](../../containers/headless/app/README.md).

<copy label="Build and launch">
docker build --platform linux/amd64 --build-arg SWARM_RUNTIME="$SWARM_RUNTIME" -f containers/headless/app/Dockerfile -t swarm-workshop:local .
docker run -d --name swarm-workshop --restart=no --stop-timeout=15 \
  --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=256 \
  --memory=4g --cpus=2 \
  -p 127.0.0.1:8443:8443 \
  -v workshop-config:/etc/swarmd -v workshop-data:/var/lib/swarmd \
  -v workshop-cache:/var/cache/swarmd -v workshop-logs:/var/log/swarmd \
  -v workshop-project:/project swarm-workshop:local
</copy>

Open **http://127.0.0.1:8443** (not HTTPS or localhost). The default is loopback-only
HTTP: no certificate warning, browser bypass, or trust-store installation. Traffic
stays on this machine; this is not a transport for LAN or remote access. Browsers
may label HTTP as “Not secure”; no certificate interstitial is involved.
The host-only login cookie remains HttpOnly and SameSite=Strict, with exact
Host/Origin checks and a separate CSRF token for privileged requests.
The following explicit secret read is for the operator's terminal only: do not
paste it into chat, screenshots, issues or logs.

<copy label="Retrieve local login secret">
docker exec swarm-workshop cat /etc/swarmd/headless-app/login-secret
</copy>

1. Unlock, then explicitly create this installation's owner/name. No workstation
   identity, environment credentials or Swarm account are imported.
2. Choose a provider and its advertised credential type, then enter an API key;
   or choose **Sign in to Codex**, open the device URL, enter the code, and click
   **Refresh sign-in status**. Alternatively choose browser/manual sign-in and
   paste the final callback URL into the password field. No callback port is
   exposed; automatic browser callback capture is not offered in this container.
3. Select models from the daemon's catalog and save canonical role assignments.
   Thinking, service-tier and context options come from the selected record.
   Stored credentials are not proof of verified inference; readiness/default
   errors are displayed. No provider/model fallback is hardcoded.
4. Create a folder inside `/project`, inspect Git, explicitly approve initialization,
   and register. If content needs a baseline, review the exact file list, select
   files, approve omissions, and commit that reviewed digest. No automatic commit.
   Partial failures leave the folder visible in the creation form for retry;
   no alternate endpoint silently bypasses repository prerequisites.
5. Select the workspace, create a session, and send a message. Approve one exact
   pending call or deny it; stop targets the current active run. No persistent
   permission rule or bypass is created.

## Streaming and persistence

`server.mjs` uses `sdk.realtime.watchSession`; it does not implement V3 transport,
cursors, replay, reducers or polling. The browser consumes CSRF-protected POST
NDJSON snapshots, replacing messages keyed by ID and rendering live drafts
separately. SDK reconnect state is visible. A disconnected app stream requires
**Reconnect**, which starts a fresh SDK bootstrap. The view is the latest 200
messages/events, not complete transcript pagination. Send retry retains the same
request ID until success or edited text. Lists refresh on explicit actions only.

<copy label="Stop and recreate without losing state">
docker stop swarm-workshop
docker rm swarm-workshop
</copy>

Repeat the same `docker run` command with the **same five named volumes**. Owner,
provider credentials, settings, workspaces, sessions and login
secret persist. Browser sessions are memory-only and require unlocking after a
restart/reload. Never remove these volumes unless intentionally destroying the
installation. Back up them together while stopped. The data volume contains the
canonical daemon state and its private socket; never attach it to another running
container or export/mount that socket. It is not an SDK access volume.

## Security boundaries and limits

- Only port 8443 is published, explicitly on host **127.0.0.1**. Never use wildcard
  publishing, host networking, Docker socket mounts, or workstation source mounts.
  The BFF must listen on the container interface for Docker NAT; the daemon's
  existing session-only container listener is unchanged and not enabled here.
- HTTP is permitted only with an exact IPv4 loopback origin. Never publish it to
  a LAN/public interface. Optional explicit `APP_ORIGIN=https://127.0.0.1:8443`
  requires a trusted `tls.key`/`tls.crt` in the config volume; certificates are
  never generated automatically. HTTPS keeps the Secure `__Host-` cookie.
- Exact Host/Origin and CSRF checks apply to every POST, including streaming.
  All browser WebSocket upgrades are rejected. Static GETs do not expose state.
  External links may open only the public `/` login shell as a top-level document;
  cross-site embedded pages, subresources and API requests remain rejected.
  Credentials never go to browser storage; user-provided API keys are cleared
  immediately after submission. SDK exception bodies are not reflected or logged.
- 64 KiB request bodies, 8 app logins, 2 streams per login, 16 concurrent requests,
  32 TCP connections, bounded stream queue/frame sizes, and login throttling.
- This is a **single-owner development installation**, not tenant isolation.
  Tool execution shares the daemon/BFF UID and can access installation state;
  review permissions carefully. Path checks reject resolved paths outside the
  project volume, but do not sandbox malicious concurrent in-container code.
- Daemon logs remain in the private log volume. Do not publish them unreviewed.
  There is no generated telemetry or live-provider claim in this example.

## Focused validation

Prerequisites: Node 22+, SDK dependencies and build. Run from repository root:

<copy label="Focused checks">
npm --prefix packages/sdk ci --ignore-scripts --no-audit --no-fund
npm --prefix packages/sdk run build
npm --prefix examples/headless-app ci --ignore-scripts --no-audit --no-fund
npm --prefix examples/headless-app test
bash -n containers/headless/app/entrypoint.sh
</copy>

Boundary tests exercise HTTP rejection and no privileged side effects, bounded
requests, redacted errors, symlink/traversal rejection and exact permission IDs.
The UI renderer test exercises replacement (no duplicate messages), draft removal,
text-only untrusted content and active-run stop state. These are hermetic unit
fixtures, **not live AI or container benchmarks**.

Parent validation must build/run the candidate, check image UID/mounts/loopback
publishing and shutdown, inspect actual UI pixels, then exercise real owner setup,
API-key/Codex login, catalog assignment, workspace/session creation, provider reply,
stream interruption/reconnect, exact permission decisions, stop, and restart
persistence. Enter live credentials only in the UI. **Not run; parent validation
required.** No deployment or visual inspection is claimed by this source handoff.
