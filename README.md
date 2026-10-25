<p align="center">
  <img src="web/public/favicon.svg" alt="Swarm logo" width="96" height="96">
</p>

# Swarm

**Swarm is a headless AI agent platform you self-host on Linux. It runs coding, research, design and media agents, workflows, schedules and monitoring across your machines. Drive it from your AI over MCP, or from your own code with the SDK, HTTP API or CLI.**

[Website](https://swarmagent.dev) · [Docs](https://swarmagent.dev/docs) · [AI setup playbook](https://swarmagent.dev/llms.txt)

Install it on your Linux PC or server. Choose how to use it:

- **MCP tools** for your AI: Claude Code, Codex or ChatGPT.
- **SDK** for your own code: [`@swarm-agent/sdk`](packages/sdk).
- **HTTP API** for your own client app: [building a client](docs/frontends.md), the way Swarm Console does it.
- **CLI** for command-line setup and control: [`swarmctl`](swarmd/cmd/swarmctl).

Through MCP, your AI can get work done in your folders two ways:

- **Delegate a goal to Swarm.** It plans the work and splits it across its
  coder, finder and designer.
- **Give work directly to an agent:** the **coder** (code), **designer** (UI
  and HTML designs), **finder** (research and audits), or **media** (images,
  video, sound). Use your own workers for jobs with standing instructions,
  and add a reviewer step to a workflow to review completed work.

Coding tasks use isolated branches and return a report and cost information;
your AI reviews the changes before authorizing a merge. Research and media
return reports or files instead. When you want more:

- **Workers:** your own named workers with standing instructions.
- **Schedules:** a worker runs a job every N minutes or on cron.
- **Automations:** steps that run when a job ends: your script, another agent
  (a reviewer, say), or sending the job back with feedback.
- **Environments:** a folder's own container (a base image plus setup) that its
  agents' commands run in, with that folder's secrets. Agents see stand-ins;
  the real value goes out only to the websites you allowed.
- **Monitoring and alerts:** your AI asks whether Swarm is all right and what
  happened (jobs finished or failed, cost, workers blocked); signals go to a
  webhook or Telegram.

By default agents run on the machine as Swarm's own user, without permission
prompts and without a sandbox; `install.sh sandbox on` runs every command in
one container per project. No dashboard is required: use your AI over MCP,
or integrate Swarm into your own apps and scripts. The headless setup page
lets you enter provider keys; scripted setup is available too.

This repository is under active development; this README describes what the
current code does.

This is [swarm-agent/swarm](https://github.com/swarm-agent/swarm) at
[swarmagent.dev](https://swarmagent.dev), not OpenAI's Swarm framework,
Docker Swarm, or kyegomez/swarms.

## When to use Swarm

- Run coding agents in parallel in the background while you keep working,
  within your configured concurrency and approval limits.
- Run a self-hosted agent fleet on your own Linux machines. Commands and
  checkouts stay there; configured model providers still receive model requests.
- Let Claude Code, Codex or ChatGPT delegate work through MCP tools, wait for
  the result, and read reported token usage and estimated cost.
- Build apps and scripts that call Swarm directly through the SDK or HTTP API,
  including [sealed agents](containers/sealed/README.md) whose tools are
  answered by your own application.
- Use your ChatGPT plan, your Claude plan through Claude Code, or provider
  API keys to run agents.
- Reach agents across several machines as one fleet through the
  [tailnet MCP server](packages/swarm-fleet/README.md).
- Give agents folder-scoped secrets through environments: agents see stand-ins,
  and real values are sent only to allowed websites.

## Install

**For AI assistants:** when asked to set up Swarm, read
[the AI setup playbook](https://swarmagent.dev/llms.txt) and follow it.

Ubuntu 24.04, x86_64. From your own account:

```bash
curl -fsSL https://swarmagent.dev/install | sudo bash
```

It downloads the newest release of Swarm, checks it against its published
checksum and runs Swarm and its setup page as services under Swarm's own
`swarm` user, listening on 127.0.0.1 only. Nothing is compiled and no Docker is
installed. At the end it creates Swarm's owner for you, puts an AI key for your
AI in `~/.config/swarm/ai-key` (only you can read it) and prints how to
connect. Add `--environments on` (or run `install.sh environments on` later)
for environments, which need Docker; add `--tailnet` only to reach this
machine from your other devices. Details: `containers/headless/app/README.md`.

Your model provider key goes in **Settings > Providers** on the setup page (`http://127.0.0.1:8443`) or in
by script, never through chat:

```bash
sudo bash /var/lib/swarm-headless/install.sh key PROVIDER < key-file
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

**Then tell your AI: "set up Swarm".** It checks for a model provider (your
ChatGPT or Claude plan, or an API key), asks which folders your work is in,
creates a project with them, and gives the first piece of work.

**Building your own client** (a console, a dashboard, a web app)?
[docs/frontends.md](docs/frontends.md) is the path, the same one Swarm Console
and the web UI use: install Swarm, connect your client with an owner session of
its own (`swarmctl client connect NAME`), read one snapshot and the live
stream, and talk to the Orchestrator over the HTTP API.

## What your AI does with it

| Tool | What it does |
| --- | --- |
| `swarm_list_providers`, `swarm_list_models` | which providers are ready; the model each worker role uses |
| `swarm_connect_chatgpt`, `swarm_connect_claude_code` | sign in with your ChatGPT plan, or use your Claude plan through Claude Code |
| `swarm_set_agent_model` | change a role's model (only when you ask) |
| `swarm_create_project` | a project: a name and your folders (nothing in them is changed) |
| `swarm_run_task`, `swarm_wait_task`, `swarm_task_result` | delegate a goal to Swarm, which splits it across its coder, finder and designer |
| `swarm_give_job` | give one of your own workers a job in a project's folder; use `swarm_run_task` with a role for built-in agents |
| `swarm_get_job` | wait for it; the result: branch, commits, files, report, cost, diff, transcript |
| `swarm_message_job` | a follow-up in the same job: more to do, a correction, a redo |
| `swarm_answer_job` | reply when a worker asks something |
| `swarm_deliver_job` | deliver the result: a local git merge into the folder, or a pull request when you set up publishing for it (`install.sh workspace publish`); Swarm never pushes otherwise |
| `swarm_request_media` | an image, video or sound |
| `swarm_deploy_worker` | your own worker: a name, a role, standing instructions |
| `swarm_schedule_job` | a worker that runs a job on a schedule |
| `swarm_set_workflow`, `swarm_list_workflows` | a workflow: steps that run when a task of Swarm's own agents (coder, finder, designer, swarm) or a worker job ends: another agent such as a reviewer, sending the work back to the agent that did it, or your script to push, deploy or alert |
| `swarm_set_environment`, `swarm_get_environment` | a folder's environment: a base image plus setup commands, built by Swarm, with `max_leases` |
| `swarm_status`, `swarm_activity` | is Swarm all right; what happened since last time |
| `swarm_list_secrets` | which secrets exist and which folders may use them (never values) |

Advanced tools (sessions, plans, project orchestrators, tasks by role) stay
available: `/mcp?tools=all`. The full catalog and the trust boundary:
`docs/checkpoints/swarm-control.md`.

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

Live candidate testing runs in the release pipeline, not from this checkout. The host Swarm service and its ports are not a live candidate-test target.

Common source-checkout commands:

```bash
./setup
./rebuild dev
swarm dev
swarm dev info
```

## Suggested GitHub repository metadata

These fields must be set in the GitHub repository UI by a maintainer:

**Website:** https://swarmagent.dev

**About description**

> Swarm is a headless AI agent platform you self-host on Linux. It runs coding, research, design and media agents, workflows, schedules and monitoring across your machines. Drive it from your AI over MCP, or from your own code with the SDK, HTTP API or CLI.

**Topics**

`ai-agents`, `coding-agent`, `mcp`, `mcp-server`, `self-hosted`, `agent-orchestration`, `multi-agent`, `claude-code`, `codex`, `chatgpt`, `background-agents`, `agent-fleet`, `llm`, `ai-tools`, `developer-tools`, `golang`, `linux`, `open-source`

## Sandbox build caches

Installed sandboxes share a project-local Go build/module cache and pnpm store
across jobs and reviewers, including custom environments. `-trimpath` enables
reuse across checkout paths. Configure the per-project soft limit with
`project_cache_bytes` in the durable box limits via `swarm_set_limits` (default
10 GiB; read live at trimming time). The image's warm pnpm store is seeded too.
See [sandbox resources](docs/sandbox-resources.md) for seeding, concurrency,
size reporting, and trimming limitations.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
