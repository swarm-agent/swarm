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

## Several accounts on one machine

The existing daemon-user login is account `default` (order 1); it is not moved.
Add another isolated slot on that machine:

```sh
swarmctl setup add-credential --provider claudecode
```

Run the exact printed `sudo -u … -- env CLAUDE_CONFIG_DIR=… claude auth login`
command yourself, then complete Claude's browser sign-in. Each new config
folder is private (`0700`) under the daemon data root's
`providers/claudecode/accounts/<id>`. Swarm creates the folder and routing
metadata only; the Claude CLI owns the login. Enable Claude Code for your
Swarm account as described above if it is not already enabled.

Provider listings and `plan_usage` include `accounts`: slot ID, order, readiness,
email, organization, plan, `limit_state` (`ok` or `exhausted`) and the observed
`limit_until` timestamp. Linked-machine provider listings carry the same data.
To reorder, supply every slot ID exactly once, including `default`:

```sh
swarmctl setup credential-order --provider claudecode default,<second-id>,<third-id>
```

The owner-only setup API is `POST /v1/claudecode/accounts`: `{}` adds a slot
and returns `id` and `login_command`; `{"order":["default","<id>"]}` changes
order. Order changes apply to the next dispatched turn.

Each turn starts with the first signed-in account outside a known limit window.
A session/weekly limit with a parseable reset records that clock for its slot
and retries the turn on the next account. Unknown reset text is an error, not
an invented window. After tool execution, the run first rebuilds its request
from durable history to avoid replaying tool side effects. If all usable
accounts are limited, it waits cancellably until the earliest recorded reset.
Account selection and waiting appear as provider events; status attention names
the number of limited accounts and the next reset (UTC).

This revision still seeds each CLI turn from Swarm's full durable history and
disables CLI transcript persistence. Per-account CLI resume IDs are deferred to
the standing-endpoint task `task_5e37e037`; account switching does not rely on a
transcript from another config directory.

## How a turn runs

Each Swarm provider request is one non-interactive Claude Code turn: `claude -p` with stream-json input and output.

**Isolation from Claude Code's own setup:**
- `--restricted` ignores user, project, and local settings files, and their hooks.
- `--tools ""` removes every built-in Claude Code tool.
- `--strict-mcp-config` and `--disable-slash-commands` keep out other MCP servers and commands.
- CLI session persistence is enabled so later turns can resume the conversation.

**Tools and permissions:**
- The CLI connects to the daemon's standing `http://127.0.0.1:<configured API port>/mcp` endpoint (or the configured IPv6 loopback address), not a per-turn server. Use a fixed daemon API port to preserve the URL across restarts.
- A private `--mcp-config` file is created on the session's first Claude Code turn and reused. Its Authorization header contains a session-bound internal scoped token; the token is sealed in Swarm's existing credential store and is not invalidated on restart. It cannot authorize other sessions or ordinary API routes.
- `tools/list` is the current run's resolved inventory: the Orchestrator catalog or a task agent's worktree tools. An inactive session refuses execution until its next run attaches the canonical executor.
- Every call runs through Swarm's tool invoker, so Swarm permissions, approvals, and durable V3 records apply.
- Claude Code itself pre-approves only that server (`--permission-mode dontAsk`).

**Conversation state:**
- The first turn seeds the CLI from Swarm's durable history. Swarm records the CLI session ID from `system/init` on the V3 session; later turns use `--resume <id>` and send only new user input, not the conversation again.
- If the CLI explicitly refuses a missing session before initialization, Swarm starts one fresh session seeded from durable history and records its new ID. Other provider failures do not trigger a history replay.
- The system prompt and MCP config are `0600` files in a private session directory under the configured daemon data root. Tokens and prompt contents never appear on argv.
- A daemon shutdown may stop the CLI process. The next turn reuses its recorded CLI session ID, endpoint and token. Transient HTTP disconnects remain the CLI's reconnect/retry responsibility; Swarm does not replace the endpoint or credentials per turn.

**Environment:** the child process gets only an allowlisted environment. `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, and `CLAUDE_CODE_OAUTH_TOKEN` are never passed.

**Models and thinking:**
- Models come from the Anthropic catalog through the normal account-scoped model settings.
- When the catalog window is larger than 200k, Swarm asks Claude Code for the 1M variant (`<model>[1m]`). Opus 4.6 and Sonnet 4.6 otherwise run with 200k in Claude Code while Swarm compacts against 1M. Usage records name the catalog model.
- Swarm thinking levels map to `--effort`. `ultra` maps to `max`, and `off` disables extended thinking.

**Usage:**
- Recorded token usage is from the turn's final model call, the same per-step context-occupancy semantics other providers use.
- The CLI's aggregate turn usage and its client-side cost estimate are kept only in the raw usage record. That estimate is not billing data.

## Validation boundary

Deterministic tests use a fixture CLI and real loopback HTTP, credential storage, V3 session mutations, and the canonical tool executor. They check config/token reuse after store reopen, resume and missing-history reseeding, cross-session rejection, resolved Orchestrator/Coder inventories, permission denial without a tool side effect, and one in-flight disconnect followed by a successful retry at the same address. The fixture explicitly requests that retry; this does not establish Anthropic's real CLI reconnect policy. Provider-backed daemon-restart qualification remains a separate live check.

## Usage limits (`/claude`)

`/claude` in the terminal UI (and `GET /v1/claudecode/usage`) shows the Claude Code version, sign-in and plan, and the latest five-hour and weekly usage Claude Code reported in a turn's stream (its `rate_limit_event`). The usage is known only after a Claude Code turn has run since the daemon started. The Claude login belongs to the daemon's OS user, so the reading covers every Swarm account on that daemon.

When Claude Code is signed in with a Claude account, the route's `cli` also names that account: `account_email`, `account_uuid`, `organization_name` and `organization_uuid`. They come from `oauthAccount` in Claude Code's global config (`.claude.json` in `CLAUDE_CONFIG_DIR`, otherwise in the daemon user's home). Swarm reads only those identity fields, never a token or key, and leaves out any field the config does not have. `manage_setup` `providers` (the `claudecode` entry) and `plan_usage` (`claude_code`) carry the same fields, including for a linked machine, so machines that share one Claude account can be told apart from machines that do not.

## Limits

- Media attachments are not supported, and fail closed.
- Text Claude writes between tool calls in the same turn streams into one assistant message.
- Subscription plan limits apply. Anthropic sizes Pro and Max limits for ordinary individual use.
