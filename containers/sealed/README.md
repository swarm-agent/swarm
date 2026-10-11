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
  agent's sessions and answer that agent's client tool calls. It cannot list
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
| Account spend per day | off | `swarmctl setup daily-limit --usd 25` |

When the daily cap is reached the daemon refuses new runs and stops running
ones. A gateway token can read today's spend (`agents.spendToday()`) but cannot
change the cap. Visitor-level limits (per person, per IP address, bursts,
budget) belong in the gateway.

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

The token is refused unless the agent is sealed. Tool names cannot reuse built-in
names: the runtime runs built-ins first, so a custom tool named `bash` would run
real bash. In the SDK, `client.agents.defineClientTool`, `defineSealedAgent` and
`createGatewayToken` do the same from a trusted owner backend. The gateway answers
calls with `client.agents.serve(sessionId, handlers)`, or with `pendingCalls`,
`answer` and `fail`.

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

Then set it up with `swarmctl setup identity`, `credential`, `workspace` and
`complete` (see `containers/headless/README.md`). Every step runs through
`docker exec`, which needs no shell. Publish the SDK port only to loopback or a
private network; the gateway is the only thing that should face the internet.

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
