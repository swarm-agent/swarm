# Local deployment and connection

Swarm runs on the person's Linux machine (Ubuntu 24.04 x86_64) as the Swarm
box. Reuse an existing box; install one only when Swarm is actually absent.

## Install the box

After the person agrees to the system changes (it installs Docker unless a
working one is there, gVisor, and Swarm as a service), run on that machine, as
the person, with sudo:

```sh
curl -fsSL https://swarmagent.dev/install | sudo bash
```

Everything it starts listens on 127.0.0.1 only: the setup page at
`http://127.0.0.1:8443` and Swarm's MCP (Swarm Control) at
`http://127.0.0.1:7783/mcp`, which answers only to AI keys. On a server, the
installer prints the `ssh -N -L 8443:127.0.0.1:8443 ...` line to open the setup
page from the person's computer. No inbound port, relay or pasted key is needed.

The installer creates Swarm's owner for the person who ran it and writes an AI
key (write: give and follow jobs, deliver results, connect their ChatGPT plan)
to their `~/.config/swarm/ai-key` (mode 0600, only they can read it; never
printed). For a new key, which replaces and revokes the old one:

```sh
sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh connect-ai
```

`--for-user NAME` makes it for another person on the machine; `--access full`
also approves tool calls and manages models, providers and workers (only when
the person asks for that). `install.sh status` shows the box's state. Missing
permission, a stopped service and a missing install are different failures;
reinstalling is not a remedy for a connection problem.

## Connect

- **This plugin (Claude Code):** with `~/.config/swarm/ai-key` present and no
  saved connection, `bin/swarm-mcp` connects to `http://127.0.0.1:7783/mcp`
  through the Node adapter (Node 22+). Restart or reconnect the plugin's MCP
  server after the install.
- **Any other MCP client on the machine:** HTTP to
  `http://127.0.0.1:7783/mcp` with `Authorization: Bearer <the key>`, read
  from the key file (for example `$(cat ~/.config/swarm/ai-key)`), never
  pasted into chat. The installer prints the Claude Code and Codex lines.

Confirm a real call (`swarm_status`) succeeds before saying it is connected.
Other machines reach the box over the tailnet ([connection.md](connection.md),
[remote.md](remote.md)).

## A model provider

`swarm_list_providers` shows which providers are ready. If none is, first offer
the person to sign in with their ChatGPT plan: `swarm_connect_chatgpt` with
action `start` returns a link and a one-time code. Give both to the person at
once; they approve in their own browser. Then call action `status` with the
`login_id` and `wait_seconds` until it reports `success`. Never reuse a code
from an earlier attempt; on `error`, start again. The same sign-in by script
on the machine prints the link and code and waits until it is approved:

```sh
sudo swarmctl setup codex-login --timeout 15m
```

The alternative is an API key the person adds themselves: on the setup page,
or `sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh key PROVIDER < key-file`.
Never ask for a key in chat. Tokens and keys stay in Swarm's credential store.
Swarm then gives every role its recommended model; do not overwrite them with
fixed models.

This signs Swarm into a model provider. It does not grant an MCP client access,
create an OpenAI tunnel, or substitute for a tunnel's runtime API key.
