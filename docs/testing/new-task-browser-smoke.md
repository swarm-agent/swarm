# New Task: browser → real backend → durable reload

One opt-in **creation/persistence E2E**, not agent completion, restart proof, provider qualification, full harness proof or a benchmark. Real Chromium submits the actual Orchestrator New Task form: explicit disposable repository, Big Feature/Swarm, auto-approval off. Assertions retain the real POST/201, stable body/header/task IDs, duplicate-click guard, one durable pending task, zero canonical run intents and the same card after reload. No mocked endpoints; never approve anything.

**Status: not run; parent validation required.** Parent owns managed environment build/ensure/exec/release and exact committed-result validation on GCP. Run only inside the isolated managed SSH Docker container, no published ports or host daemon. No image/cloud/broker implementation change is needed. Do not modify the candidate while another parent run validates an older immutable commit.

## Fixture/bootstrap contract

Parent supplies a clean committed disposable Git root strictly under run-provided absolute `TMPDIR`, an explicit loopback Desktop URL and two other unused loopback ports. Runner fails fast on missing/unsafe inputs; ports already in use fail, never attach as fallback. The fixture itself is never deleted by the wrapper.

`SMOKE_MODEL_SETTINGS_FILE` is bounded non-secret JSON from the authorized account's canonical `/v1/agent-model-settings` read: **Swarm action/plan plus all five system_agents (compact, finder, coder, designer, router)**, each with provider/model/thinking and optional service_tier/context_mode. Input may be the canonical response wrapper; only assignment fields are forwarded. Do not export credentials/tokens, copy source identity or invent dummy models/providers. Missing system slots or thinking fail before initialization. Use normalized values returned by the canonical account service.

Public PATCH **does not initialize** absent settings. The test-only `swarmd/tests/newtaskbootstrap` executable reads sanitized assignments from stdin and calls maintained `identity.Service.BootstrapFirstIdentity` and `AgentModelSettingsStore.PutForAccount` **offline**, before daemon startup. It requires the wrapper's exact owner marker, a new scratch root and exclusive absent database directory; rejects symlinks, existing state and foreign roots. It emits no identity or auth values. Startup's canonical migration accepts this complete record without rewriting it. This helper is not registered in production commands/API/harness. No source account or product behavior is changed.

After startup, the test checks the exact generated fixture username, normal Desktop-origin session authentication, empty projects/workspaces, and no ready/runnable providers. It marks onboarding complete through the authenticated maintained API, compares every imported canonical assignment and sets the disposable account's default model through `/v1/model`. No auth bypass or provider keys are installed. Project creation may attempt Router context generation: a fresh empty HOME/environment, no credentials and **externally denied provider egress** prevent spending. Do not retry context or fabricate ready state.

## Portable prerequisites inside the container

Linux amd64/glibc (checked-in FFF), Go **1.26.7**, C compiler/build tools, Git, Bash, GNU timeout, curl, CA certificates, **xz-utils and unzip**. Node **24.16.0**, pnpm **11.13.1**, lockfile Playwright **1.59.1**. Browser libraries: use the bounded maintained Playwright `install-deps chromium` command with container package-manager authority, or bake those libraries into the image. No host sudo, home mounts, `.env` files or provider environment.

Allow package egress only during installation/build. Parent must deny provider/network egress (loopback allowed) before the runtime command. The acknowledgement flag does not configure a firewall. Final managed deployment disposal is containment if the wrapper is forcibly killed.

From the exact candidate root, supply absolute writable `TMPDIR`. If Node is not already verified in the warm image:

<copy label="Verified Node installation">
set -euo pipefail
: "${TMPDIR:?run-provided scratch required}"
NODE_ROOT=$(mktemp -d "$TMPDIR/new-task-node.XXXXXX")
export NODE_ROOT
node_archive=node-v24.16.0-linux-x64.tar.xz
timeout --kill-after=5s 180s curl --fail --silent --show-error --location --max-time 170 \
  "https://nodejs.org/dist/v24.16.0/$node_archive" -o "$NODE_ROOT/$node_archive"
timeout --kill-after=5s 30s curl --fail --silent --show-error --location --max-time 25 \
  https://nodejs.org/dist/v24.16.0/SHASUMS256.txt -o "$NODE_ROOT/SHASUMS256.txt"
(cd "$NODE_ROOT"; awk '$2 == "node-v24.16.0-linux-x64.tar.xz"' SHASUMS256.txt > selected.sha256;
 test "$(wc -l < selected.sha256)" = 1; sha256sum --check selected.sha256;
 timeout --kill-after=5s 30s tar -xJf "$node_archive")
export PATH="$NODE_ROOT/node-v24.16.0-linux-x64/bin:$PATH"
test "$(node --version)" = v24.16.0
</copy>

Install/build with bounded fan-out/output. **Do not apply a 10 MiB ulimit -f to package archives, Chromium or Go binaries.** Build logs are not live-provider evidence. Warm outputs may be reused only when parent verifies their source SHA; helper must be built from this correction.

<copy label="Dependencies and builds">
set -euo pipefail
: "${TMPDIR:?}"
export SMOKE_BUILD_ROOT=$(mktemp -d "$TMPDIR/new-task-build.XXXXXX")
timeout --kill-after=10s 180s npm install --global pnpm@11.13.1 2>&1 | tail -c 65536
timeout --kill-after=10s 600s bash -c 'cd web; pnpm install --frozen-lockfile' 2>&1 | tail -c 65536
# Container package-manager authority only; omit if libraries are already baked in.
timeout --kill-after=10s 300s bash -c 'cd web; pnpm exec playwright install-deps chromium' 2>&1 | tail -c 65536
timeout --kill-after=10s 600s bash -c 'cd web; pnpm build' 2>&1 | tail -c 65536
source scripts/lib-go.sh
swarm_require_go "$PWD"
timeout --kill-after=10s 600s bash -c 'cd swarmd; go build -p 2 -o "$SMOKE_BUILD_ROOT/swarmd" ./cmd/swarmd' 2>&1 | tail -c 65536
timeout --kill-after=10s 180s bash -c 'cd swarmd; go build -p 2 -o "$SMOKE_BUILD_ROOT/newtaskbootstrap" ./tests/newtaskbootstrap' 2>&1 | tail -c 65536
</copy>

The parent's fresh daemon/frontend compile previously took about 281 seconds (Go about 200); allow the stated build bounds, not a 120-second cold compile budget. Runtime is separate.

Chromium **147.0.7727.15**, Playwright revision **1217**: Node ZIP extraction stalled in the tested image; use standard `unzip`, not another Playwright downloader retry. Parent can reuse its pinned verified warm executable. For a fresh install, supply its trusted image/installation-receipt `SMOKE_CHROMIUM_SHA256` (do not invent a digest):

<copy label="Pinned Chromium via unzip">
set -euo pipefail
: "${TMPDIR:?}" "${SMOKE_CHROMIUM_SHA256:?trusted archive SHA-256 required}"
export SMOKE_CHROME_ROOT=$(mktemp -d "$TMPDIR/new-task-chrome.XXXXXX")
timeout --kill-after=5s 180s curl --fail --silent --show-error --location --max-time 170 \
  https://storage.googleapis.com/chrome-for-testing-public/147.0.7727.15/linux64/chrome-headless-shell-linux64.zip \
  -o "$SMOKE_CHROME_ROOT/chrome-headless-shell-linux64.zip"
( cd "$SMOKE_CHROME_ROOT"; printf '%s  chrome-headless-shell-linux64.zip\n' "$SMOKE_CHROMIUM_SHA256" | sha256sum --check -;
  timeout --kill-after=5s 60s unzip -q chrome-headless-shell-linux64.zip )
export CHROMIUM_EXECUTABLE_PATH="$SMOKE_CHROME_ROOT/chrome-headless-shell-linux64/chrome-headless-shell"
timeout --kill-after=5s 10s "$CHROMIUM_EXECUTABLE_PATH" --version
</copy>

## Parent validation and runtime

Focused deterministic checks (helper/store tests and subprocess protocol/lifecycle unit tests, **not E2E proof**):

<copy label="Focused validation">
timeout --kill-after=5s 30s node --test --test-timeout=20000 scripts/run-new-task-smoke.test.mjs scripts/run-new-task-smoke-isolated.test.mjs
timeout --kill-after=5s 180s bash -c 'cd swarmd; go test -p 2 -timeout 30s ./tests/newtaskbootstrap -run "^Test(OwnedBootstrap|BootstrapRejectsPathAndInput)$" -count=1'
</copy>

Parent supplies `FIXTURE_REPO` (already clean, committed, disposable, under TMPDIR), `SMOKE_MODEL_SETTINGS_FILE`, and its pinned Chromium executable. Deny provider egress now. No nested setsid/timeout shell startup. The supervisor creates/owns all daemon state, sanitizes the daemon environment, bootstraps offline, starts only loopback listeners in this container and runs the browser command synchronously. No readiness line can qualify as success.

<copy label="Real browser smoke with owned startup and disposal">
timeout --kill-after=10s 240s node scripts/run-new-task-smoke-isolated.mjs \
  --daemon-bin "$SMOKE_BUILD_ROOT/swarmd" \
  --bootstrap-bin "$SMOKE_BUILD_ROOT/newtaskbootstrap" \
  --desktop-url http://127.0.0.1:15655/ \
  --api-port 17881 --peer-port 17882 \
  --fixture-repo "$FIXTURE_REPO" \
  --model-settings-file "$SMOKE_MODEL_SETTINGS_FILE" \
  --isolated-no-provider-egress --timeout-ms 120000
</copy>

Safe output uses fixed step/error identifiers and HTTP status only, no API bodies, URLs, tokens or raw Playwright diagnostics. Internal process output is capped at 64 KiB; bootstrap 15 seconds, readiness 25 seconds, browser 120 seconds, wrapper hard deadline 180 seconds, outer containment 240 seconds. Processes are terminated/escalated and reaped; pending task/session records remain intact until the wrapper disposes its **entire exact unique daemon root**. No backend deletion guards are weakened. Cleanup errors do not replace the first assertion; any cleanup failure prevents final PASS. External fixture/build/browser/install inputs are never deleted by the wrapper; parent removes those exact run-owned paths or disposes the managed deployment at release.

Evidence must include exact candidate SHA, deployment receipt, command exit code **zero**, and affirmative `SMOKE_ISOLATED_PASS tests=1 cleanup=disposed`. Internally the browser runner requires private success IPC **plus nonzero TAP tests**, all passed, zero failed/cancelled/skipped/todo. Early exit, missing marker, readiness-only output, HTTP failure, assertion, deadline, interrupted or cleanup failure is **nonzero**. Never count `ISOLATED_DAEMON_READY`, a successful build or an auth token as a browser pass. Capture no storage state/traces/screenshots/auth bodies; keep exact durable IDs out of public tracked evidence. Reload assertions are not pixel/aesthetic inspection.
