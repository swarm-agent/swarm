---
name: setup
description: Install or connect Swarm locally, onboard a model provider and workspace, and verify a first delegated task. Use when the person asks to set up Swarm, connect their existing Swarm, or get started with the plugin. Help configure a relay only if they request remote access.
---

# Set up Swarm

Default to local operation. This plugin supplies instructions and a local MCP
bridge; it does not contain the Swarm daemon. Installing the plugin does not
install Swarm. A relay, public URL and cloud account are not prerequisites.

## Connect first

Read [connection modes](references/connection.md). Reuse existing Swarm over
local stdio, Docker or Tailscale before installing another daemon. The OpenAI
package needs Node 22+; configure exactly one mode with the bundled helper.
A 401 is an authentication failure, not a missing installation.
If authorization is missing, follow [AI-assisted authorization](references/authorization.md)
to create and connect a scoped key when owner execution is available. Handle
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

For missing installation or connection failures, read
[local deployment](references/local-deployment.md). Perform authorized setup
steps yourself when tools permit; pause only for missing choices, required
system permissions, sign-in or secret entry. Do not reset existing state.

## Onboard only what is missing

- If owner identity is absent, use Swarm's setup UI or `swarmctl setup identity`
  on the daemon's host. Inspect `setup status` first; identity is one-time.
- For a provider API key, use `swarm_request_provider_key` when its returned
  page is reachable by the person. In Docker, loopback links from the container
  may not be reachable: use the documented headless credential input instead.
  Never request a key in chat or print a secret file. `swarm_connect_chatgpt`
  signs Swarm into a model provider; it does not connect this plugin to ChatGPT.
- Read the available models and account defaults. Ask for missing model choices;
  preserve existing assignments. Do not choose a hardcoded provider/model.
- Select a registered workspace, or create one with their authorization. Explain
  that Docker workspace paths are inside the container, e.g. `/project`.
  Registration and a familiar name do not prove the person's code is present;
  verify the requested files before assigning a repository task. An empty
  workspace can be valid for new work and is not a connection failure.
- Reconnect/refresh the plugin's MCP after installation or configuration changes.
  Confirm a real tool call succeeds before saying it is connected.

## Prove the first task

Follow the packaged `using-swarm` skill. Use the person's actual task, or agree
on a small task in their chosen workspace. Start a session, keep its ID, handle
pending decisions, and inspect the result using `swarm_get_changes` and
`swarm_read_file` when exposed. Otherwise use authorized local file access or
the owner's Swarm UI, and state what remains unverified. Prefer waits of 20–30
seconds within the live tool schema's limit. A timeout
means work may still be running; resume the same session instead of duplicating
it. Report failures honestly. End with what runs where and what was verified.

For requested remote access later, read [optional relay](references/remote.md).
Do not enable it as a workaround for a local setup error. Do not promise dots
or cloud access merely because the desktop connection works; verify that
client's tool access separately.
