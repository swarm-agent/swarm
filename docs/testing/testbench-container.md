# Pooled container-backed live testbench

All live candidate tests use the broker-owned two-slot `systemd-nspawn` pool.
The host Swarm service and host candidate ports are not a fallback.

## Authority and isolation

- Client: `scripts/testbench-container-deploy.sh`
- Compatibility command runner: `scripts/testbench-e2e-tunnel.sh`
- Registered runner wrapper: `scripts/run-testbench-runner.sh`
- Server contract: the fixed broker actions and helper maintained in the critical operations workspace
- Slots: `1` and `2`, each with independent installation, data, cache, runtime, logs, network, proxy, and loopback listeners
- Remote slot ports: Desktop `5655`/`5656`; API `7881`/`7882`
- Local slot forwards: Desktop `15655`/`15656`; API `17881`/`17882`

The client derives a stable lane ID from the repository common Git directory and
the exact clean worktree path. It reuses that lane's assigned slot or atomically
claims one inactive slot. This permits two distinct clean worktrees to test in
parallel without sharing writable state or silently replacing each other's
candidate.

The ignored repository-root `.env` contains only the SSH alias, fixed default
slot-1 port contract, Fireworks model IDs, and optional loopback reverse-forward
configuration. It contains no credential. The broker supplies Fireworks through
the protected server boundary.

## Deploy an exact worktree

Deployment requires no tracked, staged, or untracked source changes. The client
creates a verified Git bundle for the exact `HEAD`, places it in a lane-scoped
claim, and asks the broker to allocate and deploy atomically:

```bash
./scripts/testbench-container-deploy.sh deploy
```

A different clean worktree can be selected explicitly only when it belongs to the
same Git repository:

```bash
./scripts/testbench-container-deploy.sh deploy --source-worktree /path/to/managed/worktree
```

`deploy` prints the assigned slot. It builds and starts only that disposable
container slot, synchronizes Fireworks through the fixed broker, and never
installs or restarts host `swarm.service`.

## Inspect and tunnel

```bash
./scripts/testbench-container-deploy.sh pool-status
./scripts/testbench-container-deploy.sh status
./scripts/testbench-container-deploy.sh tunnel
```

`pool-status` reports both bounded slots. `status` requires this worktree to have
an assigned active slot. `tunnel` opens only the local forwards for that slot and
renews its private activity lease once per minute; the server's reaper stops a
slot after 30 idle minutes.

For exact-head checking and command execution, use the compatibility wrapper:

```bash
./scripts/testbench-e2e-tunnel.sh check
./scripts/testbench-e2e-tunnel.sh run COMMAND ARG...
```

`check` fails unless this checkout is clean and its assigned active slot runs the
exact current `HEAD`. `run` deploys the exact clean `HEAD` only when the slot is
absent, inactive, or stale, then opens temporary slot-specific forwards and
exports:

- `SWARM_DESKTOP_URL`
- `SWARM_PRIMARY_API_URL`
- `SWARM_RUNNER_API_URL`
- `SWARM_RUNNER_WEB_PACKAGE` when the repository's primary checkout has pinned Playwright dependencies

The temporary tunnel and heartbeat are stopped when the command exits.

## Run checked-in E2E scenarios

Use the registered runner wrapper for normal tests:

```bash
./scripts/run-testbench-runner.sh artifact-v3-multipart-e2e \
  --stage basic-html \
  --timeout-ms 600000
```

The wrapper maps the current worktree to its stable slot, deploys the exact clean
commit when necessary, opens its loopback forwards, and runs the checked-in
scenario. The Artifact V3 `basic-html` stage isolates one ordinary primary-Swarm
static HTML creation before targeted turns, animation, Designer, storyboard, or
video gates.

Failed Artifact V3 journeys capture their bounded dev-mode session dump before
tunnel teardown. Dumps, screenshots, summaries, lane IDs, and session identifiers
are private ignored evidence and must not be committed or copied into public docs.
