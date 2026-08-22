<p align="center">
  <img src="web/public/favicon.svg" alt="Swarm logo" width="96" height="96">
</p>

# Swarm

**Swarm is a local AI coding workspace for the terminal and browser, written in Go.**

It combines an installed `swarm` launcher, the `swarmd` daemon, a tcell terminal UI, and a desktop web UI for working with local repositories, AI model providers, sessions, tools, permissions, themes, and workspace state.

This repository is under active development. The README below is intentionally conservative: it describes capabilities represented in the current codebase and avoids placeholder screenshots, hosted-service promises, or benchmark claims.

## What Swarm does

- Installs a local backend daemon (`swarmd`) that stays running for workspace, session, model, auth, permission, and UI settings state.
- Opens controller clients that attach to the daemon: `swarm session` for the terminal UI and `swarm open` for the desktop UI.
- Keeps daemon lifecycle explicit with `swarm install`, `swarm status`, `swarm start`, `swarm stop`, `swarm restart`, and `swarm uninstall`.
- Stores runtime data under system locations instead of repository-local mutable state.
- Uses attach-token authenticated local API endpoints for non-health daemon access.
- Includes provider adapters and auth/status plumbing for Anthropic, Codex, Google, Fireworks, OpenRouter, and Exa search support. Copilot is not currently available as a selectable or runnable provider.
- Includes repository guardrails for public-repo hygiene, pre-commit checks, secret scanning, policy checks, and vulnerability scanning.
- Dedicated local-container execution and APIs are retired. V3 session durability and generic Swarm targets remain current contracts; containers or other non-local execution may return only as future runner targets.

## Install

Fast lane for Linux x86_64:

```bash
curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh | sh
```

For automation, choose the service behavior explicitly:

```bash
sh install.sh --yes --service
sh install.sh --yes --no-service
```

That command fetches the latest stable GitHub release asset, extracts it, and runs the bundled installer. You do not need to clone or download this repository to install Swarm. Deterministic video analysis requires `ffmpeg` and `ffprobe` on `PATH`; install your distribution's FFmpeg package before starting video transcription.

The installer prints an install plan, places launchers in `/usr/local/bin`, and installs Swarm runtime artifacts under `/usr/local/share/swarm/{bin,libexec,lib,share}`. It then offers three explicit choices: install/start the systemd service, install files only with no service, or cancel. Because these are system locations, `install.sh` may prompt for sudo during initial provisioning. Swarm-owned runtime directories are created for the service user, so a healthy installed system can verify, activate, restart, health-check, and if necessary roll back routine stable release updates without prompting for sudo. If launcher links, service topology, or install-root ownership are later changed or damaged, the update refuses before activation and directs you to perform a one-time privileged repair or reinstall; it does not silently fall back to sudo.

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

## Development session dumps

When `dev_mode = true` is set in `swarm.conf`, use the checked-in same-machine passthrough helper with a Desktop session URL:

```bash
./scripts/session-dump-via-api.sh http://127.0.0.1:5555/<workspace>/<session-id>
```

The helper bootstraps an authenticated local Desktop session, calls `POST /v3/developer/session-dump`, and prints the server-controlled absolute dump path. It accepts only localhost or loopback session URLs and never reads the Pebble database directly. Dump files are private (`0600`) under the daemon data directory at `session-dumps/` and include the session snapshot, projection, complete messages/events, run intents/state, plans, permissions, and usage summary. The endpoint returns `403` unless development mode is enabled and `404` when the session does not exist.

## Quick start

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
