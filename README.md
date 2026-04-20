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

First-run local setup from a fresh machine:

```bash
bash /path/to/swarm/setup --start-main
```

This verifies required host tools, including the vendored FFF runtime used by Swarm's canonical in-app `search` tool, builds both `main` and `dev` lane binaries plus the compiled launcher tools, installs `swarm` / `swarmdev` / `rebuild` / `swarmsetup` into `${XDG_BIN_HOME:-$HOME/.local/bin}`, and starts the local `main` backend when `--start-main` is used.
Use `bash /path/to/swarm/setup --with-web --start-main` if you also want to install `web/` npm dependencies, build the desktop bundle, and place the built desktop assets into the installed runtime layout during first-run setup.
After the first launcher install, use `rebuild` from the checkout to refresh the installed runtime artifacts, then launch the installed `swarm` binary to test the same runtime a downloaded install would use.

For isolated local container/replicate harness work, provision the dedicated `swarm-harness` VM lane with `./scripts/swarm-harness-vm.sh provision`, then run `./scripts/swarm-harness-vm.sh local-replicate` or `./scripts/swarm-harness-vm.sh local-replicate-recovery`.

## Run

Or run through the launcher:

```bash
cd /path/to/swarm
bash ./scripts/install-launchers.sh
swarm                     # main lane TUI (127.0.0.1:7781)
swarm dev                 # dev lane  TUI (127.0.0.1:7782)
swarm --desktop           # main lane desktop web app (built assets via swarmd)
swarm dev --desktop       # dev lane desktop web app (built assets via swarmd)
swarm server on
swarm server status
swarm server off
swarmdev                  # dev alias
```

Launchers never auto-build on launch; use `rebuild` to refresh the installed runtime artifacts from source.
Use `rebuild f` when you also want fresh installed desktop assets.
Use `./scripts/rebuild-container.sh` for the container MVP rebuild path; it rebuilds local binaries and `web/dist` for the image without shutting down the local lane daemon first.

Launchers are installed into `${XDG_BIN_HOME:-$HOME/.local/bin}` and point at installed runtime artifacts under `${XDG_DATA_HOME:-$HOME/.local/share}/swarm/{bin,libexec,share}`.

To repoint `swarm` (main-default launcher) and install `swarmdev` plus `rebuild` into `${XDG_BIN_HOME:-$HOME/.local/bin}`:

```bash
cd /path/to/swarm
bash ./scripts/install-launchers.sh
```

The TUI talks to `swarmd` at `http://127.0.0.1:7781` on the `main` lane by default.
Use `swarm dev` (or `swarmdev`) for the isolated `dev` lane (`http://127.0.0.1:7782`).
Use `swarm --desktop` to open the main-lane desktop served by `swarmd` on `http://127.0.0.1:5555` by default.
Use `swarm dev --desktop` (or `swarmdev --desktop`) to open the dev-lane desktop served by `swarmd` on `http://127.0.0.1:5556` by default.
Use `swarm server on|off|status` for detached backend lifecycle without opening the desktop.
The barebones React+Vite web placeholder lives under `web/`; for local web-only work, run `cd web && npm install && npm run dev`.

Override with:

```bash
SWARMD_URL=http://127.0.0.1:7782 SWARMD_TOKEN=<token> swarm dev
```

Use `swarm dev info` to print the dev lane URL/port/log metadata for AI-driven E2E sessions.

Use `/mouse on` and `/mouse off` at runtime to toggle without restarting.
When mouse capture is enabled, use your terminal's selection modifier (typically `Shift+drag`) to select/copy text.


## Dual-lane launcher (main/dev)

Use the launcher for isolated lanes:

```bash
cd /path/to/swarm
swarm main server on
swarm dev server on
swarm --desktop
swarm dev --desktop --port 5556
swarm dev info
swarmdev info
```

- `main` uses the configured startup backend port; `dev` uses the next backend port by default so the lanes stay isolated (`7781`/`7782` with the default config).
- `swarm.conf` now also carries `desktop_port`, which `swarm --desktop` uses as the main-lane default; `dev` uses the next port by default unless overridden with `--port` or `SWARM_DESKTOP_PORT`
- `swarm --desktop` opens the built desktop served by `swarmd`
- `swarm dev --desktop` opens the same built desktop runtime against the dev lane backend
- `swarm server on|off|status` manages the detached backend only
- installed bundle paths are lane-agnostic under user storage (`${XDG_DATA_HOME:-~/.local/share}/swarm/bin`, `${XDG_DATA_HOME:-~/.local/share}/swarm/libexec`, `${XDG_DATA_HOME:-~/.local/share}/swarm/share`)
- launcher/runtime state and data remain lane-scoped under user storage (`${XDG_STATE_HOME:-~/.local/state}/swarm/swarmd/main`, `${XDG_STATE_HOME:-~/.local/state}/swarm/swarmd/dev`, `${XDG_DATA_HOME:-~/.local/share}/swarmd/main`, `${XDG_DATA_HOME:-~/.local/share}/swarmd/dev`)


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

## Git branch flow

- `dev` is the day-to-day working branch.
- `main` is the protected release/build branch.
- Merge or cherry-pick from `dev` into `main` only when you want the canonical GitHub build to run.
- GitHub Actions now builds artifacts only for pushes to `main` (and manual dispatch), and the workflow is gated so only the `swarm-agent` account can run the build job.
- Protect `main` in GitHub so only your account can push to it; leave `dev` available for normal collaborative work.
- Recommended GitHub rule setup:
  - branch rule / ruleset for `main`
  - block force pushes
  - block deletions
  - restrict direct pushes to your account
  - optionally require pull requests for everyone except your admin bypass

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
