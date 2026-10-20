---
name: setup
description: Install Swarm (a box the person deploys AI workers to) on their Linux machine, connect their AI to it locally, get a model provider ready, create a project with their folders, and prove a first job. Use when the person asks to set up Swarm, connect their existing Swarm, or get started with the plugin. Help with Tailscale or a relay only if they want to reach Swarm from other machines.
---

# Set up Swarm

Swarm runs on the person's Linux machine (Ubuntu 24.04 x86_64: their own PC or
a server) as a box they deploy AI workers to. It works on one machine with a
local AI; Tailscale and relays are optional extras for other machines. The
person's part is consent, choices, typing keys where Swarm asks, and sign-in.
You do the rest when you can run commands on that machine.

## 1. Is Swarm already there?

Reuse what exists. Call `swarm_status`: if it answers, Swarm is installed and
connected — skip to section 4. A 401 is a key problem, not a missing install.
On the machine, `ls ~/.config/swarm/ai-key` and `systemctl is-active
swarm-headless` tell you whether the box is installed and this person has a
key.

## 2. Install the box

After the person agrees to the system changes (it installs Docker unless a
working one is there, gVisor, and Swarm as a service), run on that machine, as
the person, with sudo:

```sh
curl -fsSL https://swarmagent.dev/install | sudo bash
```

Everything it starts listens on 127.0.0.1 only. At the end it creates Swarm's
owner for the person who ran it, puts an AI key for their AI in
`~/.config/swarm/ai-key` (only they can read it) and prints the connect lines.
Workers run in a sandbox per project without permission prompts, and the
secret gateway is on. `install.sh connect-ai` makes a new key later.

Add `--tailnet` only if the person wants to reach Swarm from other devices
(see [remote access](references/remote.md)); it is not needed for a local AI.

## 3. Connect the AI

- **Claude Code with this plugin:** nothing to configure. The plugin finds
  `~/.config/swarm/ai-key` and connects to `http://127.0.0.1:7783/mcp` (it
  needs Node 22+). Restart or reconnect the plugin's MCP server after the
  install.
- **Claude Code with this plugin, Swarm on another tailnet machine:** from the
  plugin folder, `node bin/connect.mjs http https://SWARM-HOST.YOUR-TAILNET.ts.net:8444/mcp`
  once (see [connection modes](references/connection.md)), then reconnect the
  plugin's MCP server. Keyless with the tailnet grant; needs Node 22+.
- **Claude Code, tools only:**
  `claude mcp add --scope user --transport http swarm http://127.0.0.1:7783/mcp --header "Authorization: Bearer $(cat ~/.config/swarm/ai-key)"`
- **Codex:** the line the installer printed (it reads the key from an
  environment variable).

Confirm a real call (`swarm_status`) succeeds before saying it is connected.
For other clients and remote setups, see [connection modes](references/connection.md)
and [remote access](references/remote.md).

## 4. A model provider

`swarm_list_providers` shows which providers are ready. For each one that is
not, its `connect` field says exactly how to connect it. If none is ready, ask
the person which they have, show every option below in one short list with
what each needs, and follow their choice. No option is the default.

- **ChatGPT plan** (`codex`): `swarm_connect_chatgpt` with action `start`
  returns a link and a code. Give both to the person at once; they finish
  sign-in in their own browser. Then call action `status` with the
  `login_id` and `wait_seconds` until it reports `success`.
- **Claude plan through Claude Code** (`claudecode`): the person signs in
  themselves; Swarm never sees or handles Claude credentials. See
  [Claude Code](references/local-deployment.md#claude-code) for the three
  steps (install it, `claude auth login` as Swarm's user, turn it on).
  `swarm_connect_claude_code` does it from here: action `status` gives the
  installer command the person runs on the machine, then action `activate`.
- **API key** for `anthropic`, `openai`, `google`, `openrouter` or
  `fireworks`: the person adds it on the setup page (`http://127.0.0.1:8443`
  on the machine; on a server, the installer printed the `ssh -L` line to open
  it from their computer), or as the first provider by script on the machine:
  `sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh key PROVIDER < key-file`.
- **Vertex AI** (`vertex`), only on a machine with a Google Cloud identity:
  `sudo swarmctl setup credential --provider vertex --project PROJECT`.

Never ask for a key in chat; if one is pasted, tell them to revoke it.
Call `swarm_list_providers` again to confirm it is ready. Swarm then gives
every role its recommended model: `swarm_list_models` shows what the coder,
designer and finder agents will use. Do not change models unless the person
asks (`swarm_set_agent_model` does it, per role).

## 5. Their project

Ask: "Which folders is your work in?" — existing folders or git repositories,
possibly several. Create a project with `swarm_create_project`: a name and
those folders. Swarm registers each folder in place and writes the project's
`project.md` from them.

- `workspace_not_found`: the path is wrong; check it with the person.
- `workspace_not_visible`: Swarm (its own user) cannot see inside a home
  folder and never changes its host itself. Run, as root on that machine,
  `sudo swarmctl workspace add PATH`
  (the command the error names), then create the project again.

Use `swarm_create_workspace` (a new, empty repository; advanced tools) only
when the person asks to start a new project from scratch.

## 6. Prove a first task

Use the person's real task, or agree on a small one in their project. Follow
`using-swarm`: `swarm_run_task` with `project_id`, the registered workspace
and a brief with acceptance criteria. Omit `role` to delegate to Swarm, which
uses its task tool for regular specialists. No worker preset is required or
created by this flow. Follow `swarm_wait_task`, read `swarm_task_result` and
`swarm_get_session`, and handle returned questions/approvals through their
authorized controls. Check actual files, diffs and validation receipts; use
`swarm_integrate_task` only when the person wants the reviewed work integrated.
A timeout means keep waiting, never start a duplicate task.

End with what runs where, the project/workspace, resolved models without
secrets, what completed, measured usage, and what was actually verified.

## Secrets

If workers need an API token for a website (for example to post or deploy),
the person sets it on the machine; you never see it:

- `install.sh secret set NAME --hosts api.example.com` (value on stdin),
- `install.sh secret grant NAME FOLDER --days N`.

`swarm_list_secrets` then shows it granted to that folder; a job in that folder
uses it through Swarm's gateway, never seeing the value.

For protocol checks, response parsing or MCP errors, read
[Swarm's MCP contract](references/mcp-protocol.md).
