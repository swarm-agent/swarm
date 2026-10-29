# Swarm Console

A native desktop app (Rust, [GPUI](https://github.com/zed-industries/zed/tree/main/crates/gpui))
for running a Swarm fleet through its Orchestrator.

**Status: works on a real Swarm machine.** After onboarding, the console
follows the machine (this PC, or a server over SSH): projects, their
Orchestrator conversations (markdown) with the tasks they start as cards,
the machine, agent roles and models, and
usage. Type in a pane to start a conversation or follow one up (or answer the
Orchestrator's question); approve, reject and merge on a card, stop or allow
a tool call in the header. Drag a session, or a pane by its header, onto a
pane's edge to split there or its middle to show it there; drag dividers to
resize. The Orchestrator can use your own Chrome on a lease you grant (see
below). Media shows what the media tasks made (from their deliverables), and linked machines show through the home Swarm (`/v3/fleet…`). `SWARM_CONSOLE_DEMO=1` shows the design set instead.

## Run

```sh
cargo run
```

Linux needs X11 or Wayland. If the linker cannot find `libxkbcommon-x11` or
`libX11-xcb`, `build.rs` links their runtime libraries; installing
`libxkbcommon-x11-dev` and `libx11-xcb-dev` removes the need.

`cargo test` needs no display. On Debian or Ubuntu it links with
`build-essential`, `pkg-config`, `libxcb1-dev`, `libxkbcommon-dev` and
`libxkbcommon-x11-dev`; Swarm's default sandbox image
(`containers/sandbox/Dockerfile`) has these and Rust.

`SWARM_CONSOLE_CONCEPT=graphite|paper|phosphor` picks the starting concept.
`scripts/snap.sh OUT.png [concept] [page]` screenshots it on a virtual display
(needs `xvfb` and `i3`: a private i3 runs inside the virtual display, because
GPUI only paints under a window manager), so it never opens a window on your
screen.

## First run

Without a session for its machine, the app opens onboarding: one centered
card that finds Swarm (`GET /healthz`, no session), installs it or connects
to it (Swarm mints the app an owner session with `swarmctl client connect`),
connects a model provider (ChatGPT sign-in, or an API key typed here and sent
to Swarm once), creates the first project from folders you pick (`swarmctl
workspace add` makes them visible to Swarm in one call, mounting them into
the running Swarm when it can; the first folder in a home folder still
restarts Swarm once, waiting until it is idle) and waits for Swarm to write
project.md.

Root steps on this computer run through `sudo`: `sudo -n` first (a cached sudo
or a NOPASSWD rule needs nothing), otherwise the app's own password prompt,
which names the exact command, and passes the password to `sudo -S` on its
stdin only. The password is never stored, logged or kept after the call, and
no polkit agent is needed. On a server, root steps are `sudo -n` over SSH.

On the Projects page, folders Swarm cannot read yet show "Give Swarm access":
one root step for all of them, then the same folders are added to the project
again. The exact command stays one click away for people who run it
themselves.
Agents work only in a Git repository with a first commit, so the app readies
each folder the way Swarm's web UI does: an empty folder gets an empty
first commit from Swarm (`/v1/workspace/repository/setup`); a folder with
files shows them and you pick the first commit
(`/v1/workspace/repository/review` and `/baseline`). A folder without Git
gets an empty repository as you, at registration (`swarmctl workspace add`),
so your own Git keeps working in it.

Settings has a Providers group, Swarm's record read again after each
action (`/v1/providers`, `/v1/auth/credentials` and its `active`, `verify`
and `delete` routes, `/v1/auth/codex/oauth/*`): each provider's readiness
and reason; its keys, named, several per provider, the active one marked,
with type, last four, added and expiry dates; add key, use, verify, remove;
the ChatGPT device sign-in. Swarm checks a key with its provider before
keeping it, and a refused key stays in the form.

The app is a client of Swarm the way Swarm's own web UI is: its HTTP API as
the owner (`X-Swarm-Token`) and its realtime WebSocket, with the agent's
text as it streams. Never MCP, never an AI key (see AGENTS.md). The session
for this PC lives in `~/.config/swarm/clients/swarm-console.token` (written
by `swarmctl` for you) and sessions for servers in
`~/.config/swarm-console/machines/HOST.token` (`HOST_PORT.token` when SSH is not on port 22), all 0600. For a server it opens
its own SSH tunnel to Swarm's loopback-only API. `sudo swarmctl client list`
and `revoke` on the machine show and end them.

| Variable | Use |
| --- | --- |
| `SWARM_CONSOLE_ONBOARDING=1` | always start with onboarding |
| `SWARM_CONSOLE_SKIP_ONBOARDING=1` | never |
| `SWARM_CONSOLE_DEMO=1` | the design set instead of a Swarm machine (screenshots use it unless `SNAP_LIVE=1`) |
| `SWARM_CONSOLE_ONBOARDING_DEMO=STAGE` | show one step with sample data (`scripts/snap.sh` lists them) |
| `SWARM_CONSOLE_SSH=user@host:port`, `SWARM_CONSOLE_SSH_KEY` | use a server instead of this PC |
| `SWARM_CONSOLE_INSTALLER=PATH`, `SWARM_CONSOLE_INSTALL_ARGS="..."` | install from a local installer, e.g. a swarm-go checkout with `--ref BRANCH` |

## Your browser, on a lease

The Orchestrator can use your own Chrome through this app, the way Claude
in Chrome works, but only inside a lease you grant per session. Say "use
swarm browser to …" and it calls Swarm's `request_browser_access` (reason,
sites, minutes); the card in the conversation shows them and you pick a
level: **ask** (every action waits for you), **confirm** (reading, moving
and typing run; paying, publishing, keys and account settings wait),
**full**, or deny. Swarm keeps the lease on the session
(`POST /v3/sessions/{id}/access`: `browser`, `browser_sites`,
`browser_until`), tells the Orchestrator every turn, and refuses
`manage_browser` outside it. The shield shows the lease and ends it; so
does Settings → Browser, the extension's popup and the banner in a leased
tab (stop there ends every lease in Swarm).

Each `manage_browser` call (open, read, find, click, type, back, tabs) goes
from Swarm to this app, over a socket only you can reach
(`$XDG_RUNTIME_DIR/swarm-console/browser.sock`) to Chrome's native
messaging host (`swarm-console --browser-host`, a script the app writes in
`~/.config/swarm-console`), to the Swarm Browser extension
(`extension/`, plain MV3). The extension acts only in tabs it opened in the background, never taking
focus from what you are doing (kept in a "Swarm · LEVEL" tab
group, grey once the lease ends), on the leased sites or any site when you
allow it, until the lease ends. Asked again during a lease, the agent widens
it: the card shows the sites it adds; it reads the page as you see it
(hidden text is counted, not read), never reads password fields, hides
what looks like a key or token, and at confirm level holds anything that
looks critical (the hold shows as a card). What it reads comes back marked
as untrusted web content.

To connect: Settings → Browser → **connect Chrome** writes the host script
and the manifest for each Chrome family under `~/.config`; then load
`extension/` unpacked in `chrome://extensions` (Developer mode; its id is
fixed by the key in its manifest) and open its popup once.
`scripts/chrome-with-extension.mjs PROFILE extension/` starts a Chrome with
a separate profile and the extension loaded, for trying it out.

## Layout

- **Navigation:** Sessions, Media, Usage, Fleet, Agents and Settings at the
  top of the left sidebar. `«` (or ctrl-b) collapses the sidebar to an icon
  rail with the pages, the current project's sessions and fleet health; `»`
  expands it.
- **Fleet:** the one place to see the whole fleet. The top line counts it
  ("4 machines · 1 needs attention · 3 waiting on you · 2 failed today"; a
  count with `+?` has a machine that was not read). Then the machine cards,
  read-only: each is what that machine's own Swarm reports in
  `GET /v3/box/summary` (a linked machine's read through home at
  `/v3/fleet/machines/{name}/api/v3/box/summary`): status (ok, attention or
  degraded, with Swarm's reasons), build and update state, what runs now,
  today's runs and spend, uptime and sandbox; anything it does not report
  shows as "—", and a machine home cannot reach shows offline with its last
  contact. Selecting a card opens it in the right-hand inspector. Below,
  **Needs you**: one line per item across home and every linked machine,
  newest first, tagged with the machine: sessions waiting on a permission or
  a question (the summary's `attention.sessions`, each with what it asks from
  `GET /v3/sessions/{id}/permissions?status=pending`), tasks whose state is
  `needs_you` (`GET /v3/projects`, then each project's `/tasks`; the state
  is each row's `progress.task_state`, with its reason, as
  `docs/task-progress.md` describes; a landed, done, not integrated or
  stopped task is not listed, whatever its run `status`; only a Swarm that
  gives no state falls back to `pending_approval`, or `needs_review` of work
  you started), and runs that failed today (the summary's
  `runs.failed_runs`, with the error from
  `GET /v3/workers/{id}/runs/{run}`). At most 20 per machine and kind are
  listed; when Swarm counts more, or something could not be read, a note
  under the list says so. A home item opens here (the conversation, or the
  run on Automations); a linked machine's opens that machine's window, which
  opens the item once connected. Last, today's spend per machine and the
  fleet total ("unknown" where a machine did not report it). Everything is
  read on the fleet tick, every 20 seconds, off the UI thread.
- **Agents:** one role × machine grid: the first column is the setting on
  the connected Swarm (`/v1/agent-model-settings`, the only one changed
  here); each linked machine's cell says whether its own setting is the same,
  another model, or unknown. Selecting a role or cell opens it in the
  right-hand inspector.
- **Media, Usage, Settings:** generated media by kind; spend against limits by
  day, machine, role and session; home machine, connections, appearance and
  approvals.
- **Title bar:** Swarm's mark, then the open project as a menu (every
  project to switch to, "add folders to…", "new project…", and the Projects
  page), then the page and what is in focus (the open conversation).
- **Projects** (from the project menu): a tab
  per project, the open one first; the tab shows that project's folders and
  their roles and its project.md; open, rename (`PATCH /v3/projects/{id}`),
  edit project.md (the same route, `project_context`), add folders and new
  project, the last two through the onboarding's project step, which
  readies folders the way the first run does and adds them to the project
  (`workspaces` on the same route).
- **Left (Sessions):** the nav, then the open project's sessions. Every session is
  an Orchestrator conversation; coder, finder, designer and reviewer work shows
  up as tasks inside it. The status bar's fleet count opens the list of
  machines. Click opens a session in the focused pane, shift-click opens it to the
  right, alt-click opens it below.
- **Center:** split panes, side by side or stacked, each a live session.
- **Tasks, in a bar above the composer:** no status sidebar and no cards in
  the conversation. The bar says how many tasks run, need you, are queued or
  done; closed, it still shows each task waiting on a decision with its
  button (approve, reject, merge), and a running task's stop. Open, it lists
  every task with its activity and the background sessions behind it. Each
  row's badge is the task's state and reason (`progress.task_state`, e.g.
  "landed · pushed 5711ae410abc"), not its run status; a Swarm that gives no
  state shows its progress summary or run status as before.
- **Run states:** a session's glyph says how its run stands: `●` running,
  `◆` needs you, `◷` waiting, `○` idle, `✕` failed. Waiting is an
  Orchestrator asleep on `wait_tasks` (Swarm's run state `waiting_tasks`,
  `active: false`): no timer ticks, the pane header names the tasks it waits
  on (and the linked machine they run on) from the run state's `task_wait`,
  and the composer says a message from you ends the wait. It comes from the
  sync snapshot, so it survives a reconnect or restart; when the tasks settle
  or you write, the run shows as running again. The transcript shows the
  wait line Swarm records when the wait starts ("This conversation is now
  waiting on 2 task(s) on forge: …") and when it ends ("Wait ended: tasks
  settled.", "Wait ended by your message.", "Wait ended: stopped.") as a
  compact `⏳` status line, from history as well as live.
- **Composer:** beside send/stop a badge says when the run waits on tasks:
  `⏳ Waiting · 2 tasks on forge` (task count, and the linked machine). No
  badge while it runs (the stop button says so) or when idle. The text box
  grows by whole rows as it wraps, up to twelve rows. While Swarm says the run is
  active, send becomes stop (`POST /v3/sessions/{id}/run/stop`; the notice
  is Swarm's own answer). While it waits, send stays (a message ends the
  wait) and `■ stop wait` cancels it: Swarm's session detail lists no
  active run while it waits, so the console posts `run/stop` with the
  waiting run's `run_id` from the run state. Idle shows no hint text. The
  bottom row never overflows the pane: as it narrows, `/ commands` goes
  first, then the hint, then the badge shortens to `⏳ 2` (its full text on
  hover), then the permissions chip goes, then send/stop shows only `■` / `⏎`
  (names on hover), and the model name truncates. Send/stop sits outside the
  part of the row that clips, so the rest never pushes it out. Typing `/` lists the
  commands: `/new`, `/new bottom`, `/stop`, `/compact`, `/context`,
  `/theme`, `/usage`, `/settings`, `/media`, `/agents`, `/fleet`, `/live`;
  a unique prefix is enough (`/com`, `/con`; `/c` alone is ambiguous), tab
  completes, enter runs.
- **Context:** the pane header's `ctx` meter shows how much of the model's
  context window the conversation used at its latest provider request
  ("ctx 62% · 124k / 200k"; "— / 200k" before Swarm has measured it), from
  the session detail (`GET /v3/sessions/{id}`: `usage_summary`,
  `context_window`). It is read when a pane opens a conversation and after
  each finished turn, never on a timer, and takes the warning color once the context
  left is at or below the auto-compact threshold. Clicking it (or
  `/context`) opens a panel: tokens in use of the window, the latest
  request's input, cache, output and thinking tokens, the model, the last
  compaction (Swarm's checkpoint among the recent messages: origin, age,
  number) and the auto-compact threshold, with **Compact now**. Swarm
  reports totals only: what the prompt, project.md, the task list, memory,
  history or tool output each take is not in its answers, so the panel says
  so instead of guessing. `/compact [N%] [note]` reads like the web's
  (`/compact 5%`, `/compact threshold 5%`, `/compact 5% keep task ids`): a
  threshold first sets the session's `context_compaction_threshold_percent`
  (`POST /v3/sessions/{id}/metadata`; `0%` turns it off), then Swarm
  compacts (`POST /v3/sessions/{id}/compact` with the note). The result
  shows above the composer and in the status bar ("Compacted: 62% of
  context before; Swarm measures the new size on the next turn."), or
  Swarm's refusal (for example while a run is active).
- **Usage:** each sign-in plan's limit windows (`GET /v1/codex/account/usage`),
  then 30 days of tokens, spend at API prices, turns, each day, each model
  and recent sessions (`GET /v3/usage`, today and 30 days).
- **Search** (below Settings, ctrl-k or alt-6): every
  conversation Swarm has, found as you type by words in titles and messages
  (`POST /v3/sessions:search`), scoped to active, archived or all. An
  active one opens in a pane (one older than the sidebar's list is read
  from Swarm first); an archived one comes back with "unarchive"
  (`POST /v3/sessions:unarchive`, with the tombstone's version from the
  project's archive). Sessions are archived, pinned and renamed from the
  sidebar: each row's second line shows pick, pin, rename and archive on
  hover.
- **Automations** (below Agents, alt-7): what your workers do on their
  own. First what runs now, each with its latest action and a stop; then
  workers whose latest run failed, in red with Swarm's error; then recent
  runs (status, when, how long, what they handed back); then the workers
  and automations with their schedules. A run opens on its own view: its
  record and every action its agent took, live while it runs (its
  session, subscribed over the realtime stream), and its report. The page
  reads again when Swarm sends `worker.updated` or `automation.updated`;
  a new failure also shows in the status bar, and the nav says how many
  run or failed. Routes: `/v3/workers`, `/v3/workers/{id}/runs` and
  `…/cancel`, `/v3/automations/v2`, `/v3/sessions/{id}`. A linked
  machine's automations show in its own window (open it from its card on Fleet).
- **Icons:** line, solid, pixel or glyph. They
  are one-color SVG masks tinted by the theme, so a reskin needs no new art.
  Line by default; Settings or ctrl-4 change it. Pixel icons are ASCII art
  in `assets/icons/pixel.txt`; `scripts/pixel-icons.sh` rebuilds them.

| Key | Action |
| --- | --- |
| ctrl-d | new session in a pane to the right (the pane header's first button) |
| ctrl-shift-d | new session in a pane below |
| ctrl-w | close pane |
| ctrl-] | next pane |
| ctrl-b | collapse or expand the sidebar |
| ctrl-4 | next icon style |
| alt-1 … alt-5 | Sessions, Media, Usage, Agents, Settings |
| alt-8 | Fleet |
| tab | in a composer, complete a / command |

## Notes

- `gpui` 0.2.2 is Zed's last crates.io release. Its `xattr` 0.2.3 dependency
  breaks on libc 0.2.191, so `patches/xattr-0.2.3` maps `ENOATTR` to `ENODATA`.
- `src/swarm/live.rs` follows Swarm the way its web UI does: one sync
  snapshot, then the realtime stream for everything that happens, with open
  and running sessions hydrated in full. The machine summary, models, usage
  and plan limits are reread on their own thread every 30 seconds and after a
  command; `src/swarm/fleet.rs` rereads home's fleet list and each linked
  machine's summary and models through home every 20 seconds, and in the
  same tick what needs the person on home and each machine
  (`src/swarm/needs.rs`).
  `src/swarm/map.rs` turns the answers into what the console shows.

### Project folders and project.md

The Projects page lists folder paths and roles, supports confirmed removal and adding folders to the selected project, and renders editable project.md as Markdown. Folder changes refresh project.md automatically. Generation status and failures follow `project.updated`; ready documents can be refreshed and failed attempts retried. Linked-machine windows refuse folder changes; change folders on that machine instead.
