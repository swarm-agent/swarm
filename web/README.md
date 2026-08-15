# Swarm Web

Workspace-first browser client for the new desktop launcher.

## Current scope

This app now provides the main launcher page:
- load saved workspaces from `swarmd`
- show the workspace grid on `/`
- let the user select the active workspace
- let the user choose a local default workspace for later launches

This is intentionally modular:
- workspace API lives under `src/features/workspaces/api.ts`
- launcher state lives under `src/features/workspaces/hooks/`
- UI components live under `src/features/workspaces/components/`
- page composition lives under `src/features/workspaces/pages/`

## Desktop launcher

Preferred local entrypoint:

```bash
cd /path/to/swarm-go
./bin/swarm --desktop
./bin/swarm dev --desktop
```

That launcher:
- ensures the lane backend is running first
- opens the browser automatically
- `./bin/swarm --desktop` opens the built desktop served by `swarmd`
- `./bin/swarm dev --desktop` starts the local Vite dev server against the dev lane backend and disables the backend desktop listener for that run so Vite owns the dev frontend port

## Dev

```bash
cd web
corepack pnpm install --frozen-lockfile
SWARM_BACKEND_URL=http://127.0.0.1:7781 SWARM_DESKTOP_PORT=5556 corepack pnpm run dev
```

Dependencies are managed with pnpm and the checked-in `pnpm-workspace.yaml` enables supply-chain hardening: a seven-day `minimumReleaseAge`, strict release-age enforcement, blocked exotic transitive dependencies, and an explicit build-script allowlist. Use the package scripts instead of calling `vite` directly.

Default Vite URL:
- main desktop: `http://127.0.0.1:5555`
- dev desktop: `http://127.0.0.1:5556`

## MVP Network Access

Direct private-LAN desktop access is not implemented safely yet. The desktop client warns when it is opened through a private LAN address because the backend desktop auth path is still local-first.

For another device, keep the Swarm host bound to `127.0.0.1` and use an SSH tunnel to the desktop port, for example `ssh -L 5555:127.0.0.1:5555 <host>`, or use Tailscale. Tailscale is usually the lower-friction secure option.

Expected local backend:
- main lane: `http://127.0.0.1:7781`
- dev lane: `http://127.0.0.1:7782`

The Vite dev server proxies `/v1`, `/v2`, `/v3`, `/healthz`, `/readyz`, `/desktop`, and `/ws` to the lane backend selected through `SWARM_BACKEND_URL`. The launcher also verifies the target page contains Vite's `/@vite/client` marker before it reports desktop dev mode as ready, so an unrelated HTTP listener on the same port cannot masquerade as the dev frontend.

## Repeatable Desktop subagent task E2E

Use this when validating that Desktop can ask the UI to launch two saved subagents, approve the permission modal, and capture V3 realtime/task logs from a real served Desktop URL:

```bash
cd web
node ./scripts/run-desktop-subagent-task-e2e.mjs https://example.invalid/swarm-go
```

The script writes `summary.json`, `browser-events.json`, `network.json`, `browser-console.json`, `dom-snapshot.txt`, and a screenshot into a temp evidence directory printed in the test output. The printed evidence directory is disposable and should not be copied into tracked repository paths. It fails unless Playwright observes `session.tool.started`, `session.tool.delta`, and two child session IDs in the task stream.

## Canonical Desktop launch suite

Use the parameterized Playwright launch suite for a provider-backed Desktop release pass:

```bash
scripts/run-desktop-launch-test.sh <ssh-alias> <provider> --desktop-url <served-desktop-url>
```

The SSH alias, provider, and served Desktop URL are invocation inputs and are never stored in the suite. Omit `--desktop-url` to tunnel the target's loopback Desktop listener through the supplied SSH alias. `--workspace <name-or-path>`, `--timeout-ms <ms>`, and `--headful` are optional.

The suite exercises the real Desktop composer and verifies durable/runtime evidence for:

- `/new <prompt>` (Auto), `/new plan <prompt>`, `/new worktree <prompt>`, and `/new wp <prompt>`;
- `/task <prompt>` and `/task plan <prompt>` through the background Router worktree path;
- a Plan-agent proposal with exactly two checkpoints, explicit exit-plan approval, automatic mode switching to Auto, and completion of both checkpoints without another permission;
- provider/model role usage plus an AI response that reads and confirms the injected session mode for each start.

The runner temporarily assigns the selected provider's catalog-recommended Plan and Auto models to Swarm and restores the original account-scoped assignments in teardown. It creates durable test sessions and managed worktrees on the target, but it does not modify workspace files.
