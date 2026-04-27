# swarmd (Go backend authority runtime)

Initial backend slice for the Swarm V2 refactor.

Implemented in this iteration:

- daemon bootstrap with `--mode=single|box`
- lockfile guard (single authority per machine/account)
- Pebble-backed persistence
- Codex auth persistence (`auth/codex/default`, API key or OAuth tokens, unencrypted profile)
- Codex Responses transport uses WebSocket-first (`wss://chatgpt.com/backend-api/codex/responses`) with explicit HTTP/SSE fallback
- global model preference (`provider`, `model`, `thinking`)
- modular provider boundary with runnable model providers `codex`, `google`, `copilot`, `fireworks`, and `openrouter` (`exa` remains search-only)
- Fireworks runtime uses the OpenAI-compatible Fireworks Chat Completions API (`https://api.fireworks.ai/inference/v1/chat/completions`) with generic API-key auth
- OpenRouter runtime uses the OpenRouter Chat Completions API (`https://openrouter.ai/api/v1/chat/completions`) with generic API-key auth
- opt-in workspace persistence (saved explicitly)
- HTTP health + API endpoints
- WebSocket channel with `ping`, `subscribe`, `unsubscribe`, and replay support

## Run

`FFF` via the vendored Go/Cgo binding is the canonical in-app search backend for Swarm. The `search` tool uses it directly for ranked file search and content search, so Linux amd64 glibc hosts need the bundled `libfff_c.so` runtime available.

```bash
cd swarmd
SWARM_LANE=main ./scripts/dev-up.sh   # 127.0.0.1:7781
SWARM_LANE=dev  ./scripts/dev-up.sh   # 127.0.0.1:7782
```

## MVP Network Access

Direct private-LAN desktop access is not a supported secure path for the MVP. Keep `swarm.conf` bound to `127.0.0.1` for the desktop/backend unless you are working on the LAN pairing implementation itself.

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
- `POST /v1/sessions/{id}/run` (provider execution loop with concurrent tool calls; `codex`/`google`/`copilot`/`fireworks`/`openrouter`)
- `GET /ws` (WebSocket)

### Copilot Auth Source Of Truth

- The active `copilot` credential in `/v1/auth/credentials` is the canonical runtime auth source.
- Supported Swarm-managed Copilot auth sources are:
  - direct GitHub token stored in the active Swarm credential
  - `copilot login` selected via an active `cli` Copilot credential
  - `gh auth` selected via an active `gh` Copilot credential
- Managed mode (default): when `COPILOT_SIDECAR_URL`/`COPILOT_CLI_URL` are not set, `swarmd` constructs the Copilot SDK client from the selected active auth source. Token-backed sources pass `GitHubToken`; `cli` sources use logged-in-user mode; `gh` sources resolve `gh auth token` at runtime.
- External server mode is not supported for Swarm-managed Copilot credentials. If `COPILOT_SIDECAR_URL` or `COPILOT_CLI_URL` is set, Copilot requests fail explicitly until that override is removed.
- Optional Copilot CLI binary override: `COPILOT_CLI_PATH=/path/to/copilot`.

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
 # Copilot runtime provider is `copilot` (models.dev source provider id: `github-copilot`)
 go run ./cmd/swarmctl model catalog get --provider copilot
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
