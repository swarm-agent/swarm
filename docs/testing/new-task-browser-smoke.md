# New Task: browser → real backend → durable reload

This is one opt-in creation/persistence smoke, **not agent completion, daemon restart, provider qualification, full harness proof, or a benchmark**. Chromium navigates the real project Orchestrator, opens New Task, selects the explicit disposable Git workspace and Big Feature, turns auto-approval off and submits. It checks the actual POST body/response, stable task/request IDs, one submission after a double click, one durable task, `pending_approval`, zero canonical V3 run intents, then reloads and finds the same card. Nothing is approved. No endpoint is fulfilled with synthetic data.

The parent owns managed environment `get/list → ensure → exec → release`, candidate SHA verification and evidence. Run **inside** the leased generic SSH Docker container, never on the host daemon; do not publish ports. No GCP/broker changes are needed. Do not run the legacy Desktop launch runner as substitute coverage.

## Prerequisites (image recipe requirements)

Linux amd64/glibc (matching checked-in FFF libraries), Go **1.26.7**, CGO C compiler/build tools, Git, Bash, GNU `timeout`, `setsid` (util-linux), curl, CA certificates; Node **24.16.0**, pnpm **11.13.1**. `web/pnpm-lock.yaml` supplies Playwright **1.59.1**. Chromium plus OS libraries must be installed in this container before denying outbound network. The existing generic Go-only image needs these additions, not cloud provisioning. Parent supplies an absolute writable **run-provided `TMPDIR`**, not a literal system-temp path. Candidate checkout and fixture must be accessible to the daemon user.

Dependency installation/build need package-download egress; the subsequent daemon/browser phase must have **provider/network egress denied** (loopback stays allowed). Prefer the managed environment's network policy or a prewarmed offline deployment; do not mutate host firewall rules. Mount no credentials or host home, inherit no provider/auth environment, and use a fresh daemon state. All auth is ephemeral Desktop-origin bootstrap held in process/browser memory, never printed or saved as storage state/traces/screenshots.

From the exact reviewed candidate root, with Node 24.16.0 already installed through the image's verified Node distribution:

```sh
node --version # must be v24.16.0
npm install --global pnpm@11.13.1
: "${TMPDIR:?parent must supply run scratch}"
export PLAYWRIGHT_BROWSERS_PATH="$TMPDIR/new-task-chromium"
# Bound package output to a disposable private file; never upload it automatically.
umask 077
setup_log=$(mktemp "$TMPDIR/new-task-setup.XXXXXX")
timeout --kill-after=10s 600s bash -c '
  set -euo pipefail
  ulimit -f 10240 # cap disposable setup output
  cd web
  pnpm install --frozen-lockfile
  pnpm exec playwright install --with-deps chromium
  pnpm build
' >"$setup_log" 2>&1 || { echo 'Node/Chromium/Desktop setup failed or exceeded 600s'; exit 1; }
# --with-deps requires container package-manager authority; bake it into the image
# instead when exec is unprivileged. Never use host sudo for this test.
```

Installation is container-only and must have managed output/storage quotas (setup log maximum 10 MiB). If the recipe does not provide verified Node, package-manager authority or egress control, report that exact prerequisite instead of attaching to a user's credentialed daemon.

Focused deterministic runner checks (no daemon/browser/provider required):

```sh
timeout --kill-after=5s 20s node --test --test-timeout=10000 scripts/run-new-task-smoke.test.mjs
```

## Build/start and run (no listeners outside the container)

Build first while package egress is allowed. This uses the checked-in Go discovery helper, preserves production CGO/FFF and puts the binary in run scratch:

```sh
: "${TMPDIR:?}"
export SMOKE_ROOT=$(mktemp -d "$TMPDIR/new-task-smoke.XXXXXX")
umask 077
source scripts/lib-go.sh
swarm_require_go "$PWD"
timeout --kill-after=10s 600s bash -c 'ulimit -f 10240; cd swarmd; go build -p 2 -o "$SMOKE_ROOT/swarmd" ./cmd/swarmd' \
  >"$SMOKE_ROOT/build.log" 2>&1 || { echo 'isolated daemon build failed or exceeded 600s'; exit 1; }
```

Parent must supply `SMOKE_MODEL_SETTINGS_FILE`: an absolute path to a bounded non-secret JSON input obtained from the authorized account's canonical `/v1/agent-model-settings` read (copy only `swarm.action` and `swarm.plan` assignments, not credentials, identity IDs or tokens). The runner accepts `{ "swarm": { "action": { ... }, "plan": { ... } } }` or the canonical response wrapper. Never invent model/provider IDs: resolve them before deployment from account settings/catalog. Fresh identity onboarding does not itself initialize model assignments, so this explicit input is necessary. It seeds only the newly owned disposable account using public settings APIs before any project/context attempt.

Now parent denies provider egress on this deployment. Pick three explicit unused loopback ports; example below uses 17881, 15655, 17882. A bind/readiness failure is failure, never fallback to an existing listener. The outer deadline kills the owned daemon/runner process group; the runner tracks Chromium's separately launched PID over private IPC and kills its owned process group on timeout/signal. Parent deployment disposal remains the final containment boundary. It also avoids `~/.env` and ambient credentials. Start configuration and storage environment match the maintained isolated startup contract in `tests/swarmd/identity_bootstrap_e2e.sh`; do not run that unrelated suite here.

```sh
: "${SMOKE_ROOT:?}" "${PLAYWRIGHT_BROWSERS_PATH:?}" "${TMPDIR:?}" "${SMOKE_MODEL_SETTINGS_FILE:?non-secret canonical assignments required}"
export SMOKE_MODEL_SETTINGS_FILE
export SMOKE_SOURCE="$PWD" SMOKE_NODE=$(command -v node)
# Final disposal is mandatory even if a response is lost or Chromium hard-times out.
trap 'rm -rf -- "$SMOKE_ROOT"' EXIT
setsid timeout --kill-after=5s 240s bash -c '
  set -euo pipefail
  umask 077
  cd "$SMOKE_SOURCE"
  mkdir -p "$SMOKE_ROOT"/{home,data,runtime,config,cache,logs,fixture}
  git -C "$SMOKE_ROOT/fixture" init -b dev -q
  printf "Disposable New Task browser persistence fixture.\n" >"$SMOKE_ROOT/fixture/README.md"
  git -C "$SMOKE_ROOT/fixture" add README.md
  git -C "$SMOKE_ROOT/fixture" -c user.name="Smoke Fixture" -c user.email="smoke@example.invalid" commit -qm "fixture baseline"
  startup=$(printf "swarm_name = New Task Smoke\nhost = 127.0.0.1\nport = 17881\ndesktop_port = 15655\npeer_transport_port = 17882\n")
  env -i PATH="$PATH" HOME="$SMOKE_ROOT/home" TMPDIR="$TMPDIR" \
    STATE_DIRECTORY="$SMOKE_ROOT/data" RUNTIME_DIRECTORY="$SMOKE_ROOT/runtime" \
    CONFIGURATION_DIRECTORY="$SMOKE_ROOT/config" CACHE_DIRECTORY="$SMOKE_ROOT/cache" \
    LOGS_DIRECTORY="$SMOKE_ROOT/logs" SWARM_CHILD_STARTUP_CONFIG="$startup" \
    SWARM_WEB_DIST_DIR="$SMOKE_SOURCE/web/dist" SWARM_DISABLE_MINT_REPORT=1 \
    "$SMOKE_ROOT/swarmd" --listen 127.0.0.1:17881 --desktop-port 15655 \
    --data-dir "$SMOKE_ROOT/data/swarm" --db-path "$SMOKE_ROOT/data/smoke.pebble" \
    --lock-path "$SMOKE_ROOT/runtime/swarmd.lock" >/dev/null 2>&1 &
  daemon=$!
  cleanup() {
    kill "$daemon" 2>/dev/null || true
    for attempt in {1..30}; do
      kill -0 "$daemon" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL "$daemon" 2>/dev/null || true
    wait "$daemon" 2>/dev/null || true
  }
  trap cleanup EXIT
  ready=false
  for attempt in {1..100}; do
    kill -0 "$daemon" 2>/dev/null || { echo "owned daemon exited before readiness"; exit 1; }
    if curl --max-time 1 -fsS http://127.0.0.1:17881/readyz >/dev/null 2>&1 && \
       curl --max-time 1 -fsS http://127.0.0.1:15655/ >/dev/null 2>&1; then ready=true; break; fi
    sleep 0.1 # readiness only, not simulated workload or telemetry
  done
  "$ready" || { echo "isolated API/Desktop readiness failed within bounded attempts"; exit 1; }
  "$SMOKE_NODE" scripts/run-new-task-smoke.mjs \
    --desktop-url http://127.0.0.1:15655/ \
    --fixture-repo "$SMOKE_ROOT/fixture" \
    --model-settings-file "$SMOKE_MODEL_SETTINGS_FILE" \
    --isolated-no-provider-egress --timeout-ms 120000
'
```

The runner only attaches to an explicit `http://127.0.0.1:PORT/` Desktop root and a clean committed real Git root strictly under `TMPDIR`. It fails if already onboarded, if existing projects/workspaces are found, if any provider is ready/runnable, if canonical account Swarm action model settings are missing, if Chromium is absent, or if any assertion/cleanup/deadline fails. No task model overrides, keys or dummy providers are installed. The explicit non-secret settings input seeds the new disposable identity through `/v1/agent-model-settings` and `/v1/model`; it never edits the source account. Runtime resolution remains canonical. Invalid/unavailable catalog assignments are honest setup failures.

**Known production behavior:** creating a project starts real Router context generation. Without credentials and with provider egress denied it cannot spend and may leave a failed context diagnostic. The test does not retry, approve, fabricate ready context or change production behavior. It creates one empty real project conversation for navigation to the Tasks UI. If production rejects pending-task setup in this credential-free state, report that exact limitation and obtain a separately scoped setup design; do not supply real credentials to force this smoke through.

Normal cleanup verifies the unique returned project/workspace/task identities, deletes the task at its current revision, tombstones only its returned sessions, deletes only that project and deregisters only the fixture. Identity, topology, Git worktree files and durable tombstones may remain in the disposable daemon state: **parent must stop/reap the whole process group and remove the exact unique `SMOKE_ROOT` on every outcome**, including setup-response loss and hard timeout. Never reuse this daemon or delete unrelated state. Dispose the Chromium cache/setup log separately by their exact run-owned paths when the lease ends.

Runner output is bounded (64 KiB internal capture) and only prints a fixed stage/failure message or final success line; API/Playwright diagnostics are suppressed to avoid token leakage. Parent evidence should retain exact candidate SHA, managed deployment receipt, command exit codes and sanitized output, not auth responses, provider bodies, traces or actual durable session IDs in public tracked docs. **Auth and reload success are not visual/aesthetic inspection or execution proof.**

Implementation handoff status: **not run; parent validation required**. Execute the focused argument tests and then this smoke against the exact committed tree in the managed container; do not report PASS until its zero exit and all cleanup postconditions are observed.
