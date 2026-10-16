# Sealed agents and the sealed runtime

A **sealed agent** is an agent whose only capabilities are *client tools*: typed
tools your application answers. It has no built-in tools at all: no shell, files,
web, delegation or memory. It can read the conversation, reply, and call the tools
you defined, nothing else. It needs at least one client tool: a custom agent with
every tool off is an ordinary chat agent. This is the shape for agents that face
the public, such as a front desk on a website.

The **sealed runtime** (`containers/sealed/Dockerfile`) is a container with
`swarmd`, `swarmctl`, `git` and their libraries on an empty base. It has no shell,
coreutils, package manager or interpreter.

## How it fits together

```
visitor ── your website ── your gateway ──(agent-bound token)── sealed swarmd ── model provider
                               │  answers client tools with the visitor's identity
                               └─ your database / APIs
```

- The gateway holds an **agent-bound token**. It can only create and use that one
  agent's sessions (list, wait on, end and delete them, and follow their event
  feed), answer that agent's client tool calls and relay the person's
  confirmations (see [Confirmations](#confirmations)). It cannot see other
  sessions, reach any other agent (including `swarm`, which has bash), approve
  anything Swarm would execute, change agents or tools, mint tokens or use MCP. If
  someone later gives the agent a built-in tool, the token stops working instead of
  gaining that tool.
- A client tool call is validated against the tool's JSON Schema **before** the
  gateway sees it. It waits (up to `timeout_ms`, default 30 s, max 300 s) for the
  gateway's answer, which returns to the model labelled as untrusted content with
  any prompt-injection markers found in it. Swarm executes nothing for a client tool.
- Identity never comes from the model. The gateway decides whose data a call may
  see; the model supplies only business inputs such as an order number.
- A sealed agent's system prompt is its own prompt plus a short preamble. The
  coding-agent harness prompt (about 45 KB of internal detail) is not sent.

## Layers and what each one stops

| Layer | Stops | Evidence |
|---|---|---|
| Tools are checked when they run, not just when offered | any built-in tool, invented tool or other agent's tool the model names | `TestSessionsV3RefusesToolsTheAgentWasNotOffered`, `TestRunTurnRefusesToolsTheRunDidNotOffer`, red team |
| Schema and size checks on client tool arguments | malformed, extra-field, oversized and wrong-type arguments | `TestSessionsV3ClientToolIsAnsweredByTheClient`, red team |
| Agent-bound token (default-deny) | a leaked gateway token reaching the machine | `TestAgentBoundTokenIsDefaultDeny`, red team |
| End user stored at create time, delivered with each call | the model choosing whose data a tool call sees; one visitor using up everyone's limits | `TestSessionsV3EndUserIsSetOnceByTheGateway`, `TestClientToolDeliveryCarriesTheStoredEndUser`, `TestAgentBoundEndUserLimitsAreEnforcedAndDistinct` |
| User-only messages | forged assistant or system turns | `TestSessionsV3MessageRejectsCallerAssistantAndSystemTurns`, red team |
| No shell in the image | command execution, even if the layers above fail | mutant run below |
| Workspace scope plus separate path approvals | file access outside the scratch workspace | mutant run below |

### What happens if the first layer fails

The red team was also run against a deliberately broken daemon, with tool
enforcement and every permission check turned off. Build it with
`scripts/testbench-scripted-overlay.py --disable-enforcement`; it is a test build
only and must never be shipped. Results:

- `bash` was approved and failed with `exec: "bash": executable file not found`.
  Nothing ran.
- `read` and `list` outside the workspace stopped at a separate path approval,
  which the gateway token cannot give.
- `write` and `edit` **did** change files in the workspace. Point a sealed
  deployment's workspace at a dedicated empty scratch repository, never real code.
- `webfetch` and `websearch` failed only because no Exa key was configured. Block
  outbound traffic except to the model provider at the network level.

So the image removes command execution outright. File and network tools still
depend on tool enforcement plus the deployment choices above.

## Limits

Every message to a sealed agent is bounded, whatever the model does:

| Limit | Default | Set with |
|---|---|---|
| Model calls per message (the last one gets no tools) | 4 | agent `limits.max_steps` |
| Output tokens per call | 1024 | `limits.max_output_tokens` |
| History sent (newest messages, fresh provider context) | 40 messages | `limits.max_history_messages` |
| Run deadline | 60 s | `limits.run_timeout_ms` |
| New messages per gateway token | 120 / minute | `sdk-token --messages-per-minute` |
| New conversations per gateway token | 300 / hour | `sdk-token --sessions-per-hour` |
| Model runs at once (the agent's conversations, per gateway token) | 4 | `sdk-token --max-concurrent-runs` (1-64) |
| New messages per end user | 20 / minute | `sdk-token --end-user-messages-per-minute` |
| New conversations per end user | 30 / hour | `sdk-token --end-user-sessions-per-hour` |
| Model tokens per end user per UTC day | off | `sdk-token --end-user-max-tokens-per-day` |
| Open waits and feed long-polls per token | 32 (then 429) | fixed |
| Conversation retention (ended or idle) | kept until deleted | agent `limits.retention_days` (1-3650) |
| Account spend per day | off | `swarmctl setup daily-limit --usd 25` |

When the daily cap is reached the daemon refuses new runs and stops running
ones. A gateway token can read today's spend (`agents.spendToday()`) but cannot
change the cap. The end-user limits apply only to conversations the gateway
created with an `end_user` (below); a conversation without one counts against
the token's limits only. Limits by IP address or other signals the daemon cannot
see still belong in the gateway.

A refused request gets `429` with `Retry-After` (also `retry_after_seconds` in
the body), `limit_scope` (`token` or `end_user`) and a `code`:
`token_messages_rate_limited`, `token_sessions_rate_limited`,
`token_concurrent_runs_limited`, `end_user_messages_rate_limited`,
`end_user_sessions_rate_limited` or `end_user_daily_tokens_exceeded` (Retry-After
runs to UTC midnight). A request refused by one limit spends nothing from the
others. The daily token cap is checked before each message from the end user's
recorded receipts; a run already going finishes.

The message and conversation counters are stored in the daemon's database, so a
restart does not reset them. Each counter is deleted once it has refilled (at
most an hour after its last use), and new ones are only created by requests that
pass the token's own limits, so the table stays small; past 500 000 active
counters new ones are refused (`rate_limit_capacity_reached`). End-user counters
belong to the agent, not the token: a rotated gateway token for the same agent
continues the same end user's counts. The concurrent-run limit is counted from
the runs in flight and needs no stored counter. A confirmation decision (see
[Confirmations](#confirmations)) starts a run, so it counts against the
concurrent-run limit but not against the message rates.

The owner changes a gateway token's limits in place, without reissuing it:

```sh
docker exec swarm-sealed swarmctl setup sdk-token-limits --id "$TOKEN_ID" \
   --end-user-messages-per-minute 10 --end-user-max-tokens-per-day 200000
```

`POST /v3/auth/tokens/{id}/limits` takes any of `messages_per_minute`,
`sessions_per_hour`, `max_concurrent_runs`, `end_user_messages_per_minute`,
`end_user_sessions_per_hour` and `end_user_max_tokens_per_day`; fields left out
keep their value and `0` restores the default. SDK:
`agents.updateGatewayTokenLimits(tokenId, limits)`. A gateway token cannot call
it. `swarmctl setup sdk-tokens` lists each gateway token's limits.

## End users

The gateway can tell Swarm whose conversation a session is, so it does not have
to keep its own map from session to customer:

```ts
const { sessionId } = await gateway.agents.startConversation('frontdesk', {
  endUser: 'cust_8f2c', labels: { plan: 'pro' }, clientRequestId: visitId,
});
await gateway.agents.converse(sessionId, text, handlers, { endUser: 'cust_8f2c' });
// handlers get call.endUser and call.labels with every client tool call.
```

- `end_user` (on `POST /v3/sessions`) is an opaque id of 1-128 letters, digits
  and `. _ : @ + = -`, compared case-sensitively. `labels` is at most 16 entries:
  lowercase identifier keys (up to 64 characters) and printable values up to 256
  characters. Use your own ids, not e-mail addresses or names.
- Only a gateway token may set them, only for its own agent, and only when the
  session is created. Retrying the create with a different `end_user` is a
  `409`; the metadata route refuses the `swarm_v3_end_user` key; nothing the
  model writes can change them. They are never sent to the model.
- Session reads (`GET /v3/sessions/{id}` and the create response) return
  `end_user` and `labels`, and so does `GET /v3/sessions/{id}/permissions`, which
  is what `pendingCalls` reads: each `ClientToolCall` carries `endUser` and
  `labels` taken from the stored session. Decide whose data a call may see from
  `call.endUser`, never from the tool's arguments. `serve` and `converse` with
  `endUser` refuse a session that belongs to anyone else.
- `GET /v3/sessions:end-user-usage` returns one UTC day's model usage of the
  agent's conversations grouped by end user (`date=YYYY-MM-DD`, default today;
  `end_user=` for one; `limit`, default 100, max 1000). A gateway token reads its
  own agent; the owner passes `agent_name`. The figures are the projections of
  the provider receipts (tokens, costs by pricing source), not estimates. SDK:
  `agents.endUserUsage({ endUser, date })`.

## Define a sealed agent

```json
{
  "tools": [
    {"name": "lookup_order", "description": "Look up the visitor's order by its code.",
     "input_schema": {"type": "object", "properties": {"order_id": {"type": "string", "pattern": "^[A-Z0-9]{6}$"}},
                      "required": ["order_id"], "additionalProperties": false},
     "effect": "read", "timeout_ms": 5000}
  ],
  "agent": {"name": "frontdesk", "prompt": "You answer visitors' questions about their orders.",
            "provider": "codex", "model": "gpt-5.5", "thinking": "medium", "tools": ["lookup_order"]}
}
```

```sh
docker exec -i swarm-sealed swarmctl setup sealed-agent --stdin < frontdesk.json
(umask 077; docker exec swarm-sealed swarmctl setup sdk-token --agent frontdesk \
   --name gateway --expires-in-seconds 2592000 > gateway-token.json)
```

The token is refused unless the agent is sealed, and it must expire within 30
days (`--expires-in-seconds`, or `expiresInSeconds` in the SDK). When it creates
a session it may send only `agent_name`, `client_request_id`,
`idempotency_key`, `title`, `mode`, `workspace_path`, `workspace_id`, `end_user`
and `labels`; any other field is refused. Tool names cannot reuse built-in
names: the runtime runs built-ins first, so a custom tool named `bash` would run
real bash. In the SDK, `client.agents.defineClientTool`, `defineSealedAgent` and
`createGatewayToken` do the same from a trusted owner backend. The gateway answers
calls with `client.agents.serve(sessionId, handlers)`, or with `pendingCalls`,
`answer` and `fail`.

Every key minted over `POST /v3/auth/tokens` must expire. A request without
`expires_in_seconds`, or with 0 or less, is refused with 400 (there is no
default lifetime), and so is one over the kind's maximum: 30 days (2592000) for
gateway and operator tokens, 365 days (31536000) for client, SDK, monitoring and
AI keys. `swarmctl setup sdk-token` still defaults to one hour and always sends
it; its own maxima are unchanged (1 day for session keys, 30 days with
`--agent`, 365 days with `--monitor` or `--ai-access`). In the SDK the expiry is
a required parameter of `createGatewayToken`, `createOperatorToken`
(`expiresInSeconds`), `auth.createScopedToken` and `auth.createAIKey`
(`expires_in_seconds`). A gateway or operator token stored before this rule
is refused when used (a `token.denied` signal and one daemon log line): mint
a new one. Other keys stored before this rule keep working until revoked: `GET /v3/auth/tokens` and `swarmctl setup sdk-tokens` mark them
`"never_expires": true`, and at each start the daemon raises one
`token.never_expires` warning signal per account listing their IDs. Nothing is
revoked automatically. Trigger worker, remote transport and runner credentials
are minted without an expiry by their own features and are marked the same way.

## Confirmations

A client tool that makes changes waits for the person to approve each call
before the gateway runs it. Write tools (`"effect": "write"`) are confirmed by
default; set `"confirm": false` to turn that off, or `"confirm": true` on a read
tool. `"confirm_timeout_ms"` is how long a call waits for the decision (default
900000, 15 minutes; at most one day).

1. The model calls the tool. Swarm offers confirmed tools with one extra
   required argument, `summary`: one sentence for the person. Swarm validates
   the call, checks the permission policy, and stores a pending confirmation on
   the session: the tool, the arguments without `summary`, their hash
   (`arguments_hash`, `sha256:` of their canonical JSON), the summary and
   `expires_at`. Nothing reaches the gateway as a call to run. The run ends
   without another model call, so waiting costs no tokens.
2. The gateway shows an approval card. It finds the confirmation with
   `GET /v3/sessions/{id}/confirmations` (also `pending_confirmation` on
   `GET /v3/sessions/{id}`, and `pending_confirmations` on the session's wait,
   which wakes with reason `confirmation`).
3. It sends the person's decision:
   `POST /v3/sessions/{id}/confirmations/{cid}`
   `{"decision": "approve" | "reject", "note": "...", "arguments_hash": "..."}`.
   An approval must carry the `arguments_hash` of the arguments on the card, so
   it can only run those arguments.
4. On approval the call becomes an ordinary pending client tool call with
   exactly those arguments, which the gateway answers as usual (with the
   visitor's identity). Its result or failure goes to the model. A decline
   gives the model the tool error `the person declined: <note>`. If nobody
   decides in time, the model is told nobody answered and a late approval is
   refused (409). Each outcome is added to the conversation as that tool's
   result, and a new run lets the model react.

A session has at most one open confirmation; while it is open, another
confirmed call fails with a tool error. Repeating a decision returns the
current state; a different decision is a conflict (409). The state is durable
session metadata (`client_tool_confirmation`), so it survives a restart. An
approved call that is still waiting for the gateway also survives a restart. The
expiry timer does not, so after a restart an expired confirmation is closed the
next time the gateway reads the session's confirmations, decides one, answers a
call, or posts a message. The gateway token reaches these routes for its own
agent's sessions; the operator token cannot. Only the owner or the gateway token
decides a confirmation; an SDK or client app key may read confirmations but
gets 403 on a decision. On a conversation created with an
`end_user`, the confirmation routes return the session's `end_user` and
`labels` next to the confirmation, and the approved call is delivered with
them like any other call, from the stored session.

In the SDK, `converse` returns `confirmation` when the turn ended by asking.
`confirm` and `reject` record the decision, run the approved call through your
handlers and return the agent's next reply:

```ts
const turn = await gateway.agents.converse(sessionId, text, handlers);
if (turn.confirmation) {
  // Show turn.confirmation.summary and .arguments with Approve / Decline.
  await saveCard(visitor, turn.confirmation);
}
// Later, when the visitor clicks:
const card = await loadCard(visitor);
const next = approved
  ? await gateway.agents.confirm(sessionId, card, handlers)
  : await gateway.agents.reject(sessionId, card, handlers, { note: 'Not now' });
reply(next.reply);
```

`pendingCalls` lists confirmations as items with `kind: "confirmation"` next to
calls (`kind: "call"`); both carry `endUser` and `labels`. `serve` never
answers a confirmation; it calls `onConfirm(confirmation)` once for each. With
`endUser`, `converse`, `confirm` and `reject` refuse another end user's
session before sending anything, and `serve` neither runs that end user's calls
nor shows their confirmations. `decideConfirmation` and `confirmations` are the
plain route calls.

## Human takeover

A person can take a conversation from the agent, reply, and hand it back. The
state is durable session metadata (`takeover`), so the gateway sees it on the
session it already reads. Only a sealed agent's conversations can be taken over (409
otherwise), and only by the owner or the agent's operator token: an SDK or
client app key gets 403 on these routes.

| Route | Does |
|---|---|
| `GET /v3/sessions/{id}/takeover` | who holds the conversation |
| `POST /v3/sessions/{id}/takeover` `{"action":"start"\|"end","client_request_id"}` | start cancels the agent's run in flight; end hands back |
| `POST /v3/sessions/{id}/operator-messages` `{"content","client_request_id"}` | reply as a person (only during a takeover) |

While a person holds the conversation, a visitor message is kept with a
`dispatch_blocked` run intent (reason `human_takeover`) and no model run starts.
A reply is an assistant message with `source: "operator"`, so the agent sees it
after hand-back. The gateway token cannot reach these routes. The key for
whoever answers (a support hub) is a separate operator token, which can only
read the agent's conversations, take them over, reply and hand back:

```sh
(umask 077; docker exec swarm-sealed swarmctl setup sdk-token --agent frontdesk --operator \
   --name hub --expires-in-seconds 2592000 > operator-token.json)
```

In the SDK: `agents.createOperatorToken`, `takeOver`, `replyAsOperator`,
`handBack` and `takeoverState`.

## Waiting, session lifecycle and the event feed

A gateway does not need to poll. Every route below is open to the gateway token
for its own agent only (another agent's token gets 403); the operator token may
use the read-only ones (wait, list, feed).

| Route | Does |
|---|---|
| `GET /v3/sessions/{id}/wait?after=<cursor>&wait_seconds=<0-30>` | without `after`: the current state and `cursor` at once. With it: returns as soon as an event after the cursor is an assistant message, a pending tool call, a confirmation waiting for the person, the end of a run or the end of the session (`reasons`), or after the wait (`timed_out`) |
| `GET /v3/sessions:agent?state=active\|ended\|all&updated_after=&updated_before=&cursor=&limit=` | the agent's conversations, most recently updated first, 50 per page (at most 200); page with `next_cursor` |
| `POST /v3/sessions/{id}/end` `{"client_request_id","reason"}` | ends the conversation: no more messages (409), takeovers, operator replies or confirmation decisions (409); its run in flight and pending tool calls are stopped, and an open confirmation is closed without starting a run (a pending one expires; the call an approval created is failed). Ending again changes nothing (`already_ended`) |
| `DELETE /v3/sessions/{id}` | hard deletes the conversation and its messages; refused (409) while a run or tool call is in flight, so end it first |
| `GET /v3/sessions:agent-events?after=N&limit=N&kind=message,tool&wait=S` | the agent's event feed, oldest first, like `/v3/signals` |

A wait answer carries `cursor` (pass it back as `after`), `reasons`
(`assistant_message`, `tool_call`, `confirmation`, `run_ended`,
`session_ended`), `timed_out`, `more` (more events follow the cursor),
`ended`, `run` (`run_id`, `status`, `active`), `pending_calls` (the
session's pending tool calls, including the call an approved confirmation
created, which wakes the wait as `tool_call`), `pending_confirmations` (the
confirmation waiting for the person, if any) and, on a conversation created with
an `end_user`, `end_user` and `labels` from the stored session. Streaming deltas, usage and run phases move the cursor without ending the
wait. A token may hold 32 waits and feed long-polls open at once.

Feed entries are `{seq, id, at, kind, session_id, end_user, labels, data}`
(`end_user` and `labels` only on conversations created with them) with kinds
`session.created`, `message.user`, `message.assistant` (`data.source`
`operator` for a person's reply), `tool.called`, `tool.answered`,
`tool.failed`, `confirmation.requested` (`confirmation_id`, `tool`,
`summary`, `arguments`, `run_id`), `confirmation.decided` (`decision`:
`approved`, `rejected` or `expired`, and the person's `note`),
`session.ended`, `run.failed` (run ID and status, never the reason),
`takeover.started` and `takeover.ended`. Message text, tool arguments,
results and reasons, and confirmation summaries, arguments and notes are cut to
2000 characters with `data.truncated`. Listed sessions also carry `end_user`
and `labels`. The feed is read from the sessions' own durable change log, so
it is only as old as that log's replay window (30 days, at least the newest
100,000 changes on the machine); a cursor older than that, or ahead of the feed,
comes back with `gap: true`. A deleted conversation's entries disappear with it.
The feed is not pushed to signal sinks: it carries conversation text, which the
signal feed never does.

**Retention.** Set `limits.retention_days` on the sealed agent (in the
`sealed-agent` file or `defineSealedAgent`) and a background pass, every 10
minutes, hard deletes its conversations that ended or saw no activity for that
many days, 100 per pass, oldest first. A conversation with a run or tool call in
flight waits for the next pass. Each pass logs what it deleted; a delete that
fails is logged and raises a `session.retention.failed` signal naming the agent.
The default keeps conversations until deleted; the maximum is 3650 days. Only
sealed agents are swept.

In the SDK: `agents.wait`, `listSessions`, `endSession`, `deleteSession`,
`events` and `watchEvents` (an async iterator that waits between reads and
yields `{kind: 'gap'}` for missed entries). `converse`, `serve` and
`pendingCalls`, `confirm` and `reject` use `wait`, which also carries the
pending confirmations, so nothing polls for them. Against a daemon that predates
it (it answers the wait route with 404 or its unknown-route refusal) they poll as
before (the permissions and confirmations routes) and say so once with
`console.debug`; any other error is thrown.

## Build and run

```sh
docker build -t swarm-headless:local .
docker build -t swarm-sealed:local -f containers/sealed/Dockerfile .
docker run -d --name swarm-sealed --cap-drop ALL --security-opt no-new-privileges --read-only \
  --tmpfs /tmp:uid=10001,gid=10001,mode=1777 --tmpfs /run/swarmd:uid=10001,gid=10001,mode=0700 \
  --tmpfs /home/swarm:uid=10001,gid=10001,mode=0700 \
  -v swarm-sealed-config:/etc/swarmd -v swarm-sealed-data:/var/lib/swarmd \
  -v swarm-sealed-cache:/var/cache/swarmd -v swarm-sealed-logs:/var/log/swarmd \
  --mount type=bind,src="$SCRATCH_REPO",dst=/project \
  -p 127.0.0.1:7783:7783 swarm-sealed:local --container-sdk-port=7783
```

A box with no internet access (a private network, as for a sealed agent)
should set `SWARM_MODEL_CATALOG=embedded` in the daemon environment: Swarm
then uses the model snapshot built into the binary and never fetches one, so
setup does not fail on `models.swarmagent.dev`. Without it, first-provider
setup needs that host. Any other value stops the daemon at start.

Then set it up with `swarmctl setup identity`, `credential`, `workspace` and
`complete` (see `containers/headless/README.md`). Every step runs through
`docker exec`, which needs no shell. Publish the SDK port only to loopback or a
private network; the gateway is the only thing that should face the internet.

## Gemini on Vertex (no key)

Provider `vertex` runs the same Gemini models as `google`, through Vertex AI,
signed in as the machine's own Google identity. No API key exists, and Swarm
stores only a Google Cloud project and location. It suits a sealed swarmd in
Docker on a GCE VM.

On Google Cloud:

- Attach a service account to the VM and grant it `roles/aiplatform.user` on
  the project. Enable the Vertex AI API (`aiplatform.googleapis.com`).
- Give the VM the `cloud-platform` access scope (`--scopes=cloud-platform`).
  The default scopes do not cover Vertex AI, and Vertex then answers 403.
- For a VM without internet access, turn on Private Google Access on its subnet
  and add a private DNS zone for `googleapis.com` that points `*.googleapis.com`
  at `private.googleapis.com` (199.36.153.8/30), with a route to that range.

Swarm then needs two things from inside the container. It needs the metadata
server at `http://169.254.169.254` for tokens (the address behind
`metadata.google.internal`, so no DNS lookup is needed; a Docker bridge
container reaches it). It also needs `aiplatform.googleapis.com:443`, or
`REGION-aiplatform.googleapis.com:443` for a regional location. Swarm sends
`Metadata-Flavor: Google`, never through a proxy, and keeps the token in memory
until two minutes before it expires. Tokens are never stored or logged. Off
Google Cloud, set `GOOGLE_APPLICATION_CREDENTIALS` in the daemon's environment
to a service-account key file mounted read-only for UID 10001. When that
variable is set, the key file is the only source: if it is unusable, requests
fail rather than falling back to the metadata server.

```sh
docker exec swarm-sealed swarmctl setup credential --provider vertex --project my-project
# or a region: ... --provider vertex --project my-project --location us-central1
for role in action plan compact finder coder designer router; do
  docker exec swarm-sealed swarmctl setup model --role "$role" \
    --provider vertex --model gemini-3.5-flash-lite --thinking low
done
```

`--location` defaults to `global` (host `aiplatform.googleapis.com`); a region
such as `us-central1` uses `us-central1-aiplatform.googleapis.com`. Like an API
key, this is the first-provider onboarding step. The daemon checks the project
and location and fetches a token before it saves anything. If it refuses, the
CLI prints why (for example `no GCE metadata server reachable` or `no service
account`). The provider shows as ready only after a token fetch succeeds.
Setup does not call Vertex itself, so a missing role shows up on the first run
as `vertex generateContent failed status=403 (permission denied: ...)`. The API
equivalent is `POST /v1/auth/credentials` (or `/v1/onboarding/provider/credential`)
with `{"provider":"vertex","type":"adc","google_project":"my-project","google_location":"global","active":true}`.

A sealed agent selects Vertex in its definition:

```json
"agent": {"name": "frontdesk", "prompt": "...", "provider": "vertex",
          "model": "gemini-3.5-flash-lite", "thinking": "low", "tools": ["lookup_order"]}
```

Requests use the Gemini `generateContent` and `streamGenerateContent` schema
with `Authorization: Bearer`. 429 and 5xx responses retry the same way as
`google`. A 401 fetches a fresh token and retries once. Usage is recorded under
provider `vertex`, and the spend estimate uses Google's catalog prices. Not
supported yet: image input, service tiers, and domain-scoped project IDs.

## Red team

`containers/sealed/test/run.sh` runs `redteam.ts` against a real sealed container
whose model is a script that plays a hijacked LLM. It needs no provider key. The
image must include the scripted model, which only a test overlay adds:

```sh
python3 scripts/testbench-scripted-overlay.py --source . --output "$OUT"
(cd swarmd && go build -overlay "$OUT/scripted-overlay.json" -o "$OUT/swarmd" ./cmd/swarmd \
             && go build -o "$OUT/swarmctl" ./cmd/swarmctl)
# Put both binaries over swarm-headless:local as swarm-headless:scripted, then:
docker build --build-arg SWARM_RUNTIME=swarm-headless:scripted -t swarm-sealed:scripted -f containers/sealed/Dockerfile .
bash containers/sealed/test/run.sh
```

It checks:
- that every built-in tool is refused when it runs (23 attempts);
- schema violations, cross-visitor access, prompt injection in a tool result, and a timed-out gateway;
- that the model is offered only its own tool and its own prompt;
- forged turns and the token's limits;
- that no shell starts in the container and nothing outside the data volumes changed.

Verdicts come from what the daemon did, as seen by the model and the API, never
from what the model said. `REDTEAM_SHOW_BUILTINS=1` prints each built-in call's
outcome instead, which is how the mutant run above was read.

## Not covered yet

- Real-model runs: the scripted model proves the server boundary, not how a real
  LLM behaves.
- Network egress control. That belongs to the deployment.
- Per-session containers.
- A load test.
