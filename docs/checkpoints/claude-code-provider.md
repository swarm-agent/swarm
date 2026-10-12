# Claude Code provider (`claudecode`)

Swarm can run turns through the Claude Code CLI installed on the daemon's machine. Swarm never performs a Claude sign-in and never reads, stores, or forwards Claude credentials. You sign in yourself through Anthropic's own flow, and Swarm only starts the unmodified `claude` binary.

## Setup

1. Install Claude Code 2.1.248 or later. That version added `--restricted`, which Swarm requires.
2. As the OS user that runs `swarmd`, sign in with `claude auth login`. For an installed system service, run it as that service account. If that account needs a dedicated config directory, set `CLAUDE_CONFIG_DIR` both for that command and in the daemon's environment.
3. If `claude` is not on the daemon's `PATH`, set `SWARM_CLAUDE_CODE_CLI_PATH` in the daemon's environment to the executable.
4. In Settings > Auth, choose **Claude Code**, then **Claude Code CLI login**, then **Save and make active**. This enables the provider for your Swarm account. Each account opts in separately, and the record holds no secrets.

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

## Limits

- Media attachments are not supported, and fail closed.
- Text Claude writes between tool calls in the same turn streams into one assistant message.
- Subscription plan limits apply. Anthropic sizes Pro and Max limits for ordinary individual use.
