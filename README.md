# swarmtui (Go + tcell)

Component-first Swarm TUI refactor playground.

Current scope is a **backend-connected Home page + Chat demo page**:
- live workspace state from `swarmd`
- `/` command system (`/workspace`, `/workspaces`, `/model`, `/auth`, `/keybinds`, `/reload`, `/help`)
- `/header` command to toggle chat header visibility (`on|off|toggle|status`)
- `/themes` command for hot-swapping full UI theme colors (`list|set|next|prev|status|create|edit|delete|slots`)
- custom themes with persistence (`/themes create`, `/themes edit`, `/themes delete`)
- codex auth key update from inside TUI
- model selection + thinking updates from inside TUI
- backend providers include `codex`, `google`, `copilot`, `fireworks`, and `openrouter` (`exa` remains search-only)
- fuzzy workspace tree scan with `#<n>` pick shortcuts
- centered prompt card, quick action row, hint/tip lines, recent session list

Press `Enter` on a non-empty home prompt to open a dedicated **Chat demo page** with:
- streamed assistant responses
- unified timeline (chat + tool stream in one flow)
- tool stream states (pending/running/completed) inline with messages
- bash spinner demo (`⋮ ⋰ ⋯ ⋱`)
- context meter in sidebar
- bottom input + model presets row
- bottom swarm bar with tabs
- workspace sidebar (path/workspace/git), hidden on narrow terminals

Default theme is **Nord** (mapped from Swarm's `nord.json`) with 20 built-in themes including `crimson`.

Install from a GitHub release:

1. Download the versioned `swarm-<version>-linux-amd64.tar.gz` release asset from GitHub Releases.
2. Extract it.
3. Install it with:

```bash
./swarmsetup --artifact-root /path/to/extracted/swarm-<version>-linux-amd64
```

That installs the real Swarm runtime layout and launchers so the user can open the installed app.

Release bundles built by `./scripts/build-main-dist.sh --version <version>` embed the same version metadata into the launcher binaries and `swarmd`/`swarmctl`, so installed update status matches the shipped release version.

For `main` releases, if the promoted commit is not already tagged with an exact stable version, `build-main` auto-creates the next patch tag from the latest stable release (for example `v0.1.0` -> `v0.1.1`).

After install, launch Swarm with either:

```bash
swarm
```

or, if your current shell does not have `${XDG_BIN_HOME:-$HOME/.local/bin}` on `PATH` yet:

```bash
${XDG_BIN_HOME:-$HOME/.local/bin}/swarm
```

Other common commands:

```bash
swarm --desktop
swarm server on
swarm server status
swarm server off
swarm dev
swarmdev
```

Launchers are installed into `${XDG_BIN_HOME:-$HOME/.local/bin}` and point at installed runtime artifacts under `${XDG_DATA_HOME:-$HOME/.local/share}/swarm/{bin,libexec,share}`.

## Run

```bash
swarm                     # open the main app
swarm --desktop           # open the desktop app
swarm server on           # start backend without opening UI
swarm server status       # show backend status
swarm server off          # stop backend
swarm dev                 # open the dev lane
swarmdev                  # dev alias
```

By default:
- `swarm` uses the main lane backend at `http://127.0.0.1:7781`
- `swarm dev` / `swarmdev` use the dev lane backend at `http://127.0.0.1:7782`
- `swarm --desktop` uses desktop port `5555`
- `swarm dev --desktop` uses desktop port `5556`

You can also override the backend target directly:

```bash
SWARMD_URL=http://127.0.0.1:7782 SWARMD_TOKEN=<token> swarm dev
```

Installed files live under:
- `${XDG_BIN_HOME:-$HOME/.local/bin}` for launchers
- `${XDG_DATA_HOME:-$HOME/.local/share}/swarm/{bin,libexec,share}` for runtime files
- `${XDG_STATE_HOME:-$HOME/.local/state}/swarm/...` for runtime state

Use `/mouse on` and `/mouse off` at runtime to toggle mouse capture without restarting.
When mouse capture is enabled, use your terminal's selection modifier (typically `Shift+drag`) to select/copy text.

- `Ctrl+C`: quit
- `Enter` on Home with prompt text: open Chat demo page
- `Enter` on Home with `/command`: execute command in-place
- `Enter` on Home with `/...` and palette visible: runs selected command immediately
- type `/` on Home: open command palette suggestions above the prompt
- `Tab` on Home with `/...`: autocomplete selected command
- `Up/Down` on Home with `/...`: navigate command suggestions
- `/sessions` on Home: open sessions modal
- `/auth` on Home: open auth manager modal
- in auth modal: `Enter` on provider opens that provider's auth flow; for `copilot` it opens a chooser for `copilot login`, `gh auth`, or direct GitHub token
- in auth modal: `n` add token/API key, `o` add OAuth or provider login flow, `e` edit, `a` set active, `d` delete, `r` refresh, `l` login/method chooser
- in auth modal: `/` provider search, `f` credential search, `Tab` focus switch, `Esc` close
- `Esc` in Chat page: return to Home
- type `/` in Chat: open command palette suggestions above the chat input
- `Enter` in Chat with prompt text: replay demo with new prompt
- `Enter` in Chat with `/...` and palette visible: runs selected command immediately
- `Enter` in Chat with `/command`: execute slash command in chat (for example `/header off`)
- `g` in Chat: open/close full diff gallery (10 variants)
- `j/k` or `Up/Down` in diff gallery: scroll
- mouse wheel in Chat: scroll timeline; in diff gallery: scroll diff rows
- `[` / `]` in Chat: switch swarm bar tab highlight
- `Up/Down` or `j/k`: select recent session
- `Enter`: mock open selected session
- type text: edit prompt line
- `Backspace`: delete prompt chars
- `Ctrl+U`: clear prompt
- click a recent session row: select it
- click workspace chips in the top bar: switch active workspace
- click top-bar objects (`git`, `agents`, `plan`, `mode`, `sessions`): mock panel actions
- `Ctrl+R` on Home: hot reload home state from backend
- `/header` on Home: toggle chat header visibility for new chat sessions
- `/mouse` on Home/Chat: toggle mouse capture (`/mouse status` for current state)
- `/themes` on Home/Chat: open theme modal (live preview while moving selection)
- `/themes create <id> [from <theme>]`: create a persistent custom theme
- `/themes edit <id> <slot> <#RRGGBB>`: modify custom theme colors
- `/themes delete <id>`: remove a custom theme
- `/themes slots`: list editable color slots
- in themes modal: `Up/Down` (or `j/k`) live preview, `Enter` apply, `Esc` cancel/revert preview
- `/keybinds` on Home/Chat: open keybind manager modal
- in keybinds modal: `Enter` edit selected, press any key to bind, `Esc` cancel/close, `r` reset selected, `Shift+R` reset all
- `F8`: toggle mouse capture
- Chat focus strip is centered and fixed: `[a:...] [m:...] [t:...]`
- Context usage is shown separately at the bottom-right: `used:limit` (for example `0:400.0k`)

## Dev lane metadata for AI/testing

- Use `swarm dev info` to print lane listen/url/state/log metadata.
- Use `swarmdev info` as shorthand for dev lane metadata.
- `swarm dev` and `swarmdev` launch the installed dev-lane runtime; use `rebuild dev` to refresh it from the current branch.
- `swarm` and `swarmdev` refuse to launch when the installed runtime is missing (no silent fallback to source builds).
- Lane occupancy/port records are written to user state (`${XDG_STATE_HOME:-~/.local/state}/swarm/ports/swarmd-main.env`, `${XDG_STATE_HOME:-~/.local/state}/swarm/ports/swarmd-dev.env`).

`swarmtui` now persists UI settings through the daemon-backed `/v1/ui/settings` API backed by Pebble.
There is no local `swarmtui.json` file anymore.

Current settings include:
- `chat.show_header`
- `chat.thinking_tags`
- `chat.tool_stream`
- `input.mouse_enabled`
- `input.keybinds`
- `ui.theme`
- `ui.custom_themes`
- `swarming.title`
- `swarming.status`

`SWARMTUI_THEME` overrides `ui.theme` for that process only.
