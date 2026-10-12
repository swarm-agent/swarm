<p align="center">
  <img src="web/public/favicon.svg" alt="Swarm logo" width="96" height="96">
</p>

# Swarm

**Swarm is a box you deploy AI workers to.**

Install it on a Linux machine (your own PC or a server), give it your model
keys, and it comes with three workers on the recommended models: a **coder**,
a **designer** and a **finder**. Your own AI (Claude Code, Codex, ChatGPT)
gives them jobs, waits, reads what they delivered and what it cost, and merges
it. It can also ask for images, video or sound. It works the same on every
machine you own, so your AI can work with AI across machines, safely.

- **Workers** work in any folder you register, each job on its own branch, in a
  sandbox per project (gVisor when available), with no access to the host, your
  network or Swarm's keys. Jobs run without permission prompts only while that
  sandbox is active.
- **Secrets** reach a job only through Swarm's gateway: inside the sandbox an
  agent sees a stand-in, and the value goes out only to the websites you
  allowed, for the folders you granted.
- **Schedules** run a worker's job every N minutes or on cron.
- **Monitoring** is part of the product: your AI asks whether the box is all
  right and what happened since last time (jobs finished or failed, cost,
  workers blocked), over MCP or HTTP.
- **No dashboard to learn.** You talk to your AI; it talks to Swarm through its
  MCP tools. The only page is the setup page where you type your keys (or a
  script does it).

This repository is under active development; this README describes what the
current code does.

## Install

Ubuntu 24.04, x86_64. From your own account:

```bash
curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/containers/headless/app/install.sh | sudo bash
```

It installs Docker (unless a working one is there) and gVisor, and runs Swarm
and its setup page as services that listen on 127.0.0.1 only. At the end it
creates Swarm's owner for you, puts an AI key for your AI in
`~/.config/swarm/ai-key` (only you can read it) and prints how to connect.
Tailscale is not needed; add `--tailnet` only to reach this machine from your
other devices. Details: `containers/headless/app/README.md`.

Your model provider key goes on the setup page (`http://127.0.0.1:8443`) or in
by script, never through chat:

```bash
sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh key PROVIDER < key-file
```

## Connect your AI

**Claude Code**: install the `swarm` plugin. It finds `~/.config/swarm/ai-key`
by itself (it needs Node 22+) and brings Swarm's skills:

```bash
claude plugin marketplace add swarm-agent/swarm
claude plugin install swarm@swarm
```

or add only the tools:

```bash
claude mcp add --scope user --transport http swarm http://127.0.0.1:7783/mcp --header "Authorization: Bearer $(cat ~/.config/swarm/ai-key)"
```

**Codex**: the installer prints the line (it reads the key from an
environment variable).

**Then tell your AI: "set up Swarm".** It asks which folders your work is in,
registers them, and gives the coder a first job.

## What your AI does with it

| Tool | What it does |
| --- | --- |
| `swarm_add_workspace` | register a folder (nothing in it is changed) |
| `swarm_give_job` | give the coder, designer or finder a job in a folder |
| `swarm_get_job` | wait for it; the result: branch, commits, files, report, cost, diff |
| `swarm_answer_job` | reply when a worker asks something |
| `swarm_deliver_job` | merge the result into the folder (local git merge, never a push) |
| `swarm_request_media` | an image, video or sound |
| `swarm_schedule_job` | a worker that runs a job on a schedule |
| `swarm_status`, `swarm_activity` | is the box all right; what happened since last time |
| `swarm_list_secrets` | which secrets exist and which folders may use them (never values) |

Advanced tools (sessions, plans, project orchestrators, model overrides) stay
available: `/mcp?tools=all` or `swarm mcp --all-tools`. The full catalog and
the trust boundary: `docs/checkpoints/swarm-control.md`.

## Desktop and terminal app

Swarm also ships a launcher with a terminal UI and a desktop web UI for
working with it by hand. They are not needed for the box above.


Linux x86_64 only. One line, no confirmation prompt:

```bash
curl -fsSL https://swarmagent.dev/install | sh -s -- --yes
```

It installs Swarm and, on a systemd host, starts the `swarm` service. Without a terminal (an AI agent, CI) a non-root run needs passwordless sudo; otherwise run it as root with `| sudo sh -s -- --yes`. Then [connect your AI](#connect-your-ai-to-the-launcher-install).

To choose the service behavior explicitly:

```bash
curl -fsSL https://swarmagent.dev/install | sh -s -- --yes --service
curl -fsSL https://swarmagent.dev/install | sh -s -- --yes --no-service
```

The installer fetches the latest stable GitHub release asset, extracts it, and runs the bundled installer. You do not need to clone or download this repository to install Swarm. The release archive contains Swarm itself; Git is supplied as a mandatory system package rather than a private bundled executable. The installer provisions the mandatory Git and Bash system packages when they are missing through the detected supported Linux package manager, verifies them before changing Swarm files, and leaves existing installations unchanged. Git is required because every saved or selected workspace must be a repository root with an initial commit and Swarm isolates agent sessions in managed worktrees; direct-session worktree opt-out is not supported. If a selected folder is not ready, Desktop explains the requirement: an empty folder can be initialized with an explicit setup action. For a non-empty folder, a provider-connected user can open the dedicated Onboarding Swarm without first admitting another workspace; it is bound to that exact unsaved folder, keeps dedicated shell-free Git initialization, ignore-file edits, staging, and the first commit permission-gated, uses existing Git identity or a user-supplied one-time identity without persisting it, and returns to onboarding for a readiness recheck before the workspace is saved. The alternative is to fix the repository manually and retry. Deterministic video analysis requires `ffmpeg` and `ffprobe` on `PATH`; those remain feature-local prerequisites rather than startup requirements.

The installer prints an install plan, including any runtime-package provisioning, places launchers in `/usr/local/bin`, and installs Swarm runtime artifacts under `/usr/local/share/swarm/{bin,libexec,lib,share}`. Run in a terminal without `--yes`, it offers three explicit choices: install/start the systemd service, install files only with no service, or cancel. Because these are system locations, `install.sh` may prompt for sudo during initial provisioning. Swarm-owned runtime directories are created for the service user, so a healthy installed system can verify, activate, restart, health-check, and if necessary roll back routine stable release updates without prompting for sudo. If launcher links, service topology, or install-root ownership are later changed or damaged, the update refuses before activation and directs you to perform a one-time privileged repair or reinstall; it does not silently fall back to sudo.

If your shell does not already include the launcher directory on `PATH`, run Swarm with:

```bash
/usr/local/bin/swarm
```

Manual release asset install is also supported. Download `swarm-<version>-linux-amd64.tar.gz` from a GitHub release, extract it, and run:

```bash
cd /path/to/extracted/swarm-<version>-linux-amd64
sh install.sh
```

Equivalent explicit artifact-root form:

```bash
sh install.sh --artifact-root /path/to/extracted/swarm-<version>-linux-amd64
```

From a source checkout, the setup helper can build and install local development launchers:

```bash
./setup
```

### Connect your AI to the launcher install

Your AI talks to Swarm through `swarm mcp`: MCP over stdio, no key to make or
paste. Run as the user that runs Swarm, it gets a short-lived AI key over
Swarm's private owner socket, keeps it only in memory and deletes it on exit.
If `swarm help` does not list `swarm mcp`, your Swarm is too old: update it
with the install line above.

**Claude Code**: install the `swarm` plugin (Swarm's tools plus skills for
setting it up, running agents and building apps on it):

```bash
claude plugin marketplace add swarm-agent/swarm
claude plugin install swarm@swarm
```

or add only the tools:

```bash
claude mcp add swarm -- swarm mcp
```

**Codex CLI** (`~/.codex/config.toml`):

```toml
[mcp_servers.swarm]
command = "swarm"
args = ["mcp"]
```

To add the Swarm skills to Codex too, package the plugin from a checkout of
this repository (Node 22+) and install it as a local marketplace:

```bash
node scripts/package-swarm-codex.mjs /absolute/new/output-folder
codex plugin marketplace add /absolute/new/output-folder
codex plugin add swarm@swarm-local
```

ChatGPT in the cloud cannot reach a Swarm on your computer; it needs a private
tunnel, see `plugins/swarm/skills/setup/references/chatgpt-tunnel.md`.

**Claude Desktop** (`claude_desktop_config.json`):

```json
{"mcpServers": {"swarm": {"command": "swarm", "args": ["mcp"]}}}
```

**Then set up:** ask your AI to "set up Swarm". It finds what is missing
(owner, model provider, workspace) and gives you a link to Swarm's local key
page, where you type your model provider key. The key never goes through chat.

The default is full access (sessions, approvals, models, workers, provider
key requests); add `--access write` or `--access read` to limit it. If a
desktop app cannot find `swarm`, use its full path (`command -v swarm`).
Details and the trust boundary: `docs/checkpoints/swarm-control.md`.

## Development session dumps

When `dev_mode = true` is set in `swarm.conf`, use the checked-in same-machine passthrough helper with a Desktop session URL:

```bash
./scripts/session-dump-via-api.sh http://127.0.0.1:5555/<workspace>/<session-id>
```

The helper bootstraps an authenticated local Desktop session, calls `POST /v3/developer/session-dump`, and prints the server-controlled absolute dump path. It accepts only localhost or loopback session URLs and never reads the Pebble database directly. Dump files are private (`0600`) under the daemon data directory at `session-dumps/` and include the session snapshot, projection, complete messages/events, run intents/state, plans, permissions, and usage summary. The endpoint returns `403` unless development mode is enabled and `404` when the session does not exist.

## Launcher quick start

Install Swarm with the systemd service and verify the daemon:

```bash
swarm install --service
swarm status
```

To install files only and manage the daemon with your own supervisor:

```bash
swarm install --no-service
```

Open controller clients against the running daemon:

```bash
swarm session
swarm open
```

Manage the daemon explicitly:

```bash
swarm start
swarm stop
swarm restart
swarm uninstall
```

Use the development lane:

```bash
swarm dev
swarmdev
swarm dev --desktop
swarm dev info
```

Default local ports:

| Command | Backend | Desktop |
| --- | --- | --- |
| `swarm` | `http://127.0.0.1:7781` | `http://127.0.0.1:5555` |
| `swarm dev` / `swarmdev` | `http://127.0.0.1:7782` | `http://127.0.0.1:5556` |

## Terminal UI commands

Type `/` in the terminal UI to open command suggestions. Current command surfaces include:

- `/auth` and `/models` for provider credentials, active credentials, model selection, and provider catalog state.
- `/workspace`, `/workspaces`, and `/add-dir` for workspace selection and linked-directory flows.
- `/sessions`, `/new`, `/home`, and `/compact` for session navigation and chat context management.
- `/permissions` for global tool and bash-prefix policy controls.
- `/plan` for plan-mode session workflows.
- `/agents` for saved agent profile management.
- `/themes`, `/keybinds`, and `/mouse` for UI customization and terminal input behavior.
- `/voice` for experimental terminal voice input controls. The terminal STT path has been tested, but voice is not a polished or guaranteed workflow yet.
- `/update` and `/rebuild` for installed runtime update and development rebuild flows.

Useful keys and runtime behavior:

- `Ctrl+C` quits.
- `F8` toggles mouse capture.
- `/mouse on`, `/mouse off`, and `/mouse status` manage mouse capture from the UI.
- When mouse capture is enabled, use the terminal selection modifier, usually `Shift+drag`, to select text.

## Architecture

Swarm is split into a launcher, a terminal UI, a daemon, and a web frontend:

| Area | Path | Purpose |
| --- | --- | --- |
| Launcher CLI | `cmd/swarm/`, `internal/launcher/` | Manages the installed daemon service, records port metadata, launches TUI or desktop controllers, runs update helpers. |
| Installer | `cmd/swarmsetup/` | Installs launchers and release artifacts into system locations. |
| Terminal UI | `cmd/swarmtui/`, `internal/app/`, `internal/ui/` | tcell app, slash commands, modals, settings, model/auth/workspace/session UI. |
| Daemon | `swarmd/` | HTTP/WebSocket API, provider runtime, sessions, workspaces, permissions, Pebble-backed persistence. |
| Desktop UI | `web/` | Vite/React browser frontend served by the local runtime. |
| Tests and harnesses | `tests/`, `swarmd/tests/`, `scripts/` | Unit tests, integration tests, e2e harnesses, release and policy checks. |

`swarmd` exposes health endpoints plus authenticated local API routes for auth credentials, provider status, model preferences/catalogs, workspaces, sessions, UI settings, permissions, and streaming session events.

## Local-first networking model

Swarm is designed for local use by default. Normal desktop/backend traffic should stay bound to `127.0.0.1`.

For access from another device, prefer an SSH tunnel or a private overlay network such as Tailscale. Direct private-LAN browser access may show browser security warnings and may be rejected by the local-first desktop auth path.

Example SSH tunnel for the desktop port:

```bash
ssh -L 5555:127.0.0.1:5555 <host>
```

You can point the terminal UI at a specific daemon with:

```bash
SWARMD_URL=http://127.0.0.1:7782 SWARMD_TOKEN=<token> swarm dev
```

## Privacy and anonymous mint reporting

When a new local swarm identity is generated for the first time, `swarmd` makes one best-effort HTTPS request to `https://swarmagent.dev/api/mint`. The request contains only payload version `1` and a one-way anonymous identifier derived locally from the random swarm ID. It does not send the raw swarm ID or user, machine, network, key, account, workspace, provider, or session data. Delivery state is stored locally so transient failures can retry on a later start without delaying or preventing daemon startup; existing and explicitly restored identities are not reported as new mints.

## Data and configuration locations

Swarm uses system locations for Swarm-owned daemon state. By default it does not write daemon databases, secrets, runtime files, logs, caches, generated artifacts, downloads, reports, or worktrees under a user home directory, XDG user directory, repository checkout, or current working directory.

Linux defaults:

- `/usr/local/bin` for launchers.
- `/usr/local/share/swarm/{bin,libexec,lib,share}` for runtime files.
- `/etc/swarmd` for daemon configuration.
- `/var/lib/swarmd` for daemon data, databases, secrets, generated artifacts, reports, and worktrees.
- `/var/cache/swarmd` for daemon caches.
- `/run/swarmd` for volatile runtime files, sockets, locks, and PID files.
- `/var/log/swarmd` for logs and diagnostic artifacts.

macOS support is not yet the primary installer target, but the storage contract is prepared for system-level locations: `/Library/Application Support/Swarm/swarmd` for data, `/Library/Application Support/Swarm/swarmd/config` for configuration, `/Library/Caches/Swarm/swarmd`, `/var/run/swarmd`, and `/Library/Logs/Swarm/swarmd`. Future macOS installer work should provision those system roots rather than user `~/Library` locations.

Swarm intentionally does not silently migrate or reuse legacy home/XDG/workspace daemon data. If legacy startup config or secrets are detected, startup stops with a diagnostic telling you which legacy path exists and which system path is expected. Move data only after an explicit backup and operator-controlled migration.

UI settings are persisted through the daemon-backed `/v1/ui/settings` API. Current settings include chat header visibility, thinking tags, tool stream display, mouse capture, keybinds, theme selection, custom themes, and swarm display metadata.

## Development

Run the repository pre-commit gate before committing changes:

```bash
./scripts/check-precommit.sh
```

That gate includes repository policy checks, secret checks, hardcoded-path checks, and vulnerability scanning. Additional development scripts live under `scripts/`.

Live candidate testing runs in the release pipeline, not from this checkout. The host Swarm service and host ports `5555/7781` are not a live candidate-test target.

Common source-checkout commands:

```bash
./setup
./rebuild dev
swarm dev
swarm dev info
```

## Suggested GitHub repository metadata

These fields must be set in the GitHub repository UI by a maintainer:

**About description**

> Local AI coding workspace for terminal and desktop: Go launcher, swarmd daemon, tcell TUI, browser UI, providers, sessions, tools, and permissions.

**Topics**

`ai-coding-agent`, `developer-tools`, `terminal-ui`, `tui`, `desktop-app`, `go`, `golang`, `local-first`, `multi-agent`, `coding-assistant`, `llm`, `ai-tools`, `workspace-management`, `permissions`, `websocket`, `react`, `vite`, `pebble`, `cli`, `open-source`

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
