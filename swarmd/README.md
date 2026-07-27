# swarmd (Go backend authority runtime)

Initial backend slice for the Swarm V2 refactor.

Implemented in this iteration:

- installed always-on daemon bootstrap managed by explicit service commands
- lockfile guard (single authority per machine/account)
- Pebble-backed persistence
- Codex auth persistence (`auth/codex/default`, API key or OAuth tokens, unencrypted profile)
- Codex Responses transport uses WebSocket-first (`wss://chatgpt.com/backend-api/codex/responses`) with explicit HTTP/SSE fallback
- global model preference (`provider`, `model`, `thinking`)
- modular provider boundary with runnable model providers `anthropic`, `codex`, `google`, `fireworks`, and `openrouter` (`exa` remains search-only); Copilot implementation code is present but intentionally not registered as selectable/runnable right now
- Fireworks runtime uses the OpenAI-compatible Fireworks Chat Completions API (`https://api.fireworks.ai/inference/v1/chat/completions`) with generic API-key auth
- OpenRouter runtime uses the OpenRouter Chat Completions API (`https://openrouter.ai/api/v1/chat/completions`) with generic API-key auth
- opt-in workspace persistence (saved explicitly)
- HTTP health + API endpoints
- WebSocket channel with `ping`, `subscribe`, `unsubscribe`, and replay support

## Run

For product installs, use the root launcher lifecycle. `swarm install --service` installs and starts the systemd service, while `swarm install --no-service` installs runtime files only for operators who manage the daemon with their own supervisor. The explicit service lifecycle commands are `swarm status`, `swarm start`, `swarm stop`, `swarm restart`, and `swarm uninstall`. Controller clients attach with `swarm session` or `swarm open`; closing a controller does not stop the daemon.

The commands below are development helpers for running `swarmd` from a source checkout.

`FFF` via the vendored Go/Cgo binding is the canonical in-app search backend for Swarm. The `search` tool uses it directly for ranked file search and content search, so Linux amd64 glibc hosts need the bundled `libfff_c.so` runtime available.

```bash
cd swarmd
SWARM_LANE=main ./scripts/dev-up.sh   # 127.0.0.1:7781
SWARM_LANE=dev  ./scripts/dev-up.sh   # 127.0.0.1:7782
```

## MVP Network Access

Direct private-LAN desktop access is not a supported secure path for the MVP. Keep `swarm.conf` bound to `127.0.0.1` for the desktop/backend.

For access from another device, use an SSH tunnel to the desktop port, for example `ssh -L 5555:127.0.0.1:5555 <host>`, or use Tailscale. Direct private LAN HTTP may show browser "Not Secure" warnings and desktop API auth may reject the request.

## API

- `GET /healthz`
- `GET /readyz`
- `GET /v1/auth/codex`
- `POST /v1/auth/codex`
- `GET /v1/auth/credentials?provider=&query=&limit=`
- `POST /v1/auth/credentials` (upsert credential: api/oauth + tags + active toggle)
- `POST /v1/auth/credentials/verify` (provider-specific auth connectivity verification for a credential)
- `POST /v1/auth/credentials/active` (set active credential per provider)
- `POST /v1/auth/credentials/delete`
- `GET /v1/auth/attach/token` (desktop bootstrap token reveal for loopback or trusted same-origin desktop requests)
- `POST /v1/auth/attach/rotate` (requires auth token)
- `GET /v1/model`
- `POST /v1/model`
- `GET /v1/model/catalog?provider=codex&model=gpt-5.4`
- `GET /v1/model/catalog?provider=codex&limit=500`
- `GET /v1/providers` (provider readiness + runnable status + supported auth methods)
- `GET /v1/workspace/current`
- `GET /v1/workspace/resolve?cwd=/path`
- `POST /v1/workspace/select`
- `GET /v1/workspace/list?limit=200`
- `POST /v1/workspace/add`
- `POST /v1/workspace/rename`
- `POST /v1/workspace/delete`
- `GET /v1/context/sources?cwd=/path`
- `GET /v1/sessions?limit=100`
- `POST /v1/sessions` (requires explicit `preference.provider`, `preference.model`, `preference.thinking`)
- `GET /v1/sessions/{id}`
- `GET /v1/sessions/{id}/messages?after_seq=0&limit=500`
- `POST /v1/sessions/{id}/messages`
- `POST /v1/sessions/{id}/run` (provider execution loop with concurrent tool calls; `anthropic`/`codex`/`google`/`fireworks`/`openrouter`)
- `GET /ws` (WebSocket)

### Copilot availability

Copilot provider code is retained in the tree, but Copilot is intentionally not registered as a selectable or runnable provider right now. Do not document Copilot as supported until it can be validated end-to-end with the required paid Copilot plan.

WebSocket client messages:

```json
{"type":"ping"}
{"type":"subscribe","channel":"system:*","last_seen_seq":10}
{"type":"unsubscribe","channel":"system:*"}
{"type":"resume","channel":"system:*","last_seen_seq":25}
```

## Helper CLI

```bash
cd swarmd
# show daemon health
 go run ./cmd/swarmctl health

# set codex key (or export CODEX_API_KEY)
 # first bootstrap attach token (loopback or same-origin desktop bootstrap) then export SWARMD_TOKEN
 go run ./cmd/swarmctl auth attach token
 export SWARMD_TOKEN="<value>"

 # inspect/rotate attach auth state
  go run ./cmd/swarmctl auth attach rotate

 # login with Codex OAuth (auto callback server) and optionally name the credential
 go run ./cmd/swarmctl auth codex login --method auto --label work
 go run ./cmd/swarmctl auth codex login --method code --label laptop

 # set codex key (or export CODEX_API_KEY) as fallback and optionally name it
 go run ./cmd/swarmctl auth codex set --api-key "$CODEX_API_KEY" --label backup

# inspect codex auth status
 go run ./cmd/swarmctl auth codex status

# set or get model preference
 go run ./cmd/swarmctl model set --provider codex --model gpt-5.4 --thinking xhigh
 go run ./cmd/swarmctl model get

# model catalog (models.dev-backed cache with stale fallback)
 go run ./cmd/swarmctl model catalog get --provider codex --model gpt-5.4
# Fireworks runtime provider is `fireworks` (models.dev source provider id: `fireworks-ai`)
go run ./cmd/swarmctl model catalog get --provider fireworks
# OpenRouter runtime provider is `openrouter`
go run ./cmd/swarmctl model catalog get --provider openrouter

# inspect discovered rules + skills for current directory
 go run ./cmd/swarmctl context sources

# create/list/read sessions and messages (projection-backed)
 go run ./cmd/swarmctl session create --title "debug run"
 go run ./cmd/swarmctl session list
 go run ./cmd/swarmctl session send --id session_... --role user --content "hello"
 go run ./cmd/swarmctl session messages --id session_...

# run a full Codex turn with concurrent tool execution
 go run ./cmd/swarmctl session run --id session_... --prompt "inspect README and suggest improvements"

```

All non-health endpoints require attach auth via `X-Swarm-Token` (or `Authorization: Bearer <token>`).

## Long-session diagnostics

`long_session_diagnostics` is an opt-in, metadata-only recorder for investigating memory growth and UI lag during multi-hour Swarm sessions. It is disabled by default and independent of `v3_diagnostics` and `provider_api_diagnostics`; leave those payload-oriented flags disabled during this capture to avoid changing the workload.

### Five-hour capture

1. Edit the canonical daemon startup config (`swarm.conf`) and set `long_session_diagnostics = true`.
2. Restart the daemon. Startup fails clearly if the private diagnostics directory cannot be created. Confirm the daemon log reports the selected run directory.
3. Open the browser-based Desktop. When the flag is detected, a memory-chip button appears immediately left of the notification bell. The browser automatically sends an authenticated metadata sample every 30 seconds. Use **Capture now** to send a current browser sample and ask the daemon to write a fresh Go runtime sample plus pprof snapshots. The dialog reports only the artifact directory and filenames returned by the daemon.
4. Copy the reported run directory for analysis. Set `long_session_diagnostics = false` and restart the daemon to disable the control and recorder.

The run directory is `long-session-diagnostics/run-<UTC timestamp>-<suffix>` below the platform's canonical Swarm logs root (`storagecontract.RootLogs`; `/var/log/swarmd` for the default Linux installation). Directories are mode `0700`, files are mode `0600`, and each run has a hard 512 MiB budget.

Artifacts:

- `manifest.json`: capture times, cadence, budget, and content policy.
- `samples.jsonl`: daemon/runtime/subsystem snapshots, including Go memory, RSS, goroutines, Pebble size, Codex retained-size counters, queues, and fixed-label latency aggregates.
- `desktop-samples.jsonl`: browser-attributed memory when the current `Performance.measureUserAgentSpecificMemory()` API is available (secure and cross-origin-isolated contexts only), plus event-loop drift, supported PerformanceObserver long-task and long-animation-frame duration/blocking time, DOM nodes, cache mutation timing grouped by bounded action type, sampler overhead, query-cache count/estimated bytes, V3 cache counts/estimated bytes, and largest cache-owning sessions represented only by stable hashes. Browser memory may be unavailable on the default local Desktop because this API has limited browser support and requires cross-origin isolation. The recorder does not use the deprecated, non-standard `performance.memory` API and does not claim to capture a browser heap snapshot.
- `operations.jsonl`: bounded metadata-only operation durations and dimensions with run-local hashed session identifiers.
- `profile-*.pprof`: periodic heap, allocation, goroutine, block, mutex, and occasional bounded CPU profiles.
- `latest-findings.json`: ranked baseline/current deltas for daemon, Codex/context, Desktop cache/DOM, realtime queues, storage, and provider/API latency. Rankings are correlations, not proof of causation.

Inspect `latest-findings.json` first, then correlate the spike window in `desktop-samples.jsonl`. For frontend CPU, prioritize `long_animation_frame_blocking_duration_ms`, `long_task_duration_ms`, `event_loop_drift_ms`, and `cache_action_duration_ms`; these are responsiveness signals, not an OS process CPU percentage or a JavaScript CPU profile. For memory, compare `browser_memory_bytes` when available with V3/query cache estimates and DOM nodes. A true JavaScript heap snapshot or frontend CPU profile must be captured externally with Chrome DevTools' Memory/Performance panels or a Chrome DevTools Protocol client; ordinary page JavaScript cannot invoke those profiler domains. Inspect daemon profiles with the Go pprof CLI and the matching daemon binary: `go tool pprof <daemon-binary> <profile-file>`.

The recorder omits prompts, message/tool content, headers, credentials, raw session identifiers, URLs, and workspace paths. Desktop ingestion is authenticated, flag-gated, typed, size-limited, and rate-limited. Profiling, heap estimation, DOM scans, and cache aggregation add CPU and private local disk overhead; enable only for a controlled capture, do not publish the run directory, and disable it afterward.
