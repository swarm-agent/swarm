# Container-backed live testbench

All live candidate tests use the dedicated broker-owned `systemd-nspawn` machine.
The host Swarm service and host candidate ports are not a fallback.

## Authority and ports

- Lifecycle entrypoint: `scripts/testbench-container-deploy.sh`
- Fixed broker actions: `test-container-prepare`, `test-container-start`,
  `test-container-stop`, `test-container-status`, and
  `test-container-sync-fireworks`
- Remote loopback: Desktop `127.0.0.1:5655`, API `127.0.0.1:7881`
- Default local forwards: Desktop `127.0.0.1:15655`, API
  `127.0.0.1:17881`
- Retired candidate target: host `swarm.service` and host ports `5555/7781`

The ignored repository-root `.env` is the single local routing source. Start from
`.env.example`; it declares `SWARM_TESTBENCH_TARGET=container` and the fixed
remote container ports. Checked-in wrappers reject a host target or host ports.

## Deploy the exact committed candidate

Deployment requires a clean checkout because the broker accepts one verified Git
bundle and records its exact commit as `candidate_head`.

```bash
./scripts/testbench-container-deploy.sh deploy
```

This prepares and starts only the isolated container, installs the candidate into
container-local system paths, enables `dev_mode` inside that disposable container
so the authenticated session-dump workflow is available, synchronizes the
configured Fireworks credential through the fixed broker, and prints the exact
deployed commit. It does not replace or restart a host Swarm installation.

## Check before testing

```bash
./scripts/testbench-container-deploy.sh check
```

`check` is read-only and requires all of the following:

1. the current checkout has no tracked, staged, or untracked source changes;
2. the broker reports the isolated container active;
3. both container loopback endpoints are healthy;
4. `candidate_head` exactly equals the current checkout `HEAD`.

A stopped or stale container fails with an instruction to deploy. For automatic
setup without running a test, use `./scripts/testbench-container-deploy.sh ensure`;
it deploys only when needed. Neither action falls back to `scripts/ssh-fast-test.sh`,
host `swarm.service`, or ports `5555/7781`.

## Run checked-in tests

Use `run` when invoking a command directly. It checks the exact current `HEAD`; when the container is stopped or stale it automatically invokes the same canonical `deploy` action first. That deployment still fails closed unless all source changes are committed:

```bash
./scripts/testbench-container-deploy.sh run \
  ./scripts/run-runner-test.sh \
  http://127.0.0.1:15655 fireworks artifact-v3-multipart-e2e \
  --workspace-path /path/already/bound/in/the/container \
  --action-model deepseek-v4-flash-0731 --action-thinking high \
  --plan-model deepseek-v4-pro-0813 --plan-thinking xhigh \
  --coder-model deepseek-v4-flash-0731 --coder-thinking high \
  --designer-model kimi-k3 --designer-thinking high
```

After exact-head readiness, the wrapper opens temporary SSH forwards, exports:

- `SWARM_DESKTOP_URL=http://127.0.0.1:15655`
- `SWARM_PRIMARY_API_URL=http://127.0.0.1:17881`
- `SWARM_RUNNER_API_URL=http://127.0.0.1:15655`

and closes the tunnel when the command exits. Runners use the Desktop endpoint
for local-session bootstrap because it preserves the browser/local-transport
authentication contract; the API forward remains available for probes that
explicitly require it. Failed Artifact V3 journeys invoke
`scripts/session-dump-via-api.sh` before the tunnel closes and download the exact
private dump into the runner's ignored evidence directory.

For ordinary registered runners, use the shorter wrapper:

```bash
./scripts/run-testbench-runner.sh artifact-v3-multipart-e2e \
  --workspace-path /path/already/bound/in/the/container
```

The compatibility entrypoint `scripts/testbench-e2e-tunnel.sh` now delegates
only to the same container `check` and `run` actions. It contains no host-service
probe, deployment, restart, or alternate port path.
