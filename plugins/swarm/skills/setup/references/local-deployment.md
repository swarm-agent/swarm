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
sudo bash /var/lib/swarm-headless/install.sh connect-ai
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

`swarm_list_providers` shows which providers are ready; each one that is not
has a `connect` field with the exact steps for this connection. If none is
ready, ask the person which they have, present all of these options in one
short list with what each needs, and follow their choice:

| Option | Provider | What it needs |
| --- | --- | --- |
| ChatGPT plan | `codex` | A ChatGPT plan; the person signs in in their own browser. |
| Claude plan through Claude Code | `claudecode` | Claude Code 2.1.248+ on this machine, signed in by the person as Swarm's user. |
| API key | `anthropic`, `openai`, `google`, `openrouter`, `fireworks` | A key the person adds themselves. |
| Vertex AI | `vertex` | A Google Cloud identity on this machine with `roles/aiplatform.user`. |

Never ask for a key in chat, and never handle Claude credentials. Tokens and
keys stay in Swarm's credential store. Once a provider is ready, Swarm gives
every role its recommended model; do not overwrite them with fixed models.

### ChatGPT plan

`swarm_connect_chatgpt` with action `start` returns a link and a one-time
code. Give both to the person at once; they approve in their own browser. Then
call action `status` with the `login_id` and `wait_seconds` until it reports
`success`. Never reuse a code from an earlier attempt; on `error`, start
again. The same sign-in by script on the machine prints the link and code and
waits until it is approved:

```sh
sudo swarmctl setup codex-login --timeout 15m
```

### Claude Code

Swarm runs the person's own signed-in Claude Code CLI and never sees, stores
or forwards Claude credentials. The person signs in themselves; Swarm never
copies their own Claude login.

On the box, the installer does steps 1 and 2 in the person's terminal, and
`swarm_connect_claude_code` does step 3 (action `status` until `signed_in`,
then action `activate`; no setup page or owner password):

```sh
sudo bash /var/lib/swarm-headless/install.sh claude-code
```

Or the person does all three steps by hand:

1. Install Claude Code 2.1.248 or later where Swarm's daemon finds it: on its
   `PATH` (for example `/usr/local/bin`), or named by
   `SWARM_CLAUDE_CODE_CLI_PATH` in the daemon's environment.
2. Sign in as the OS user that runs Swarm. On the box that user is `swarm`:

   ```sh
   sudo -u swarm -H claude auth login
   ```

3. Turn it on for their Swarm account on the setup page: provider
   `claudecode`, key type **Claude Code CLI login**. That record holds no
   secret; the page's key field is not used for this type (type any word,
   never a real key).

### API key

The person adds it on the setup page, or, as the first provider, by script on
the machine:

```sh
sudo bash /var/lib/swarm-headless/install.sh key PROVIDER < key-file
```

With a full AI key, `swarm_request_provider_key` gives them a one-time link to
paste it on instead.

### Vertex AI

Only on a machine with a Google Cloud identity (its attached service account,
or `GOOGLE_APPLICATION_CREDENTIALS`). No key is stored:

```sh
sudo swarmctl setup credential --provider vertex --project PROJECT
```

(`setup add-credential` adds it next to an existing provider.)

This signs Swarm into a model provider. It does not grant an MCP client access,
create an OpenAI tunnel, or substitute for a tunnel's runtime API key.
