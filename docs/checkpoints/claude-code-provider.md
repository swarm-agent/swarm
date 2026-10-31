# Claude Code provider (`claudecode`)

Swarm can run turns through the Claude Code CLI installed on the daemon's machine. Swarm never performs a Claude sign-in and never reads, stores, or forwards Claude credentials. You sign in yourself through Anthropic's own flow, and Swarm only starts the unmodified `claude` binary.

## Setup

The CLI and its sign-in belong to the OS user that runs `swarmd`, not to the person's own account. Never copy the person's own Claude login into Swarm's account.

### Headless install (installer and MCP)

#### Update Swarm

Read `swarm_status` → `machine.update.command`: on a standard headless install,
`sudo bash /var/lib/swarm-headless/install.sh update`. The person (or their AI
running as the person, with sudo) runs this on the machine; it waits until no
job runs and Swarm restarts by itself. The installer is retained at
`$STATE/install.sh`; the generated daemon unit sets `SWARM_INSTALLER` to it.
Without that variable, status omits the command and instructions rather than
guessing an installation method.

#### Connect Claude Code

1. On the machine, in a terminal: `sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh claude-code`. It finds a `claude` the `swarm` service user can run (or installs one with Anthropic's installer into `/var/lib/swarm/.local/bin`), updates one older than 2.1.248, sets `SWARM_CLAUDE_CODE_CLI_PATH` through `install.sh daemon-env` (one restart, only when the path changes), and runs `claude auth login` as that user. The person opens the link it shows and finishes in their browser. Without a terminal it does everything else and exits non-zero at the sign-in step. Running it again is safe and reports the current state.
2. The person's AI calls the Swarm Control tool `swarm_connect_claude_code`. Action `status` reports `state` (`not_installed`, `not_working`, `needs_update`, `signed_out` or `signed_in`), the CLI version, `signed_in` and `provider_active`. When the CLI is not ready it also returns the installer command (`run_on_the_machine`). Action `activate` requires `signed_in`. It saves and activates the account's Claude Code CLI login record through the same handler as the setup page's **Save and make active** (`POST /v1/auth/credentials`, type `cli`, active). It needs no owner password: a write AI key (`swarm:write`), or `admin` for a token without Swarm Control levels, the same authority as `swarm_connect_chatgpt`. It is local only and never offered over the relay.

### Other installs

1. Install Claude Code 2.1.248 or later. That version added `--restricted`, which Swarm requires.
2. As the OS user that runs `swarmd`, sign in with `claude auth login`. For an installed system service, run it as that service account. If that account needs a dedicated config directory, set `CLAUDE_CONFIG_DIR` both for that command and in the daemon's environment.
3. If `claude` is not on the daemon's `PATH`, set `SWARM_CLAUDE_CODE_CLI_PATH` in the daemon's environment to the executable.
4. Enable the provider for your Swarm account: `swarm_connect_claude_code` action `activate`, or in Settings > Auth choose **Claude Code**, then **Claude Code CLI login**, then **Save and make active**. Each account opts in separately, and the record holds no secrets.

On a headless box from `install.sh`, the daemon runs as the `swarm` user (home `/var/lib/swarm`), so by hand step 2 is `sudo -u swarm -H claude auth login`, and step 4 on the setup page is provider `claudecode`, key type **Claude Code CLI login** (the key field is not used for this type); the installer command and `swarm_connect_claude_code` above replace both. The person still completes the sign-in themselves. `swarm_list_providers` returns these steps in the provider's `connect` field, with the installer command first and `tool: swarm_connect_claude_code` when the key may call it, next to the other options (ChatGPT sign-in, API keys, Vertex AI).

Swarm checks readiness with `claude --version` and `claude auth status`. The provider status shows the result.

## How a turn runs

Each Swarm provider request is one non-interactive Claude Code turn: `claude -p` with stream-json input and output.

**Isolation from Claude Code's own setup:**
- `--restricted` ignores user, project, and local settings files, and their hooks.
- `--tools ""` removes every built-in Claude Code tool.
- `--strict-mcp-config` and `--disable-slash-commands` keep out other MCP servers and commands.
- `--no-session-persistence` saves no transcript.

**Tools and permissions:**
- Swarm's tools are exposed only through a per-turn MCP server on `127.0.0.1`, which requires a bearer token.
- Every call runs through Swarm's tool invoker, so Swarm permissions, approvals, and durable V3 records apply.
- Claude Code itself pre-approves only that server (`--permission-mode dontAsk`).

**Conversation state:**
- Swarm sends the complete conversation on every request (`stateless_full_input`). The V3 session stays the only conversation authority.
- The system prompt and bridge config are written to `0600` files in a per-turn temporary directory, which is removed after the turn. They never appear on the command line.

**Environment:** the child process gets only an allowlisted environment. `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, and `CLAUDE_CODE_OAUTH_TOKEN` are never passed.

**Models and thinking:**
- Models come from the Anthropic catalog through the normal account-scoped model settings.
- When the catalog window is larger than 200k, Swarm asks Claude Code for the 1M variant (`<model>[1m]`). Opus 4.6 and Sonnet 4.6 otherwise run with 200k in Claude Code while Swarm compacts against 1M. Usage records name the catalog model.
- Swarm thinking levels map to `--effort`. `ultra` maps to `max`, and `off` disables extended thinking.

**Usage:**
- Recorded token usage is from the turn's final model call, the same per-step context-occupancy semantics other providers use.
- The CLI's aggregate turn usage and its client-side cost estimate are kept only in the raw usage record. That estimate is not billing data.

## Usage limits (`/claude`)

`/claude` in the terminal UI (and `GET /v1/claudecode/usage`) shows the Claude Code version, sign-in and plan, and the latest five-hour and weekly usage Claude Code reported in a turn's stream (its `rate_limit_event`). The usage is known only after a Claude Code turn has run since the daemon started. The Claude login belongs to the daemon's OS user, so the reading covers every Swarm account on that daemon.

When Claude Code is signed in with a Claude account, the route's `cli` also names that account: `account_email`, `account_uuid`, `organization_name` and `organization_uuid`. They come from `oauthAccount` in Claude Code's global config (`.claude.json` in `CLAUDE_CONFIG_DIR`, otherwise in the daemon user's home). Swarm reads only those identity fields, never a token or key, and leaves out any field the config does not have. `manage_setup` `providers` (the `claudecode` entry) and `plan_usage` (`claude_code`) carry the same fields, including for a linked machine, so machines that share one Claude account can be told apart from machines that do not.

## Limits

- Media attachments are not supported, and fail closed.
- Text Claude writes between tool calls in the same turn streams into one assistant message.
- Subscription plan limits apply. Anthropic sizes Pro and Max limits for ordinary individual use.
