---
name: setup
description: Install or connect Swarm locally, onboard a model provider, the person's workspaces and a project, and verify a first delegated task. Use when the person asks to set up Swarm, connect their existing Swarm, or get started with the plugin. Help configure a private tunnel or relay only if they request remote access.
---

# Set up Swarm

Default to local operation. The skills package supplies instructions; the local
Codex package also supplies an MCP bridge. Neither contains the Swarm daemon.
Installing a custom MCP connection does not install these skills, and installing
these skills does not deploy Swarm or authorize a connection.
A relay, public URL and cloud account are not local prerequisites.

## Select the client before connecting

Identify where the requesting AI runs and where Swarm runs. Reuse a working
connection. Do not apply one client's transport setup to another:

| Client | Connection instructions |
| --- | --- |
| Codex/local MCP runtime on the person's computer | [Local stdio, Docker or tailnet](references/connection.md) |
| ChatGPT Work or dot using private remote Swarm | [OpenAI private tunnel](references/chatgpt-tunnel.md) |
| Claude Code or Claude Desktop local MCP | [Claude local connection](references/remote.md#claude-clients); no OpenAI tunnel |
| Claude.ai or an explicitly requested public relay | [Remote client options](references/remote.md) |

A cloud client cannot reach a desktop's localhost or tailnet just because that
desktop can. ChatGPT setup needs authorized execution on the actual Swarm host
(a connected computer or selected remote host); the skill alone cannot supply
that access. Keep the person's role to missing choices, consent, sign-in and
hidden secret entry. Perform authorized deployment and connection mechanics.

## Connect first

Reuse existing Swarm before installing another daemon. For the local Codex
bridge, read [connection modes](references/connection.md): it needs Node 22+ and
exactly one native, Docker or tailnet mode configured with the bundled helper.
For a ChatGPT private tunnel, follow its dedicated guide instead: it goes
directly to Swarm HTTP and does not use the Node adapter or connection.json.
A 401 is an authentication failure, not a missing installation.
For local bridge authorization, follow [AI-assisted authorization](references/authorization.md);
the tunnel guide has its own service credential handoff. Create and connect a
scoped key when owner execution is available. Handle
the mechanics yourself; the person's role is to authorize access or complete
an OS password prompt, not learn key management.

## Find the missing step

Inspect the live tool list first: installed daemon versions and access grants
may expose different tools. Call `swarm_list_models` and
`swarm_list_workspaces`, plus `swarm_list_providers` when available. Reuse the existing installation
and account. Missing tools do NOT prove that Swarm is absent: check plugin
activation, MCP startup errors, the selected execution computer and PATH.

If a shell on the person's selected computer is available, inspect the OS,
architecture, `command -v swarm`, and whether their chosen Docker container
exists. On Windows use the shell's equivalent command discovery. Never deploy
into an unrelated cloud execution sandbox and call it the person's computer.
If local execution is unavailable, explain that setup needs their connected
computer or give them the exact commands to run there.

To install Swarm natively (Linux x86_64 only), use the maintained one-line
installer after the person authorizes its system changes:

```sh
curl -fsSL https://swarmagent.dev/install | sh -s -- --yes
```

It installs under `/usr/local` and starts the systemd service on systemd hosts.
Without a terminal, a non-root run needs passwordless sudo; otherwise the
person runs it in their own terminal with `| sudo sh -s -- --yes`. If Swarm is
installed but `swarm help` does not list `swarm mcp`, or the MCP server says
this Swarm is too old for AI connections, run the same line to update it; it
keeps the existing install's owner. Then reconnect the MCP server.

For missing installation or connection failures, read
[local deployment](references/local-deployment.md). Perform authorized setup
steps yourself when tools permit; pause only for missing choices, required
system permissions, sign-in or secret entry. Do not reset existing state.

## Onboard only what is missing

- If owner identity is absent (MCP tools answer "Swarm has no owner yet"), ask the
  person for their name and a name for this Swarm, then run on the daemon's host
  `swarm ctl setup identity --username <name> --name <swarm name>` (or use the
  setup UI). Inspect `swarm ctl setup status` first; identity is one-time.
- For a provider API key, use `swarm_request_provider_key` when its returned
  page is reachable by the person. In Docker, loopback links from the container
  may not be reachable: use the documented headless credential input instead.
  Never request a key in chat or print a secret file. `swarm_connect_chatgpt`
  signs Swarm into a model provider; it does not connect this plugin to ChatGPT.
- Read the available models and account defaults. Ask for missing model choices;
  preserve existing assignments. Do not choose a hardcoded provider/model.
- Ask the person: "What workspaces would you like to add?" — existing folders
  or git repositories, possibly several. Register each with
  `swarm_add_workspace` and group them into a project with
  `swarm_create_project`; wait with `swarm_get_project` until its `project.md`
  is ready (see the `using-swarm` skill, section 6). Swarm only sees folders it
  can read: Docker paths are inside the container (e.g. `/project`), and a
  service install sees what its service account can read. A headless install
  hides home folders; when `swarm_add_workspace` answers
  `workspace_not_visible`, run the command it names as root on that machine
  (`install.sh workspace add PATH`, which binds exactly that repository into
  the service and restarts Swarm; Swarm never changes the machine itself),
  then add the folder again. Registration and a
  familiar name do not prove the person's code is present; verify the requested
  files before assigning a repository task. Use `swarm_create_workspace` (a
  new, empty repository) only when they ask to start a new project from scratch.
- Reconnect/refresh the plugin's MCP after installation or configuration changes.
  Confirm a real tool call succeeds before saying it is connected.

## Prove the first task

Follow the packaged `using-swarm` skill. Use the person's actual task, or agree
on a small task in their project. Talk to the project's orchestrator
(`swarm_start_session` with agent `system-orchestrator` and the `project_id`),
or for a quick check start a session in one of its workspaces. Keep its ID, handle
pending decisions, and inspect the result using `swarm_get_changes` and
`swarm_read_file` when exposed. Otherwise use authorized local file access or
the owner's Swarm UI, and state what remains unverified. Prefer waits of 20–30
seconds within the live tool schema's limit. A timeout
means work may still be running; resume the same session instead of duplicating
it. Report failures honestly. End with what runs where, which workspaces the
project holds and what was verified.

For requested remote access later, read [optional remote access](references/remote.md).
Do not enable it as a workaround for a local setup error. Do not promise dots
or cloud access merely because the desktop connection works; verify that
client's tool access separately.

For protocol checks, response parsing, scope filtering or MCP errors, read
[Swarm's MCP contract](references/mcp-protocol.md). Use the live schema; do not
assume all installations expose the same tools.
