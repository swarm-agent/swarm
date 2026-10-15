# Public chat: a website assistant on a sealed Swarm agent

An always-on "ask us anything" chat for a public website. A sealed agent answers
from an approved playbook, captures leads and asks for a person; a person can
take any conversation over and hand it back. Everything the visitor can reach is
narrow, rate-limited and replaceable.

```
visitor ── site + widget/chat.js
              │ HTTPS
              ▼
        worker/ (Cloudflare Worker)   origins, per-IP limit, Turnstile, size caps, kill switch
              │ Cloudflare Tunnel + Access service token (nothing listens on the box)
              ▼
        gateway/ (Node, this directory)   visitor → conversation map, client tools,
              │ gateway token          per-visitor limits, takeover routing, hub events
              ▼
        sealed swarmd (containers/sealed)   only client tools, run/step/token limits,
              │                             concurrency + daily spend caps
              ▼
        model provider
```

| Piece | Path | Holds |
|---|---|---|
| Agent and tools | `agent.json` | the prompt, three client tools, model and limits |
| Approved answers | `playbook.json` | what `get_playbook` returns; edit for your business |
| Gateway | `gateway/`, `Dockerfile` | the gateway and operator tokens (files), visitor map, leads |
| Box | `compose.yaml` | sealed swarmd, gateway, cloudflared |
| Edge | `worker/` | the gateway key and Access service token (Worker secrets) |
| Widget | `widget/chat.js` | nothing secret |

## Run it

Prerequisites: a Linux host with Docker (any small VM; nothing is published on
it), a Cloudflare account with a domain, a model provider key, Node 22 for the
Worker deploy. Run from the repository root unless noted.

**1. Images.**

```sh
docker build -t swarm-headless:local .
docker build -t swarm-sealed:local -f containers/sealed/Dockerfile .
docker build -t public-chat-gateway:local -f examples/public-chat/Dockerfile .
```

**2. Start the daemon and set it up** (from `examples/public-chat`). Every step
runs through `docker compose exec`, which needs no shell in the image.

```sh
cd examples/public-chat
docker compose up -d swarm
docker compose exec swarm swarmctl setup identity --username owner --name "Public chat"
docker compose exec -T swarm swarmctl setup credential --provider google --api-key-stdin < "$PROVIDER_KEY_FILE"
for role in action plan compact finder coder designer router; do
  docker compose exec swarm swarmctl setup model --role "$role" \
    --provider google --model gemini-3.5-flash-lite --thinking low
done
docker compose exec swarm swarmctl setup workspace --path /project --name Scratch
docker compose exec swarm swarmctl setup complete
docker compose exec swarm swarmctl setup daily-limit --usd 5
docker compose exec -T swarm swarmctl setup sealed-agent --stdin < agent.json
```

The role models are the daemon's own defaults, which setup requires; the public
agent uses the provider and model in `agent.json` (`google`,
`gemini-3.5-flash-lite` here). Change any of them to a model your account has. The
workspace is an empty scratch volume: the agent has no file tools, and it must
never point at real code.

**3. Keys** (mode 0600 under `./secrets`, which git ignores).

```sh
mkdir -p secrets && chmod 700 secrets
(umask 077
 docker compose exec swarm swarmctl setup sdk-token --agent frontdesk --name gateway \
   --expires-in-seconds 2592000 --messages-per-minute 30 --sessions-per-hour 100 \
   --max-concurrent-runs 4 | jq -r .token > secrets/swarm_gateway_token
 docker compose exec swarm swarmctl setup sdk-token --agent frontdesk --operator --name hub \
   --expires-in-seconds 2592000 | jq -r .token > secrets/swarm_operator_token
 for s in gateway_secret ops_secret hub_events_secret; do openssl rand -hex 32 > secrets/$s; done)
```

Both Swarm tokens expire within 30 days; mint new ones and restart the gateway
before they do.

**4. Cloudflare.**

- Create a Tunnel. Give it a public hostname (for example
  `chat-gateway.example.com`) that routes to `http://gateway:8080`, and put its
  token in `secrets/tunnel.env` as `TUNNEL_TOKEN=...`.
- Put that hostname behind a Cloudflare Access application whose only policy is
  a service token. The Worker presents it; nobody else gets through.
- In `worker/wrangler.jsonc` set `ALLOWED_ORIGINS` and `GATEWAY_URL`. Then, in
  `worker/`, put the secrets listed at the top of that file (`GATEWAY_SECRET` is
  the content of `secrets/gateway_secret`) and run `npx wrangler deploy`.

**5. Start the box.**

```sh
CLOUDFLARED_VERSION=<pinned release> BOOKING_URL=https://example.com/book docker compose up -d
```

**6. Embed the widget** on your site, pointing at the Worker's URL:

```html
<script src="/chat.js" defer data-endpoint="https://public-chat-edge.example.workers.dev"
        data-title="Ask us anything" data-fallback="Email hello@example.com"></script>
```

## Limits, in layers

| Layer | Where | Default here |
|---|---|---|
| Origins, per-IP rate, body and message size, human check | Worker | site origins; 20/min per IP; 4 KB body, 2,000 chars; Turnstile once per visitor (optional) |
| Per-visitor rate, conversation length, requests in flight | gateway | 20 messages / 10 min; 30 messages; 8 |
| Messages and conversations per token | daemon (gateway token) | 30/min, 100/hour |
| Model runs at once | daemon (gateway token) | 4 |
| Cost of one message | daemon (agent `limits`) | 3 model calls, 600 output tokens, last 20 messages, 30 s |
| Spend per day | daemon (`setup daily-limit`) | $5; the gateway answers "resting" once reached |

Set an outside budget alert on the provider account as well; it is independent
of everything above.

## Monitoring and self-healing

- **Restarts:** every container restarts on failure; the gateway's health check
  (`/ready`) makes a real authenticated call to the daemon.
- **Signals:** the daemon raises `session.run.failed` and
  `session.run.interrupted` for a chat that broke, alongside its other signals.
  Forward them with a signal sink (`/v3/signals/sinks`, HMAC-signed, heartbeat).
- **Gateway events:** set `HUB_EVENTS_URL` and the gateway POSTs
  `conversation.started`, `lead.captured`, `handoff.requested`,
  `visitor.message` (during a takeover) and `chat.error`, signed with
  `X-Signature: sha256=<HMAC of the body>` using `hub_events_secret`.
- **Fallback:** if the box is unreachable, paused or over budget, the Worker
  says so and the widget shows its fallback text instead of breaking.

## Human takeover

The ops routes on the gateway (header `X-Ops-Key`, reached through the same
tunnel; put a separate Access policy in front for your hub) use the operator
token:

| Route | Does |
|---|---|
| `GET /ops/conversations` | known conversations and their sessions |
| `GET /ops/transcript?session_id=` | the conversation and who holds it |
| `POST /ops/takeover` `{session_id}` | a person takes over; the agent's run in flight stops |
| `POST /ops/reply` `{session_id, content}` | reply as a person |
| `POST /ops/handback` `{session_id}` | the agent answers the next message |

While a person holds a conversation, the visitor's messages are kept and start
no model run; the widget polls `/api/chat/replies` and shows the person's
replies. A `handoff.requested` event is the cue to take over.

## Security boundaries and what is not covered

- The daemon's SDK port exists only on the private `bots` network; the gateway
  is reached only through the tunnel, by keys; nothing is published on the host.
- The gateway token can only create and use the agent's sessions and answer its
  client tools; the operator token can only read them, take over, reply and hand
  back. Neither can reach any other agent, approve anything, or mint tokens.
- Identity never comes from the model: the gateway maps visitors to sessions and
  binds each tool call to its own record of who is asking.
- Restrict the box's outbound traffic to the model provider and Cloudflare at the
  host or cloud firewall; compose cannot.
- The tests (`npm test` here, Go tests in `swarmd`) are hermetic: fake SDK
  clients, fixed clocks, no daemon or model. They prove the gateway's and edge's
  boundaries, not how a real model behaves, a deployed Cloudflare
  configuration, or load. Run a real conversation end to end before going live.

## Tests

```sh
npm --prefix packages/sdk ci && npm --prefix packages/sdk run build
npm --prefix examples/public-chat ci
npm --prefix examples/public-chat test
```
