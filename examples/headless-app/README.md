# Workshop: a real SDK-powered headless app

Custom browser UI → allowlisted Node BFF → `@swarm-agent/sdk` → private swarmd Unix
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
1. **Create your login**: a username and a password of at least 12 characters
   (save both in your password manager). This account also becomes the Swarm
   owner. No workstation identity, environment credentials or Swarm account are
   imported.
2. **Two-factor (recommended)**: scan the QR code with an authenticator, or in
   1Password add a one-time password field with the setup key; enter one code
   to turn it on. Sign-in then asks for a current code.
3. **AI provider**: **Sign in with ChatGPT** shows a device code and finishes by
   itself; or open **Use an API key instead**.
4. **Models**: **Use recommended models** (the daemon's verified defaults for
   connected providers); change them later in Settings.
5. **Workspace**: type a name; the app creates an empty Git repository with one
   initial commit under `/project` and registers it.
6. **Agents**: **Ask me first** (default) or **On their own** (no ordinary
   permission prompts; see `containers/headless/app/README.md` for the
   container isolation this relies on). Changeable in Settings.
7. **Connect to Claude**: see below. Then chat in a workspace from **Home**:
   approve one exact pending call or deny it; stop targets the active run.

## Guided setup

After sign-in, setup shows one step at a time (provider, models, workspace, agents,
Connect to Claude), derived from canonical state (`setup` operation); a
returning owner with nothing left goes straight to Home. **Connect to Claude** initializes and enables the machine's relay connection
(`APP_RELAY_URL` and `APP_DEVICE_NAME` prefill it) with the ceiling you tick,
then shows the pairing code to give Claude; it refreshes on a bounded timer
only while connecting. AI client authorization requests appear there to
approve or deny. Relay administration uses the owner-only `client.remote`
SDK namespace on the private socket and never returns key material.

## Sign-in API (reusable)

`accounts.mjs` is the owner login: one account per installation in
`/etc/swarmd/headless-app/account.json` (mode 0600) holding only a salted
scrypt hash and, once confirmed, the TOTP key (RFC 6238, SHA-1, 6 digits, 30 s,
one step of drift, each code usable once). Ten failures in a minute lock
sign-in for five minutes; a wrong password never consumes the current code, and
every failure gets the same message. `boundary.mjs` then issues the browser
session (HttpOnly SameSite=Strict cookie, CSRF token, 12 hours, at most 8).
Use both from your own BFF, or call these routes (JSON POST, exact Origin):

| Route | Signed in | Body | Result |
|---|---|---|---|
| `/auth/status` | no | | `registered`, `two_factor`, `can_register` |
| `/auth/register` | no | `username`, `password` | first owner only; sets the cookie, returns `csrf` |
| `/auth/login` | no | `username`, `password`, `code` | sets the cookie, returns `csrf` |
| `/auth/logout` | yes | | |
| `/auth/me` | yes | | `username`, `two_factor`, `claimed_by` |
| `/auth/2fa/start` | yes | | `secret`, `uri` (otpauth), `qr` (module grid) |
| `/auth/2fa/confirm` | yes | `code` | turns 2FA on |
| `/auth/2fa/disable` | yes | `password`, `code` | turns 2FA off |
| `/auth/password` | yes | `current`, `next`, `code` | |

Changing the password or 2FA signs out every other browser. Behind Tailscale
Serve, `/auth/register` requires Serve's `Tailscale-User-Login` header (Serve
sets it for tailnet users and strips any copy a client sends) and records it as
`claimed_by`; on a loopback origin only the machine itself can reach the page.
Forgotten password or lost authenticator: `install.sh reset-login` on the host
(or delete `account.json` and restart the container), then create a new login.

## Private access from your tailnet

To open the app from your own devices, keep the loopback publication above and
let [Tailscale Serve](https://tailscale.com/kb/1312/serve) provide HTTPS on the
host. Set `APP_ORIGIN` to the exact Serve origin (`https://NAME.TAILNET.ts.net`,
no port or path) with `-e APP_ORIGIN=...` on `docker run`, then on the host run
`tailscale serve --bg 8443`. Tailscale terminates TLS, so the container listener
stays plain HTTP on host loopback and no certificate files are needed. The app
rejects any other non-loopback origin, and requests whose Host is not the Serve
name. Login, HttpOnly `__Host-` cookie and CSRF rules are unchanged; the Serve
name is reachable only from your tailnet. Do not use Tailscale Funnel.

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
provider credentials, settings, workspaces, sessions and the login
persist. Browser sessions are memory-only and require signing in again after a
restart. Never remove these volumes unless intentionally destroying the
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
- 64 KiB request bodies, 8 browser sessions, 2 streams per session, 16 concurrent
  requests, 32 TCP connections, bounded stream queue/frame sizes, and sign-in lockout.
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
