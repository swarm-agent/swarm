# Public chat: a website assistant on a sealed Swarm agent

An always-on "ask us anything" chat for a public website. A sealed agent answers
from an approved playbook, captures leads, asks for a person and can end an
abusive chat; a person can take any conversation over and hand it back. Everything the visitor can reach is
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
| Agent and tools | `agent.json` | the prompt, four client tools, model and limits |
| Approved answers | `playbook.json` | what `get_playbook` returns; edit for your business |
| Gateway | `gateway/`, `Dockerfile` | the gateway and operator tokens (files), visitor map, leads |
| Box | `compose.yaml` | sealed swarmd, gateway, cloudflared |
| Edge | `worker/` | the gateway key and Access service token (Worker secrets) |
| Widget | `widget/chat.js` | nothing secret |
| Staff console | `console/` | the ops key and Access service token (Worker secrets) |

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
docker compose exec swarm git init -q -b main /project
docker compose exec swarm git -C /project -c user.name=scratch -c user.email=scratch@localhost \
  commit -q --allow-empty -m "empty scratch workspace"
docker compose exec swarm swarmctl setup workspace --path /project --name Scratch
docker compose exec swarm swarmctl setup complete
docker compose exec swarm swarmctl setup daily-limit --usd 5
docker compose exec -T swarm swarmctl setup sealed-agent --stdin < agent.json
```

The role models are the daemon's own defaults, which setup requires; the public
agent uses the provider and model in `agent.json` (`google`,
`gemini-3.5-flash-lite` here). Change any of them to a model your account has. The
workspace is an empty scratch volume: the agent has no file tools, and it must
never point at real code. A workspace must be a Git repository with a commit, so
the two `git` lines make an empty one (the sealed image has git but no shell).

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
# Compose mounts secret files as they are, and the gateway runs as uid 10001.
sudo chown 10001:10001 secrets/swarm_* secrets/*_secret && chmod 0400 secrets/swarm_* secrets/*_secret
```

Both Swarm tokens expire within 30 days; mint new ones and restart the gateway
before they do.

Restrict the provider key to the model API and cap its daily requests in the
provider's console. An IP restriction only works if you know the address the
provider sees: on Google Cloud, calls to Google APIs from a VM behind Cloud NAT
take Google's private path, not the NAT address, so an IP-locked Gemini key is
refused there.

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
  `conversation.ended`, `conversation.reopened`, `person.offered`,
  `person.declined`, `handoff.accepted`, `handoff.missed`, `visitor.message`
  (during a takeover) and `chat.error`, signed with
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
| `POST /ops/reopen` `{session_id}` | undo the agent's `end_conversation` |
| `POST /ops/presence` `{id, name, role, available}` | heartbeat: who is online (lapses after 90 s) |
| `GET /ops/queue` | visitors waiting for a person, with when their request lapses |
| `POST /ops/accept` `{session_id, name, role}` | take over and tell the visitor who joined |
| `GET /ops/events?after=&wait=` | long poll (up to 25 s) for new events, each with a `seq` |

While a person holds a conversation, the visitor's messages are kept and start
no model run; the widget polls `/api/chat/replies` and shows the person's
replies. A `handoff.requested` event is the cue to take over.

## Live hand-off to a person

1. The staff console sends a presence heartbeat while someone is set to
   available; the widget's header reads "Sam is online" from `/api/chat/status`.
2. When a visitor asks for a person or wants to discuss their own project, the
   agent calls `offer_person`. If someone is online, it asks "Would you like to
   talk to Sam, the founder?" and the widget shows a Yes button; if not, it
   offers to take their email instead.
3. Yes (`/api/chat/person`, no model run) puts the visitor in the queue and emits
   `handoff.requested`; the console's long poll wakes, it plays a tone and shows
   a desktop notification.
4. Accept takes the conversation over; the widget says "Sam joined the chat" and
   shows Sam's replies with their name. Hand back returns it to the agent.
5. If nobody accepts within two minutes (`ACCEPT_TIMEOUT_MS`), the request
   becomes `missed` and the widget asks the visitor to leave their email
   (`/api/chat/contact`, no model run). It lands in `leads.jsonl`, on the
   conversation in the console (with an email-reply link) and as `lead.captured`.

A page refresh keeps the conversation: the widget stores its random visitor id
and reloads the visitor's own messages from `/api/chat/history`. That id is the
only key to the conversation, so it is long and random and never shown.

The console (`console/`) is a Worker on its own hostname behind an Access
application for your team's emails. It verifies the Access token itself, takes
the staff member's name from that login (the `STAFF` var), forwards only the
listed ops routes with only their reviewed fields, and serves its page under a
strict Content-Security-Policy.

## Ending a chat

The agent's `end_conversation` tool (reason `abuse` or `cannot_help`, plus an
internal note) is a hard stop the model can trigger but not undo. The gateway
records it against the visitor and answers every later message with
`{"status":"ended"}` before any daemon call, so no model run or spend follows;
the widget shows the goodbye and disables its input. Only a person reopens it
(`/ops/reopen`). A visitor can still clear their browser storage and start a new
conversation; the Worker's per-IP limit is what bounds that.

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
