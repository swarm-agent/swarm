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
npm install
npm run dev
```

The checked-in launcher keeps the Vite workflow usable on local Node 18 environments by providing the missing `crypto.hash` API that Vite 7 expects. Use the package scripts instead of calling `vite` directly.

Default Vite URL:
- main desktop: `http://127.0.0.1:5555`
- dev desktop: `http://127.0.0.1:5556`

## MVP Network Access

Direct private-LAN desktop access is not implemented safely yet. The desktop client warns when it is opened through a private LAN address because the backend desktop auth path is still local-first.

For another device, keep the Swarm host bound to `127.0.0.1` and use an SSH tunnel to the desktop port, for example `ssh -L 5555:127.0.0.1:5555 <host>`, or use Tailscale. Tailscale is usually the lower-friction secure option.

Expected local backend:
- main lane: `http://127.0.0.1:7781`
- dev lane: `http://127.0.0.1:7782`

The Vite dev server proxies `/v1`, `/healthz`, `/readyz`, and `/ws` to the lane backend selected through `SWARM_BACKEND_URL`. The launcher also verifies the target page contains Vite's `/@vite/client` marker before it reports desktop dev mode as ready, so an unrelated HTTP listener on the same port cannot masquerade as the dev frontend.
