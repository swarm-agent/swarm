# Supported application image

One Linux amd64 container owns one durable daemon store. Node 22+ runs the trusted
application BFF as UID 10001 alongside the canonical Debian/glibc/FFF daemon.
Only the BFF uses `/var/lib/swarmd/local-transport/api.sock`. Never mount/export
that privileged socket or expose the daemon over a public listener.
`--container-sdk-port` is only the basic session boundary: it does not grant
apps, workers, sync, storage or publication APIs.

## One-line install on your machine (local; Tailscale optional)

For Ubuntu 24.04 **x86_64**: a server or your own PC. It works on the one
machine with your own AI (Claude Code or Codex) and needs no Tailscale.
From your own account (the AI connection is made for you):

<copy label="Install">
curl -fsSL https://swarmagent.dev/install | sudo bash
</copy>

`install.sh` downloads the newest release from GitHub
(`swarm-vX.Y.Z-linux-amd64.tar.gz`, built and signed by the release pipeline),
checks it against its published `.sha256` and that it was built from the commit
GitHub names, unpacks its `headless/` package (the daemon, `swarmctl`, this app
and its Node) and starts them. Nothing is compiled and no Docker is installed.
Host dependencies installed through apt are Git, curl, ca-certificates, gnupg,
iproute2, jq, acl, and ffmpeg (including ffprobe). Media transcription runs on
the host; fresh installs and `install.sh update` install ffmpeg when either
media tool is missing from the daemon's PATH, which includes `/usr/bin`.

`--environments on` (or `install.sh environments on` later) installs Docker and
gVisor for environments and the sandbox; Docker from snap cannot run them, so
the installer then asks you to remove it. `--ref BRANCH` builds that branch
from source with Docker instead (`Dockerfile.host`), for development machines;
installs made before downloads keep doing that. `--release-file ARCHIVE`
installs a release archive already on the machine. Everything listens on
127.0.0.1 only and no firewall rule is opened: the setup page on
`http://127.0.0.1:8443` and Swarm's MCP (Swarm Control) on
`http://127.0.0.1:7783/mcp`, which answers only to AI keys. On a server, reach
the page with `ssh -N -L 8443:127.0.0.1:8443 USER@SERVER` and open
`http://127.0.0.1:8443` on your computer (the installer prints the exact line).
No provider key or other secret is passed to the installer. On the first visit
you create your login (username, password, optional authenticator code; see
`examples/headless-app`) and Swarm's owner; setup then covers provider
sign-in, models, a workspace and **Connect to Claude**, which shows a pairing
code for your relay (`--relay URL`; see `packages/swarm-relay`).

**Your AI on this machine.** At the end of the install (run through sudo, or
with `--for-user NAME`) the installer creates Swarm's owner for that person
(the setup page keeps it) and mints an AI key (write: start and steer work,
deploy workers, merge results) through `swarmctl setup sdk-token --ai-access`,
writes it to their `~/.config/swarm/ai-key` (mode 0600, owned by them; never
printed) and prints the connect lines.
`sudo bash /var/lib/swarm-headless/install.sh connect-ai`
does the same later (`--access full` also approves tool calls and manages
models, providers and workers); running it again replaces the key and revokes
the old one.

**An app on this machine** (a console or a frontend you build) gets its own key
with `install.sh connect-app NAME [--access read|write|full]`, in
`~/.config/swarm/apps/NAME.key`; it never replaces the AI key. The path for
frontends, discovery (`GET /v3/hello`, no key) and installer progress lines
(`--progress=json`) is in [docs/frontends.md](../../../docs/frontends.md).

Register once as yourself for every later session:

```sh
codex mcp add swarm -- swarm-mcp
claude mcp add --scope user --transport stdio swarm -- swarm-mcp
```

The installed `swarm-mcp` uses the release's bundled Node and reads your private
AI key or saved plugin connection; no per-session `SWARM_AI_KEY` is needed.
Then tell your AI: set up Swarm.

For older releases without the bridge only, use the URL fallback:
`codex mcp add swarm --url http://127.0.0.1:7783/mcp --bearer-token-env-var SWARM_AI_KEY`
and start each session with `SWARM_AI_KEY="$(cat ~/.config/swarm/ai-key)" codex`.
For Claude Code: `claude mcp add --scope user --transport http swarm http://127.0.0.1:7783/mcp --header "Authorization: Bearer $(cat ~/.config/swarm/ai-key)"`.

**Keys by script.** `install.sh key PROVIDER` sets the first provider API key
from stdin (or a hidden prompt) through `swarmctl setup credential
--api-key-stdin`; it never appears on a command line or the screen.
`sudo swarmctl secret set --name NAME --hosts HOST` creates or updates a secret
and asks the person for its value at a hidden prompt (piped stdin also works).
New secrets need `--hosts`; existing secrets accept omitted or matching hosts,
never a changed allowlist. After asking the person how long,
`sudo swarmctl secret grant --name NAME --workspace FOLDER --days N` (or `--hours N`)
lets that workspace's agents use it. The person enters the value, never the AI;
never ask for a secret or key in chat. `install.sh secret set NAME --hosts HOST`
and `install.sh secret grant NAME FOLDER --days N` still work; set passes through
to swarmctl with stdin connected.

**Claude Code as the provider.** Swarm can run its agents through the Claude
Code CLI with your Claude plan. It runs `claude` as its own `swarm` user, which
needs its own install and sign-in; Swarm never reads, copies or stores Claude
credentials, and your own Claude login is not copied. In a terminal on the
machine:

<copy label="Claude Code for Swarm">
sudo bash /var/lib/swarm-headless/install.sh claude-code
</copy>

It finds or installs the CLI for that user (Anthropic's installer, into
`/var/lib/swarm/.local/bin`; an older one than Swarm needs is updated), sets
`SWARM_CLAUDE_CODE_CLI_PATH` with `daemon-env` and starts `claude auth login`
as that user: open the link it shows and finish in your browser. Without a
terminal it does the rest and stops at the sign-in. Running it again is safe
and reports what is set up. Then your AI makes Claude Code the active provider
through MCP (`swarm_connect_claude_code`, action `status`, then `activate`); no
setup-page form or owner password is needed. Details:
[docs/checkpoints/claude-code-provider.md](../../../docs/checkpoints/claude-code-provider.md).

**Tailscale (optional, for your other devices).** `--tailnet` at install, or
`install.sh tailnet on` later, installs Tailscale and prints one login link
(or joins with `TS_AUTHKEY` from the environment; `--tag tag:swarm`), turns on
ufw admitting only the tailnet (and SSH until `--lock-ssh`), and serves the
setup page at `https://NAME.TAILNET.ts.net` (it then answers only there, also
from this machine) and the MCP at `https://NAME.TAILNET.ts.net:8444/mcp`; your
MagicDNS and HTTPS certificates must be on. See [TAILNET.md](TAILNET.md).
`install.sh tailnet off` goes back to loopback only. Installs from before
Tailscale was optional keep their tailnet setup on update.

**Layout.** Swarm runs on the machine itself as two systemd services under a
dedicated `swarm` user: `swarm-headless` (the daemon; `--sandbox=off` by default)
and `swarm-headless-app` (this app on 127.0.0.1:8443). Binaries live in
`/opt/swarm/current`; Swarm's state in `/etc/swarmd`, `/var/lib/swarmd`,
`/var/cache/swarmd` and `/var/log/swarmd` (mode 0700, systemd state
directories); agent worktrees under `/var/lib/swarm`; projects in
`/var/lib/swarm-headless/project`. `swarmctl` (in `/usr/local/bin`) runs as
the `swarm` user, e.g. `sudo swarmctl remote approve CODE`.
**Updates.** Automatic updates are opt-in: `install.sh auto-update on` (or
`--auto-update` at install) runs `install.sh update --automatic` every 5
minutes; `install.sh update` can also be run by hand. Automatic updates are
idle-only: while anything runs, the built commit waits (state `waiting`,
`busy (N running)`) and the next timer run tries again; no admission drain is
started, so running work can still start the runs it needs to finish. A
person's `install.sh update` drains and waits (see below). An update takes the newest commit of the branch only when
it contains the running build (never a downgrade) and passed its checks on
GitHub: by default Swarm's AI review (`ai-review`, accepted only from the
`swarm-go-ai-review` GitHub App); a failed review blocks the commit and a
missing one waits. It builds the commit next to the running build while work
goes on and restarts Swarm into it only when nothing runs (no agent run,
worker run or environment build: `GET /v3/box/busy`). If the new build does
not come up within two minutes, Swarm goes back to the build before and never
retries that commit. The machine summary shows the state (`machine.update`)
and what runs (`busy`); `auto-update on --hook PATH` also tells a root-owned
command when a commit is installed, blocked or rolled back, and `--checks`
names other checks (`none` for a fork without them).
**Disk.** After a build comes up (an update, or `install.sh up`), the installer
removes older releases under `/opt/swarm/releases` (with their
`swarm-sandbox:COMMIT` images when environments are on), keeping the running
build, the one before it (for rollback) and a build waiting to start; a source
install also trims Docker's build cache to `BUILD_CACHE_MAX` in `install.env`
(default `8gb`, `none` keeps it all). Nothing
is removed after a failed or rolled-back update. A trimmed cache can make the
next build start from scratch and take longer; the running build is not
affected.
**Channels.** The installer follows `main`, the released build; `--ref dev`
follows development, which changes several times a day.
`install.sh channel` shows the branch and `install.sh channel dev` (or `main`)
switches it without reinstalling; the switch takes effect at the next update
once that branch contains the running build.
**Daemon settings.** `install.sh daemon-env set NAME` reads a value from stdin
into a root-only file the daemon's unit loads, then restarts Swarm;
`daemon-env list` shows names only and `daemon-env unset NAME` removes one.
Approved X posts (deliverables with the `publish_x_post` action) use
`TWITTER_API_KEY`, `TWITTER_API_SECRET`, `TWITTER_ACCESS_TOKEN`,
`TWITTER_ACCESS_TOKEN_SECRET`, `TWITTER_ACCOUNT` and
`TWITTER_ACCOUNT_SCOPE_ID` from there; agents never see them.
`install.sh dev-sync /path/to/checkout` builds the committed HEAD of a Swarm
checkout on the machine instead (for testing before a push); the timer then
leaves it alone until `install.sh dev-sync off`.
`install.sh reset-login` deletes the login (not Swarm or your work).
`install.sh reinstall` wipes Swarm (services, binaries, sandboxes, state:
owner, provider sign-in, AI keys, relay pairing, agent worktrees) and installs
it again with the saved `--relay`, `--name` and `--ref`; `install.sh uninstall`
only removes it. Both keep Docker, gVisor, Tailscale, ufw and the project
folder (unless `--delete-projects`), and remove the earlier
Swarm-in-a-container layout if present.
**Workspaces and projects.** Swarm runs as the `swarm` user with every home
folder hidden, so it cannot see a person's folders until they are made visible
from outside: `sudo install.sh workspace add ~/my-app` (run by the person, or
by the AI setting the machine up; `--for-user NAME` when not run through sudo)
binds exactly that folder, a Git repository or not, owned by that person, into
the service at its real path, gives Swarm's user access to it
(the owner keeps full rights on what Swarm creates) and mounts it into the
running Swarm at once (`systemctl bind`), so Swarm and every agent sandbox it
starts afterwards see it without a restart. The unit drop-in keeps it there
after later restarts. Only when the live mount is impossible does it say why
and restart Swarm once nothing runs; the first home folder always takes that
restart, because the running Swarm still has every home hidden
(`ProtectHome=yes`) until it starts with the drop-in's `ProtectHome=tmpfs`.
`workspace remove` unmounts the folder the
same way.
Swarm itself never changes the machine. That folder's agent sandbox runs as its
owner (with Swarm's group), so what agents create stays the person's, while
Swarm keeps running as `swarm`. Then register the folder in place
(`swarm_add_workspace`, nothing copied), group folders into a project
(`swarm_create_project`) so Swarm writes its `project.md`, and work through
the project's orchestrator once it is ready. Worker jobs and reviewers get
independent full clones in the managed checkout directory. These use no shared
object alternates or hardlinks, so job commits and source garbage collection
are independent. Desktop and plan sessions still use worktrees. Never point work at a freshly
initialised, empty repository in place of their code; `swarm_create_workspace`
is only for a new project started from scratch. System folders, settings
folders (any `.name` folder), Swarm's storage, the whole home folder and
other people's folders are refused. `workspace remove PATH` reverses `add`;
`workspace list` shows the folders.

**Environments and the sandbox (optional, need Docker).** Swarm keeps its
keys, login and local socket to itself. By default agents run their commands on
the machine as Swarm's user. A folder with an environment (`swarm_set_environment`
with `max_leases`) runs its agents' bash in leased containers of it, with its
secrets as stand-ins; with `install.sh sandbox on`, agents' commands, and
Swarm's own Git on their projects, run in one sandbox per project (`swarmd/internal/sandbox`, image `containers/sandbox`): gVisor
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

**Secrets agents can use but never see** (on by default; `--secrets-gateway
off` or `install.sh secrets-gateway off` turns it off). The gateway runs on the
sandbox bridge and only its port is open to sandboxes. The owner then creates a
secret with the exact websites it may go to using
`sudo swarmctl secret set --name NAME --hosts HOST`, typing its value at the
hidden prompt. After asking the person how long, use
`sudo swarmctl secret grant --name NAME --workspace FOLDER --days N` (or `--hours N`)
to grant it for a limited time. The value is entered by the person, never the AI;
`install.sh secret set|grant` still works over the same local socket. A granted project's sandbox sees only a stand-in
(`NAME=swarm-secret://NAME`); the gateway swaps in the real value on requests
to the allowed websites, scrubs it from responses and logs each use (`swarmctl secret uses`).

**Job delivery.** A job ends when its agent has built, tested and committed its
work; nothing is delivered until `swarm_deliver_job` asks.
`install.sh workspace publish PATH COMMAND` makes that delivery a pull request.
`--sync` starts jobs from the forge's latest base. `publish PATH --off` restores
local merges.

**Agent permissions.** Jobs normally run without tool-approval prompts: the installer
starts Swarm with `--bypass-permissions`, sandbox or not, so agents run without
ordinary prompts (plans to accept, agent questions and hard denials still
stop): a headless machine has nobody to answer a prompt.
`--ask-permissions` at install, or `install.sh permissions ask`, starts it with
`--bypass-permissions=false` instead (agents pause before commands and file
changes); `permissions auto` goes back. The installer's choice applies at every
start of Swarm, so a different choice on the setup page lasts until the next
restart.

Swarm's **AI gateway** (Swarm Control MCP on the scoped-token listener) is
`http://127.0.0.1:7783/mcp` on the machine and, with the tailnet,
`https://NAME.TAILNET.ts.net:8444/mcp` through `tailscale serve`.
It answers only to **AI keys** (from `connect-ai`, or created in the app
under **AI access**; none exist at first): read only, or read and write (start
sessions, send messages, stop runs, and start the person's ChatGPT sign-in,
which they finish in their own browser); those never approve tool calls or
manage workers, limits or models. A **full** key, minted only by the owner
(`connect-ai --access full`, `swarmctl setup sdk-token --ai-access full` or the
SDK), also approves tool calls and manages the box (models, workers, limits, custom
agents, client app keys). No AI key reaches any route but
`/mcp`. The page shows the
key once, lists keys with last use, revokes them, and shows the Tailscale
access rule and client settings. `--no-ai-access` skips the gateway. Clients:
Claude Code or Codex on the machine (`connect-ai`), Claude Code on a tailnet
device (`claude mcp add --transport http`), or `packages/swarm-fleet` for
Claude Code on the web and routines.

To watch the machine from a dashboard, mint a **monitor key**:
`sudo -u swarm swarmctl setup sdk-token --monitor --name dashboard
> monitor.json` (a private file; the key is never printed). Like every key it
works while it is used and stops after 90 days unused. It reads `GET /v3/box/summary` (is the machine all right, does
anything need a person) and `GET /v3/signals` (the event feed) on the same
gateway and nothing else: no sessions, no setup. A key with `signals:write`
lets a source on the machine, such as a host security monitor, report
`external.*` signals with `POST /v3/signals`.

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

### CPU contention controls

Restart after changing these keys in the daemon startup config (`swarm.conf`):

- `worker_max_concurrent_runs`: optional limit on concurrent agents; 0 (the
  default) is no limit. Sandboxes have no default memory or process cap either.
- Agents have no default CPU quota or command slot queue. `sandbox_cpus` remains
  available only as an explicit per-container override; empty means unrestricted.
- `sandbox_cgroup_parent`: optional Docker cgroup parent. The headless installer
  supplies `--sandbox-cgroup-parent=swarm-sandbox.slice` on install and update.

The installer gives agent containers a lower-weight slice (CPUWeight/IOWeight 25), versus daemon weights 1000,
and retains `ProtectControlGroups=yes`. Install/update detects Docker's cgroup
 driver: systemd uses the slice; cgroupfs explicitly clears the parent override
and uses per-container CPU shares (256 versus daemon-command containers' 2048).
Unknown drivers or failed detection stop installation rather than guessing.
Outside this installer, containers still receive lower CPU shares (256).
Weights only affect contention; idle CPU remains available to agents.

Ordinary daemon Git runs in a separate confined per-project container (CPU shares
2048), outside any explicit agent quota; reviewer Git retains its restricted read-only
container. Worktree Git command budgets start after sandbox acquisition. Cold-start
and engine failure can still fail explicitly; priority is not a real-time guarantee.

### Optional idle auto-off (cloud VMs)

`sudo bash install.sh idle-off 30` installs a root systemd timer that powers off
this machine after 30 continuous minutes of verified idle. It is off by default;
`sudo bash install.sh idle-off off` disables it. `install.sh status` shows the
configured interval and timer state. Use the saved installer file, not a pipe.

The timer checks `/v3/box/busy`, including follow-ups and reviews.
An unreachable daemon, malformed response or failed pre-shutdown signal resets
the idle interval; it never authorizes shutdown. The installer sends an
`external.idle_off` signal through the daemon before root invokes systemd poweroff.
The daemon never powers off its host. A stopped cloud VM keeps its disk; start
it in your cloud console to resume. Disk/storage charges may continue.

### Update admission drain

After building, `install.sh update` requests `POST /v3/box/drain` before
activation. An automatic update (`--automatic`, the timer) first checks
`GET /v3/box/busy` and requests the drain only when Swarm is idle; busy, or
an unknown status, records `waiting` with the candidate commit and leaves the
daemon untouched until the next timer run. New V3 run intents remain durably pending; existing execution
owners, including parked parents, finish without being cancelled. The admission
fence remains closed until daemon restart. Only the installer installs binaries
and performs the privileged restart.

`UPDATE_DRAIN_SECONDS` sets the wait budget (default 7200, range 1–86400).
Timeout reports blocking session/run identifiers and automatically reopens
admission without interrupting running work. `DELETE /v3/box/drain` or
`swarm_cancel_update` explicitly cancels the drain; installer exit/failure also
attempts cancellation. Expiry and cancellation emit end signals. A new request
after either starts a fresh deadline. Readiness carries a daemon generation;
the installer verifies that same generation and readiness immediately before restart. An unavailable status endpoint
never authorizes activation. If the running daemon answers the initial drain POST
with HTTP 404, the installer records that it is draining the older way and polls
`/v3/box/busy` within the same `UPDATE_DRAIN_SECONDS` budget. Only an explicit
`busy: false` permits the shared activation and rollback path. A missing or
unavailable busy endpoint, invalid status, or timeout blocks the update and leaves
the daemon running. Other drain errors do not enable this fallback. Older daemons
cannot fence new admissions, so this compatibility path retains the old idle-check
race; avoid starting new work during the upgrade. No drain cleanup or reservation
is attempted when no generation was established.

Owner/admin clients can request the same fence with `swarm_request_update`
(MCP, optional `max_seconds`) or `POST /v3/box/drain` with JSON
`{"max_seconds":7200}`. This does not execute the installer: automatic updates
must be enabled or the operator must run it. `GET /v3/box/drain` reports readiness,
start/deadline times, timeout and blocking runs/builds; the machine summary exposes
redacted drain counts. New environment definitions are rejected during drain and
must be retried after restart. Existing builds remain blockers.

Before changing activation files, the installer atomically reserves a ready drain
using `PUT /v3/box/drain` with its generation and a private claim. A reservation
cannot expire or be cancelled by ordinary API/MCP cancellation. Restart replaces
it with a fresh admission manager; failure cleanup releases it with the same
claim. If the installer is killed without executing its trap, operator recovery
is required; the daemon never kills running work or performs host installation.
