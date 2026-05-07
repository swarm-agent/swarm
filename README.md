<p align="center">
  <img src="web/public/favicon.svg" alt="Swarm logo" width="96" height="96">
</p>

# Swarm

**Swarm is a local AI coding workspace for the terminal and browser, written in Go.**

It combines an installed `swarm` launcher, the `swarmd` daemon, a tcell terminal UI, and a desktop web UI for working with local repositories, AI model providers, sessions, tools, permissions, themes, and workspace state.

This repository is under active development. The README below is intentionally conservative: it describes capabilities represented in the current codebase and avoids placeholder screenshots, hosted-service promises, or benchmark claims.

## What Swarm does

- Runs a local backend daemon (`swarmd`) for workspace, session, model, auth, permission, and UI settings state.
- Opens a terminal UI with slash commands for workspaces, sessions, providers, models, auth, permissions, themes, keybinds, updates, and related coding workflow controls.
- Opens a browser desktop UI with the installed launcher via `swarm --desktop`.
- Supports main and dev runtime lanes so an installed release and a development lane can use separate ports and state.
- Stores runtime data under system locations instead of repository-local mutable state.
- Uses attach-token authenticated local API endpoints for non-health daemon access.
- Includes provider adapters and auth/status plumbing for Anthropic, Codex, Google, Fireworks, OpenRouter, and Exa search support. Copilot is not currently available as a selectable or runnable provider.
- Includes repository guardrails for public-repo hygiene, pre-commit checks, secret scanning, policy checks, and vulnerability scanning.

## Install

Fast lane for Linux x86_64:

```bash
curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh | sh
```

That command fetches the latest stable GitHub release asset, extracts it, and runs the bundled installer. You do not need to clone or download this repository to install Swarm.

The installer places launchers in `/usr/local/bin` and installs Swarm runtime artifacts under `/usr/local/share/swarm/{bin,libexec,lib,share}`. Because those are system locations, `install.sh` may prompt for sudo during provisioning. Swarm-owned subdirectories are created for the installing user so the daemon still runs as that user.

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

## Quick start

Open the main terminal UI:

```bash
swarm
```

Open the desktop UI:

```bash
swarm --desktop
```

Manage the local backend without opening a UI:

```bash
swarm server on
swarm server status
swarm server off
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
| Launcher CLI | `cmd/swarm/`, `internal/launcher/` | Starts/stops lanes, records port metadata, launches TUI or desktop, runs update helpers. |
| Installer | `cmd/swarmsetup/` | Installs launchers and release artifacts into system locations. |
| Terminal UI | `cmd/swarmtui/`, `internal/app/`, `internal/ui/` | tcell app, slash commands, modals, settings, model/auth/workspace/session UI. |
| Daemon | `swarmd/` | HTTP/WebSocket API, provider runtime, sessions, workspaces, permissions, Pebble-backed persistence. |
| Desktop UI | `web/` | Vite/React browser frontend served by the local runtime. |
| Tests and harnesses | `tests/`, `swarmd/tests/`, `scripts/` | Unit tests, integration tests, e2e harnesses, release and policy checks. |

`swarmd` exposes health endpoints plus authenticated local API routes for auth credentials, provider status, model preferences/catalogs, workspaces, sessions, UI settings, permissions, and streaming session events.

## Local-first networking model

Swarm is designed for local use by default. Normal desktop/backend traffic should stay bound to `127.0.0.1`.

For access from another device, prefer an SSH tunnel or a private overlay network such as Tailscale. Direct private-LAN browser access may show browser security warnings and may be rejected by desktop auth until a safer LAN pairing flow exists.

Example SSH tunnel for the desktop port:

```bash
ssh -L 5555:127.0.0.1:5555 <host>
```

You can point the terminal UI at a specific daemon with:

```bash
SWARMD_URL=http://127.0.0.1:7782 SWARMD_TOKEN=<token> swarm dev
```

## Data and configuration locations

Swarm uses system locations for Swarm-owned daemon state. By default it does not write daemon databases, secrets, runtime files, logs, caches, generated artifacts, downloads, reports, or worktrees under a user home directory, XDG user directory, repository checkout, or current working directory.

Linux defaults:

- `/usr/local/bin` for launchers.
- `/usr/local/share/swarm/{bin,libexec,lib,share}` for runtime files.
- `/etc/swarmd` for daemon and startup configuration.
- `/var/lib/swarmd` for daemon data, databases, secrets, generated artifacts, reports, worktrees, and remote-deploy session data.
- `/var/cache/swarmd` for daemon caches.
- `/run/swarmd` for volatile runtime files, sockets, locks, and PID files.
- `/var/log/swarmd` for logs and diagnostic artifacts.

Remote deploy and container sessions use the same split-root model. Remote deploy session data lives under `/var/lib/swarmd/remote-deploy/<session>`, configuration under `/etc/swarmd/remote-deploy/<session>`, cache under `/var/cache/swarmd/remote-deploy/<session>`, runtime files under `/run/swarmd/remote-deploy/<session>`, and logs under `/var/log/swarmd/remote-deploy/<session>`.

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

MIT. See [`LICENSE`](LICENSE).
