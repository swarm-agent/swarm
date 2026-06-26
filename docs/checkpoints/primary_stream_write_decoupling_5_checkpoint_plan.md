# Primary assistant stream/write decoupling — explicit five-checkpoint implementation plan

**Repository baseline:** `dev` at `bf60b4e00285184f976aebc8ce9ae94eb41b69a2`  
**Purpose:** eliminate the high-rate pattern `persist -> emit -> wait for persist -> emit` for primary assistant text so a fast provider can continue feeding the browser while durable Pebble checkpoints happen independently.

This document replaces the earlier plan. It is intentionally prescriptive. Do not substitute another architecture, rename the protocol, or “simplify” a test unless this document explicitly permits it.

---

## 0. Scope and non-scope

### This pass must change

Only these high-rate inputs are in scope:

1. `provideriface.StreamEventOutputTextDelta` from the primary provider.
2. Provider reasoning events that currently execute durable writes from the same provider callback. They must leave the callback too, but they remain **durable-only** in this pass.
3. The Desktop path that receives primary assistant live text, batches it to one paint-bound store commit, and reconciles it with durable checkpoints and committed assistant messages.

### This pass must not claim or attempt

Do **not** fix or test delegated subagents in this plan.

Do not modify these subagent/task producers as part of the five checkpoints:

```text
swarmd/internal/run/service_task_launch.go
swarmd/internal/run/service_tools.go
emitTaskStreamDelta
buildTaskStreamPayload
sessionV3StreamChildLineages
```

Do not add a “subagent passed” acceptance criterion. Subagents are currently not a valid end-to-end test source.

The generic stream identity and keyed mailboxes created here must support future child-session streams, but all tests in this plan use either:

- one real primary assistant stream, or
- synthetic independent stream IDs generated directly in unit tests.

Synthetic 100-stream tests prove that the data structure is fan-out-safe. They do **not** prove that subagents work.

### Tools

Provider-managed tools remain on their current tool execution path. This plan must still flush primary assistant progress before a pre-tool assistant message is committed, but it does not migrate tool delta streaming to `live.patch`.

---

## 1. The invariant the finished code must enforce

Inside the callback passed to `runner.CreateResponseStreaming`, an assistant text delta may perform only these operations:

```text
1. Lock a small in-memory callback-state mutex.
2. Advance the stream-local live sequence and UTF-8 byte offset.
3. Append the text to the in-memory response accumulator.
4. Merge the patch into the bounded transient live hub.
5. Merge the text into the bounded durable-progress sink.
6. Unlock.
7. Return.
```

It must not do any of the following:

```text
ApplySessionMutation
applySessionV3PrimaryMutation
appendSessionV3Diagnostic
publishCommittedSessionV3MutationResult
recordRunPhase
recordRunProgress
recordReasoningEvent
recordProviderToolEvent
Pebble reads or writes
outbox reads or writes
Gorilla / transportws writes
wait for a timer
wait for a queue consumer
wait for the browser
sleep
```

“Decoupled” means the provider callback can finish all accepted deltas while the durable writer is deliberately blocked. It does not mean unlimited memory. If the bounded durable sink reaches its hard byte cap, the run fails explicitly; it never blocks the callback and never silently drops canonical progress.

---

## 2. Fixed names, limits, and wire format

Use these names and values exactly in this pass.

### Backend constants

```go
const (
    V3RealtimeKindLivePatch           = "live.patch"
    V3RealtimeCapabilityLivePatchV1   = "live_patch_v1"

    v3LiveMaxPendingBytesPerStream     = 64 << 10  // 64 KiB
    v3LiveMaxPendingBytesPerSubscriber = 256 << 10 // 256 KiB
    v3LiveWriterMaxFramesPerTurn       = 16
    v3LiveWriterMaxBytesPerTurn        = 64 << 10

    sessionV3DurableMaxPendingBytes    = 1 << 20 // 1 MiB per run
    sessionV3DurableMaxSealedEpochs    = 128
    sessionV3DurableMaxControlItems    = 64
)
```

Retain the existing durable checkpoint policy:

```go
sessionV3AssistantDeltaFlushMaxBytes = 2048
sessionV3AssistantDeltaFlushMaxDelay = 250 * time.Millisecond
sessionV3ReasoningDeltaFlushMaxBytes = 4096
sessionV3ReasoningDeltaFlushMaxDelay = 500 * time.Millisecond
```

### Frontend constants

```ts
export const DESKTOP_V3_LIVE_MAX_PENDING_BYTES_PER_STREAM = 64 * 1024
export const DESKTOP_V3_LIVE_MAX_PENDING_BYTES_TOTAL = 256 * 1024
export const DESKTOP_V3_LIVE_HIDDEN_FLUSH_MS = 50
export const DESKTOP_V3_DURABLE_QUEUE_MAX_FRAMES = 512
export const DESKTOP_V3_DURABLE_QUEUE_MAX_BYTES = 2 * 1024 * 1024
```

### Exact live frame

The server sends this on the existing `/v3/realtime/stream` socket:

```json
{
  "protocol": "v3.realtime",
  "protocol_version": 1,
  "kind": "live.patch",
  "session_id": "session-1",
  "live": {
    "session_id": "session-1",
    "run_id": "run-1",
    "stream_id": "assistant:run-1:step:1",
    "stream_kind": "assistant_text",
    "operation": "append",
    "step": 1,
    "step_id": "step-1",
    "live_seq_start": 1,
    "live_seq_end": 1,
    "offset_start": 0,
    "offset_end": 5,
    "text": "hello",
    "recorded_at": 1234567890
  }
}
```

Rules:

1. `live.patch` has no `endpoint_cursor`, `rev`, `prevRev`, outbox row, or durable event.
2. `operation` is only `append` in this pass.
3. `offset_*` count UTF-8 bytes, not JavaScript UTF-16 code units and not rune count.
4. `offset_end - offset_start` must equal the UTF-8 byte length of `text`.
5. `live_seq_*` are scoped to one `stream_id` and start at `1`.
6. The top-level `session_id` must equal `live.session_id`.
7. `stream_kind` is exactly `assistant_text` in this pass.

### Exact primary stream identity

Add this backend helper:

```go
func sessionV3AssistantLiveStreamID(runID string, step int) string {
    if step <= 0 {
        step = 1
    }
    return fmt.Sprintf("assistant:%s:step:%d", strings.TrimSpace(runID), step)
}
```

The stream key used by queues and frontend state is:

```text
account_scope_id + session_id + run_id + stream_id
```

Do not use only `run_id`. A single provider run can commit pre-tool assistant text at step 1 and final assistant text at a later step.

---

## 3. Checkpoint discipline and rollback insurance

Do not start the next checkpoint until every named test in the current checkpoint passes.

Create one git commit after each checkpoint, using these exact commit subjects:

```text
stream-decouple cp1: add dormant live patch transport
stream-decouple cp2: remove persistence from provider callback
stream-decouple cp3: add disabled frontend live bypass
stream-decouple cp4: reconcile live and durable assistant streams
stream-decouple cp5: enable and stress primary live streaming
```

Do not squash these commits until the feature has been exercised in the real application. Each commit is a rollback boundary.

After each commit, create or move the matching local safety tag:

```bash
git tag -f stream-decouple-cp1   # use cp2/cp3/cp4/cp5 at later gates
```

### Rules for the coding agent at every checkpoint

Before editing:

```bash
git status --short
```

The worktree must be clean except for files the user explicitly supplied. Run the checkpoint's smallest existing baseline test before changing code; record whether it passed. Then:

1. Modify only files listed for that checkpoint, except when the plan gives a discovery command for an owning file.
2. Do not add TODOs, skipped tests, `test.only`, `test.skip`, sleep-based correctness tests, or placeholder implementations.
3. Do not continue to the next checkpoint automatically.
4. On a failed acceptance test, fix the current checkpoint or revert to its starting commit. Do not “carry” the failure forward.
5. At the end, print:

```text
checkpoint number
files changed
git diff --stat
exact test commands run
exact pass/fail result
feature gates after the checkpoint
commit hash and safety tag
```

A checkpoint with an unrun named test is failed, even if compilation succeeds.

### Feature-state insurance

| Checkpoint | Provider publishes into hub | Backend accepts capability | Desktop requests capability | Production behavior |
|---|---:|---:|---:|---|
| 1 | No | No | No | Completely unchanged |
| 2 | Yes | No | No | Durable UI behavior unchanged; hub has no production members |
| 3 | Yes | No | No; frontend path test-only | Durable UI behavior unchanged |
| 4 | Yes | No | No; reconciliation test-only | Durable UI behavior unchanged |
| 5 | Yes | Yes | Yes | New live path enabled |

This table is mandatory. Both production gates stay off through Checkpoint 4. Tests enable the per-server/per-transport options explicitly. Do not turn either default on early “to test manually.”

---

# Checkpoint 1 — Add the dormant live contract, keyed hub, and existing-socket writer case

## Goal

Build and prove the transient backend lane without connecting it to the provider. After this checkpoint, synthetic code can publish `live.patch`, but no production code publishes one and Desktop does not request the capability.

## Files to create

```text
swarmd/internal/api/sessions_v3_live_hub.go
swarmd/internal/api/sessions_v3_live_hub_test.go
```

## Files to modify

```text
swarmd/internal/api/server.go
swarmd/internal/api/sessions_v3_realtime_contract.go
swarmd/internal/api/sessions_v3_realtime_contract_test.go
swarmd/internal/api/sessions_v3_realtime_ws.go
swarmd/internal/api/sessions_v3_realtime_ws_test.go
<the source file that owns swarm/packages/swarmd/internal/transport/ws.Conn>
```

Locate the last file instead of guessing its filename:

```bash
TRANSPORT_WS_DIR="$(go list -f '{{.Dir}}' swarm/packages/swarmd/internal/transport/ws)"
rg -n 'type Conn|func \(.*Conn.*WriteText|SetWriteDeadline' "$TRANSPORT_WS_DIR"
```

Modify only the file reported by that command.

## Do not modify in this checkpoint

```text
swarmd/internal/api/sessions_v3_executor.go
web/**
subagent/task files
```

## Step 1.1 — Add the backend production gate and protocol types

In `server.go`, add a default that remains false through Checkpoint 4:

```go
const v3LivePatchDefaultEnabled = false
```

Add to `Server`:

```go
v3LivePatchEnabled bool
```

Initialize it in `NewServer`:

```go
v3LivePatchEnabled: v3LivePatchDefaultEnabled,
```

Tests that need live delivery set `server.v3LivePatchEnabled = true` on their private test server. Do not use a process-wide mutable feature flag.

In `sessions_v3_realtime_contract.go`, add:

```go
const (
    V3RealtimeKindLivePatch         = "live.patch"
    V3RealtimeCapabilityLivePatchV1 = "live_patch_v1"
)

type V3RealtimeLivePatch struct {
    SessionID    string `json:"session_id"`
    RunID        string `json:"run_id"`
    StreamID     string `json:"stream_id"`
    StreamKind   string `json:"stream_kind"`
    Operation    string `json:"operation"`
    Step         int    `json:"step"`
    StepID       string `json:"step_id"`
    LiveSeqStart uint64 `json:"live_seq_start"`
    LiveSeqEnd   uint64 `json:"live_seq_end"`
    OffsetStart  uint64 `json:"offset_start"`
    OffsetEnd    uint64 `json:"offset_end"`
    Text         string `json:"text"`
    RecordedAt   int64  `json:"recorded_at"`
}
```

Add only this field to the existing durable/control `V3RealtimeMessage`:

```go
Capabilities []string `json:"capabilities,omitempty"`
```

Do **not** put `Live` on `V3RealtimeMessage`. Its existing `PrevRev` field is serialized without `omitempty`, so reusing that struct would leak a durable-only `prevRev: 0` field into every transient frame. Use a separate envelope:

```go
type V3RealtimeLiveMessage struct {
    Protocol        string               `json:"protocol"`
    ProtocolVersion int                  `json:"protocol_version"`
    Kind            string               `json:"kind"`
    SessionID       string               `json:"session_id"`
    Live            V3RealtimeLivePatch  `json:"live"`
}

func NewV3RealtimeLiveMessage(patch V3RealtimeLivePatch) V3RealtimeLiveMessage {
    return V3RealtimeLiveMessage{
        Protocol:        V3RealtimeProtocol,
        ProtocolVersion: V3RealtimeProtocolVersion,
        Kind:            V3RealtimeKindLivePatch,
        SessionID:       patch.SessionID,
        Live:            patch,
    }
}

func ValidateV3RealtimeLiveMessage(message V3RealtimeLiveMessage) error
```

Keep `v3RealtimeKindAllowed`, `ValidateV3RealtimeSchemaMessage`, and `ValidateV3RealtimeOutboundServerMessage` for the existing durable/control envelope. Do not add `live.patch` to those switches. A client attempting to send `kind=live.patch` through the existing inbound envelope must continue to fail as an unsupported client message.

`ValidateV3RealtimeLiveMessage` must reject the frame when any of these are true:

```text
protocol or version is wrong
kind != live.patch
message.SessionID is empty
message.SessionID != message.Live.SessionID
run_id, stream_id, step_id, text are empty
stream_kind != assistant_text
operation != append
step <= 0
live_seq_start == 0
live_seq_end < live_seq_start
offset_end < offset_start
offset_end-offset_start != len([]byte(text))
recorded_at <= 0
```

The separate live envelope contains no endpoint cursor, revision, event, projection, or outbox fields by construction.

Add `Capabilities []string` to resume validation but do not require it. Unknown capabilities are ignored. Duplicate capabilities are normalized by the WebSocket handler, not rejected.

The server hello advertises `live_patch_v1` only when `s.v3LivePatchEnabled` is true. When the field is false, omit `Capabilities` entirely.

A resume request does not override the server gate. Acceptance is exactly:

```go
livePatchAccepted = s.v3LivePatchEnabled &&
    containsCapability(message.Capabilities, V3RealtimeCapabilityLivePatchV1)
```

## Step 1.2 — Add the hub to `Server`

In `server.go`, add next to `v3RealtimeOutbox`:

```go
v3LiveHub *v3LiveHub
```

In `NewServer`, initialize it:

```go
v3LiveHub: newV3LiveHub(),
```

Do not lazily create a new hub per connection. There must be one process-level hub owned by `Server`.

## Step 1.3 — Implement a keyed subscriber mailbox, not a delta FIFO

In `sessions_v3_live_hub.go`, use these core shapes:

```go
type v3LiveSessionKey struct {
    AccountScopeID string
    SessionID      string
}

type v3LivePatchKey struct {
    SessionID string
    RunID     string
    StreamID  string
}

type v3LiveSlowConsumer struct {
    Reason string
}

type v3LivePendingPatch struct {
    Patch V3RealtimeLivePatch
    Text  bytes.Buffer
    Bytes int
}

type v3LiveSubscriber struct {
    id string

    mu sync.Mutex

    pendingByKey map[v3LivePatchKey]*v3LivePendingPatch
    readyKeys    []v3LivePatchKey
    queuedKeys   map[v3LivePatchKey]struct{}
    sessionKeys  map[v3LiveSessionKey]struct{} // protected by hub.mu, not subscriber.mu
    pendingBytes int

    notify chan struct{} // capacity 1
    slow   chan v3LiveSlowConsumer // capacity 1
    closed bool
}

type v3LiveHub struct {
    mu sync.RWMutex

    nextSub uint64
    subs    map[string]*v3LiveSubscriber
    bySession map[v3LiveSessionKey]map[string]*v3LiveSubscriber
}
```

Required methods:

```go
func newV3LiveHub() *v3LiveHub
func (h *v3LiveHub) subscribe() *v3LiveSubscriber
func (h *v3LiveHub) unsubscribe(sub *v3LiveSubscriber)
func (h *v3LiveHub) replaceSessions(sub *v3LiveSubscriber, accountScopeID string, sessionIDs []string)
func (h *v3LiveHub) publish(accountScopeID string, patch V3RealtimeLivePatch)
func (h *v3LiveHub) markSlow(sub *v3LiveSubscriber, reason string)
func (s *v3LiveSubscriber) enqueue(patch V3RealtimeLivePatch) (overflow bool)
func (s *v3LiveSubscriber) drain(maxFrames, maxBytes int) []V3RealtimeLivePatch
```

### `publish` algorithm

The implementation must do this, in this order:

1. Validate/normalize `accountScopeID` and `patch.SessionID`.
2. Under `h.mu.RLock`, copy only the subscribers indexed under that account/session into a local slice; then release `h.mu` before locking any subscriber.
3. For each copied subscriber, call `sub.enqueue(patch)`. That method returns `overflow=true` instead of modifying hub indexes itself.
4. After `sub.enqueue` releases `sub.mu`, call `h.markSlow(sub)` for overflowed subscribers. Never hold `h.mu` and `sub.mu` at the same time.
5. Never call `conn.WriteText`, Pebble, session lookup, or authorization storage from `publish`.
6. Return after all indexed subscribers are offered the patch.

### Subscriber enqueue algorithm

For one stream key:

- If no patch is pending for the key, allocate one `v3LivePendingPatch`, copy the metadata into `Patch`, clear `Patch.Text`, call `pending.Text.WriteString(patch.Text)`, set `Bytes`, and put the key into `readyKeys` exactly once.
- If a patch is already pending, merge only when:

```text
same session_id
same run_id
same stream_id
same operation=append
new.live_seq_start == pending.Patch.live_seq_end + 1
new.offset_start == pending.Patch.offset_end
```

The merge must be O(1) with respect to accumulated text:

```go
_, _ = pending.Text.WriteString(patch.Text)
pending.Bytes += len([]byte(patch.Text))
pending.Patch.LiveSeqEnd = patch.LiveSeqEnd
pending.Patch.OffsetEnd = patch.OffsetEnd
pending.Patch.RecordedAt = patch.RecordedAt
```

Do **not** use `pending.Patch.Text += patch.Text` and do not retain one `[]string` entry per provider delta. `bytes.Buffer.WriteString` gives amortized linear growth with one aggregate per active stream. Do not append another ready-queue item for the same key.

Update `pendingBytes` by the incoming UTF-8 byte count, not by the merged frame's full size.

If one stream exceeds `64 KiB` pending text or one subscriber exceeds `256 KiB` total pending text:

1. `sub.enqueue` leaves the existing mailbox unchanged and returns `overflow=true` after releasing `sub.mu`.
2. `h.markSlow` marks only that subscriber closed, removes every key in `sub.sessionKeys` from `bySession`, and clears `sub.sessionKeys`.
3. Send one nonblocking value to `sub.slow`.
4. Do not block the publisher.
5. Do not cancel the provider or affect another subscriber.

`sub.sessionKeys` is membership metadata protected only by `h.mu`. `replaceSessions` must update `sub.sessionKeys` and `h.bySession` atomically under `h.mu`; it must not take `sub.mu` and must not leave stale memberships after unsubscribe or account/session replacement. `markSlow`/`unsubscribe` first close the mailbox under `sub.mu`, release it, then remove hub membership under `h.mu`. `replaceSessions` must first confirm `h.subs[sub.id] == sub`, so a concurrently removed subscriber cannot be re-added.

Use UTF-8 byte counts (`len([]byte(text))`) for all caps.

### `drain` algorithm

`drain(16, 64<<10)` must:

1. Under `sub.mu`, pop keys from `readyKeys` in order into a local slice of `v3LivePendingPatch` values.
2. Select at most one aggregate per key in that pass.
3. Stop at 16 aggregates or 64 KiB of `pending.Bytes`, whichever comes first.
4. Remove selected entries from `pendingByKey` and `queuedKeys` and subtract their bytes from `pendingBytes`.
5. If keys remain, signal `notify` again with a nonblocking send.
6. Release `sub.mu`.
7. Only after releasing the lock, set each output patch's `Text = pending.Text.String()` and build the returned frames. Do not copy or reuse a `bytes.Buffer` after it has been written; move its pointer out of the map and delete the map entry first.

Do not materialize accumulated text while holding `sub.mu`; provider publication for that browser must not wait on a 64 KiB string view/copy. This drain prevents one hot stream from monopolizing the writer turn and gives future independent child streams fair turns.

## Step 1.4 — Wire the live subscriber into the existing WebSocket loop

In `handleV3RealtimeStream`:

1. Subscribe once after the durable outbox subscription:

```go
liveSub := s.v3LiveHub.subscribe()
if liveSub == nil { ... }
defer s.v3LiveHub.unsubscribe(liveSub)
```

2. Add connection state:

```go
livePatchAccepted := false
```

3. On every `resume`, set it exactly from the server gate and requested capability:

```go
livePatchAccepted = s.v3LivePatchEnabled &&
    containsCapability(message.Capabilities, V3RealtimeCapabilityLivePatchV1)
```

A later resume without the capability turns it back off. A request cannot turn it on while the server default is disabled.

4. Add one helper:

```go
func (s *Server) syncV3LiveSubscriptionSessions(
    liveSub *v3LiveSubscriber,
    principal identity.Principal,
    subs map[string]v3RealtimeSubscription,
    livePatchAccepted bool,
)
```

If the capability is false, replace membership with an empty set. If true, membership is exactly the session IDs in `subs`.

5. Call that helper only after durable replay/catch-up for the subscription operation completes:

```text
after resume replay.complete
after subscribe.session replay.complete
after unsubscribe.session
after any durable catch-up that changes auto-subscribed workset sessions
```

Do not register a session in the live hub before its `replay.complete` has been sent.

6. Add this case to the same main `select` that currently writes durable frames:

```go
case <-liveSub.notify:
    patches := liveSub.drain(
        v3LiveWriterMaxFramesPerTurn,
        v3LiveWriterMaxBytesPerTurn,
    )
    for _, patch := range patches {
        if err := s.sendV3RealtimeLivePatch(conn, patch); err != nil {
            return
        }
    }
```

Add:

```go
func (s *Server) sendV3RealtimeLivePatch(
    conn *transportws.Conn,
    patch V3RealtimeLivePatch,
) error {
    message := NewV3RealtimeLiveMessage(patch)
    if err := ValidateV3RealtimeLiveMessage(message); err != nil {
        return err
    }
    raw, err := json.Marshal(message)
    if err != nil {
        return err
    }
    return s.writeV3RealtimePayload(conn, raw)
}
```

Refactor the existing `sendV3RealtimeMessage` so that, after validation and marshal, it also calls `writeV3RealtimePayload`. That shared helper is the only function that calls `conn.WriteText`.

Do not create a live writer goroutine. Both durable/control and live envelopes are written by the same WebSocket select loop through the same `writeV3RealtimePayload` helper.

Add:

```go
var v3RealtimeWriteTimeout = 5 * time.Second
```

Inspect `transportws.Conn` using the command above. If `WriteText` does not already set a hard write deadline, add this method to the wrapper that owns `Conn`:

```go
func (c *Conn) SetWriteDeadline(deadline time.Time) error
```

It must forward to the underlying WebSocket connection. `writeV3RealtimePayload` must call:

```go
if err := conn.SetWriteDeadline(time.Now().Add(v3RealtimeWriteTimeout)); err != nil {
    return err
}
return conn.WriteText(raw)
```

If the wrapper already enforces an equal or stricter deadline inside `WriteText`, do not add a second deadline; add a test proving the existing one instead. A browser that stops reading must eventually release the one connection loop with an error. Subscriber overflow alone is not sufficient because the loop may already be blocked inside a socket write.

7. On `liveSub.slow`, send the existing `slow_consumer.reconnect_required` frame when the socket is writable and close only that connection. If a write is already stalled, the hard deadline closes the path.

## Step 1.5 — Add a single-writer observer for a hard test

In `sessions_v3_realtime_ws.go`, add a package-private test hook:

```go
var v3RealtimeWriteObserver func(activeDelta int)
```

In the new shared `writeV3RealtimePayload`, immediately around `conn.WriteText(raw)`:

```go
if observer := v3RealtimeWriteObserver; observer != nil {
    observer(+1)
    defer observer(-1)
}
```

The hook must be nil in normal operation and must not change the write path.

## Required tests

### `TestV3RealtimeLivePatchContractRoundTrip`

File: `sessions_v3_realtime_contract_test.go`

Construct the exact JSON example from this plan as `V3RealtimeLiveMessage`. Marshal, unmarshal, validate it with `ValidateV3RealtimeLiveMessage`, and assert every field. Then unmarshal the JSON into `map[string]any` and assert these keys are completely absent:

```text
endpoint_cursor
rev
prevRev
event
projection
realtime_outbox
```

### `TestV3RealtimeLivePatchContractRejectsInvalidOffsets`

Use text `"é"`, which is 2 UTF-8 bytes. Assert `offset_start=0, offset_end=1` fails and `offset_end=2` passes.

### `TestV3RealtimeLiveHubCoalescesInterleavedStreamsByKey`

File: `sessions_v3_live_hub_test.go`

Register one subscriber for `acct-1/session-1`. Publish this pattern without draining:

```text
A1 B1 C1 ... stream-100/patch-1
A2 B2 C2 ... stream-100/patch-2
```

Use one-byte text per patch and contiguous offsets/sequences per stream.

Assert before draining:

```text
len(pendingByKey) == 100
len(readyKeys) == 100
not 200
pendingBytes == 200
```

Drain and assert every stream contains exactly its two bytes and has `live_seq_start=1`, `live_seq_end=2`.

Also add a source guard for `sessions_v3_live_hub.go` that rejects text accumulation patterns matching `.Text +=`, `pending.Patch.Text = pending.Patch.Text +`, or `[]string` per-delta chunk queues. Require one `bytes.Buffer` per pending stream and `WriteString`.

This is a synthetic mailbox test, not a subagent test.

### `TestV3RealtimeLiveHubOverflowIsSubscriberLocal`

Register fast and slow subscribers for the same session. Never drain the slow subscriber; continuously drain the fast one. Publish until slow exceeds 256 KiB.

Assert:

```text
slow receives exactly one slow signal
slow is removed from the session index
fast continues receiving patches
publish returns without waiting
```

### `TestV3RealtimeLegacyResumeReceivesNoLivePatch`

Connect through the real V3 WebSocket. Send a resume without `capabilities`. Complete a session subscription. Publish a synthetic live patch through `server.v3LiveHub`.

Assert no `live.patch` frame arrives within the existing bounded test timeout.

### `TestV3RealtimeCapabilityResumeReceivesLivePatchAfterReplayComplete`

Send resume with:

```json
"capabilities": ["live_patch_v1"]
```

Assert the order is:

```text
replay.started
durable replay events
replay.complete
live.patch
```

### `TestV3RealtimeLiveAndDurableUseOneSocketWriter`

Set `v3RealtimeWriteObserver`. Concurrently trigger a durable outbox wake and a synthetic live publish. Track current active writes, completed writes, and maximum active writes atomically.

Hard assertions:

```go
if completedWrites.Load() < 2 { t.Fatalf(...) }
if maxActive.Load() != 1 { t.Fatalf(...) }
```

Restore the observer with `t.Cleanup`.

### `TestV3RealtimeLivePatchServerGateDefaultsOff`

Create a normal server without changing `v3LivePatchEnabled`. Send a resume requesting `live_patch_v1`, subscribe, and publish a synthetic patch. Assert the hello does not advertise the capability and no live frame is delivered. This test protects the rollback state through Checkpoint 4.

### `TestV3RealtimeWriteDeadlineReleasesStalledSocket`

Temporarily set `v3RealtimeWriteTimeout` to a small test value such as 25 ms. Use a real WebSocket client that completes the handshake and then stops reading. Repeatedly write bounded large test frames until the server-side write blocks. Assert the write path returns an error and the handler exits within the test's one-second outer timeout. Restore the timeout with `t.Cleanup`.

If the transport wrapper already owns the deadline internally, put the focused test in that wrapper package and keep a smaller API test that asserts the shared V3 writer uses the wrapper.

## Checkpoint 1 pass/fail gate

Run:

```bash
go test ./swarmd/internal/api \
  -run 'TestV3Realtime' \
  -count=1

go test -race ./swarmd/internal/api \
  -run 'TestV3RealtimeLiveHub|TestV3RealtimeLiveAndDurableUseOneSocketWriter|TestV3RealtimeWriteDeadline' \
  -count=1
```

**Pass only when all are true:**

```text
[ ] Existing V3 realtime tests still pass.
[ ] No production code publishes to v3LiveHub.
[ ] Legacy resume gets no live frame.
[ ] 100 interleaved streams occupy 100 pending keys, not 200 items.
[ ] Slow subscriber isolation is proven.
[ ] Maximum concurrent socket writes is exactly 1 and at least two writes were observed.
[ ] A stalled socket exits through a hard write deadline.
[ ] The backend production gate defaults off.
```

Commit with:

```text
stream-decouple cp1: add dormant live patch transport
```

---

# Checkpoint 2 — Remove all persistence from the primary provider callback

## Goal

Make primary assistant and reasoning callback handling independent of Pebble. After this checkpoint, the backend publishes live assistant patches and queues durable progress, but the production Desktop client still does not request `live_patch_v1`.

## Files to create

```text
swarmd/internal/api/sessions_v3_durable_progress.go
swarmd/internal/api/sessions_v3_provider_stream.go
```

## Files to modify

```text
swarmd/internal/api/sessions_v3_executor.go
swarmd/internal/api/sessions_v3_primary_test.go
```

Modify `sessions_v3_diagnostics.go` only if needed for a test observer. Do not change its durable semantics outside the hot callback.

## Do not modify in this checkpoint

```text
web/**
subagent/task files
tool live protocol
```

## Step 2.1 — Replace the old synchronous coalescers

Delete these old types after the new sink is used:

```text
sessionV3AssistantDeltaCoalescer
sessionV3ReasoningDeltaCoalescer
newSessionV3AssistantDeltaCoalescer
newSessionV3ReasoningDeltaCoalescer
```

Do not leave both implementations in the tree.

Create a single per-run sink in `sessions_v3_durable_progress.go`.

Use these public methods exactly:

```go
type sessionV3DurableProgressSink struct { ... }

func newSessionV3DurableProgressSink(
    exec *sessionV3Executor,
    job sessionV3ExecutorJob,
    cancelProvider context.CancelFunc,
) *sessionV3DurableProgressSink

func (s *sessionV3DurableProgressSink) TryRecordPhase(
    phase RunPhase,
    eventType string,
) error

func (s *sessionV3DurableProgressSink) TryAppendAssistant(
    progress sessionV3AssistantProgress,
) error

func (s *sessionV3DurableProgressSink) TryStartReasoning(
    step int,
    reasoningKey string,
) error

func (s *sessionV3DurableProgressSink) TryReplaceReasoning(
    step int,
    reasoningKey string,
    snapshot string,
) error

func (s *sessionV3DurableProgressSink) TryCompleteReasoning(
    step int,
    reasoningKey string,
    summary string,
) error

func (s *sessionV3DurableProgressSink) FlushBarrier(
    ctx context.Context,
) error

func (s *sessionV3DurableProgressSink) CloseAndFlush(
    ctx context.Context,
) error
```

Do not make sink tests patch `applySessionV3PrimaryMutation` indirectly. Add this exact writer seam:

```go
type sessionV3DurableProgressWriter interface {
    RecordRunPhase(
        job sessionV3ExecutorJob,
        phase RunPhase,
        eventType string,
    ) (sessionruntime.SessionMutationResult, error)

    RecordRunProgress(
        job sessionV3ExecutorJob,
        progress sessionV3AssistantProgress,
        deltaIndex int,
    ) (sessionruntime.SessionMutationResult, error)

    RecordReasoningEvent(
        job sessionV3ExecutorJob,
        eventType string,
        step int,
        eventIndex int,
        reasoningKey string,
        delta string,
        summary string,
    ) (sessionruntime.SessionMutationResult, error)
}

type sessionV3ExecutorDurableProgressWriter struct {
    exec *sessionV3Executor
}
```

The production writer methods are one-line adapters to the existing executor methods. Add:

```go
func newSessionV3DurableProgressSinkWithWriter(
    exec *sessionV3Executor,
    job sessionV3ExecutorJob,
    cancelProvider context.CancelFunc,
    writer sessionV3DurableProgressWriter,
) *sessionV3DurableProgressSink
```

`newSessionV3DurableProgressSink` calls the `WithWriter` constructor with `sessionV3ExecutorDurableProgressWriter{exec: exec}`. Unit tests use a deterministic blocking writer implementation with `entered` and `release` channels. Do not use sleeps to simulate Pebble.

Define:

```go
type sessionV3AssistantProgress struct {
    StreamID     string
    Step         int
    StepID       string
    LiveSeqStart uint64
    LiveSeqEnd   uint64
    OffsetStart  uint64
    OffsetEnd    uint64
    Text         string
    RecordedAt   int64
}
```

### Required sink ownership model

The callback methods (`Try*`) may only:

```text
lock sink.mu
check closed/error/caps
merge into the current in-memory epoch
signal a capacity-1 notify channel
unlock
return
```

They must never wait for the worker.

The worker goroutine owns calls to:

```text
recordRunPhase
recordRunProgress
recordReasoningEvent
```

### Required keyed epoch model

Do not use `chan durableProgressInput` with one channel item per provider delta.

The sink must contain these fields or exact equivalents with the same ownership:

```go
currentAssistantByStream map[string]*sessionV3AssistantProgressAggregate
currentReasoningByKey    map[string]*sessionV3ReasoningProgressAggregate
acceptedAssistantEnd     map[string]sessionV3AssistantAcceptedEnd
sealedEpochs             []sessionV3DurableProgressEpoch
waiters                  map[uint64][]chan error

pendingBytes  int
inFlightBytes int
controlItems  int

nextOrder               uint64
nextEpochID             uint64
committedEpochID        uint64
nextAssistantDeltaIndex int
reasoningDeltaIndexByKey map[string]int

notify    chan struct{} // capacity 1
workerDone chan struct{}
closed     bool
firstErr   error
cancelOnce sync.Once
```

Each assistant aggregate records `FirstOrder`, `Text bytes.Buffer`, and `Bytes int`. `TryAppendAssistant` calls `Text.WriteString`; it must not rebuild the full accumulated string and must not retain one slice entry per callback. Sealing moves the aggregate pointer into an epoch without copying the buffer. After the worker moves the epoch to a local variable and releases `sink.mu`, it obtains `Text.String()` and builds the immutable `sessionV3AssistantProgress` passed to the writer. Never copy a non-empty `bytes.Buffer` value. When an epoch is sealed, sort its assistant/reasoning aggregates by `FirstOrder`; persist epochs strictly by `EpochID`. Continuous assistant deltas for the same `stream_id` merge into one current aggregate. Interleaved synthetic stream IDs merge independently by key.

`acceptedAssistantEnd` is updated on every accepted callback and must validate:

```text
new.live_seq_start == prior.live_seq_end + 1
new.offset_start == prior.offset_end
```

That validation still applies when the prior aggregate has already moved to a sealed or in-flight epoch. A discontinuity is a fatal sink error; do not merge or silently skip it.

A control boundary must seal the current epoch before adding a control-only epoch. Control boundaries are:

```text
provider first-event phase
output-streaming phase
reasoning started
reasoning completed
FlushBarrier
CloseAndFlush
```

A timer/size flush seals current aggregates into `sealedEpochs`; it does not create one epoch per delta.

`TryAppendAssistant` must **not** seal an epoch directly merely because the incoming delta contains a newline or crosses 2,048 bytes. It sets `FlushRequested=true` on the aggregate and signals `notify`. The worker, after acquiring `sink.mu`, performs the seal. This prevents newline-heavy output from creating one sealed object per callback while persistence is blocked.

The worker protocol is exact:

1. Under `sink.mu`, seal due/flush-requested aggregates and move the oldest sealed epoch to a local variable.
2. Move that epoch's bytes from `pendingBytes` to `inFlightBytes`; the sum must not change.
3. Release `sink.mu`.
4. Persist every item in the epoch through `sessionV3DurableProgressWriter` in its stored order. Increment the one run-level `nextAssistantDeltaIndex` immediately before each assistant `RecordRunProgress` call. Never reset it at a provider tool-loop step.
5. Reacquire `sink.mu`.
6. On success, subtract the epoch bytes from `inFlightBytes`, advance `committedEpochID`, and complete all waiters whose target epoch is now committed.
7. On failure, retain the first error, cancel once, and complete every waiter with that error.
8. Never hold `sink.mu` during `RecordRunPhase`, `RecordRunProgress`, `RecordReasoningEvent`, Pebble, JSON mutation publication, or any other durable call.

`FlushBarrier` must, under the lock, seal all current aggregates, record the resulting target `EpochID`, register a one-shot waiter channel for that target, signal the worker, unlock, and then select on the waiter or `ctx.Done()`. When there is no pending/in-flight work and no error, return immediately instead of registering a waiter for epoch zero. `CloseAndFlush` does the same after setting `closed=true`; repeated calls return the same result. `Try*` racing with close must return a closed/error result and must never send on a closed channel. Decrement `controlItems` only after its control epoch commits or is discarded after terminal failure.

### Byte accounting

Count all of these until persistence succeeds or the run fails:

```text
current aggregate text
sealed epoch text
worker-owned in-flight text
reasoning snapshots
```

Do not subtract bytes merely because the worker removed an epoch from the queue.

`Try*` checks the cap before mutating state and returns `ErrSessionV3DurableProgressBacklog` immediately when:

```text
pendingBytes + inFlightBytes + additionalBytes > 1 MiB
sealedEpochs would exceed 128
controlItems would exceed 64
```

For a reasoning snapshot replacement, `additionalBytes` is `max(0, newSnapshotBytes-oldSnapshotBytes)` and accounting is adjusted by the exact byte difference after acceptance.

When any `Try*` call detects a hard cap, it must store `ErrSessionV3DurableProgressBacklog`, call `cancelProvider()` once, and return immediately. When the worker encounters a persistence error:

1. Store the first error.
2. Call `cancelProvider()` once.
3. Make every later `Try*` return that error immediately.
4. Wake `FlushBarrier`/`CloseAndFlush` waiters.

The worker must own a real timer. Record `firstPendingAt` on each open assistant/reasoning aggregate. The worker selects on `notify`, its timer, flush requests, and close. On a timer tick it seals aggregates whose configured delay has elapsed even when no later provider delta arrives. A blocked persistence call may delay durable catch-up, but it must never prevent callback-side merging or live publication.

## Step 2.2 — Give durable assistant deltas stream identity and offsets

Change `recordRunProgress` from positional `(deltaIndex, delta)` to:

```go
func (e *sessionV3Executor) recordRunProgress(
    job sessionV3ExecutorJob,
    progress sessionV3AssistantProgress,
    deltaIndex int,
) (sessionruntime.SessionMutationResult, error)
```

The durable event payload must retain the legacy fields and add the new fields:

```json
{
  "run_id": "run-1",
  "stream_id": "assistant:run-1:step:1",
  "operation": "append",
  "step": 1,
  "step_id": "step-1",
  "delta_index": 1,
  "offset_start": 0,
  "offset_end": 11,
  "delta": "hello world",
  "recorded_at": 1234567890
}
```

`delta_index` remains for old clients. The new frontend uses `stream_id` and byte offsets.

The sink assigns one durable `delta_index` per persisted aggregate, not per provider callback. It is one monotonically increasing counter for the entire run and does not reset when `step` changes. Update the payload hash and idempotency/client-request material to include `delta_index`, `stream_id`, `offset_start`, `offset_end`, and `delta`; do not hash only the text.

## Step 2.3 — Create a callback-safe stream tracker

In `sessions_v3_provider_stream.go`, add:

```go
type sessionV3AssistantLiveTracker struct {
    SessionID string
    RunID     string
    Step      int
    StepID    string
    StreamID  string
    NextSeq   uint64
    Offset    uint64
}

func newSessionV3AssistantLiveTracker(
    sessionID, runID string,
    step int,
) *sessionV3AssistantLiveTracker

func (t *sessionV3AssistantLiveTracker) Append(
    text string,
    recordedAt int64,
) (V3RealtimeLivePatch, sessionV3AssistantProgress)
```

For `"é"`, `OffsetEnd` must increase by 2.

Add one callback-state object:

```go
type sessionV3ProviderStreamState struct {
    mu sync.Mutex

    exec        *sessionV3Executor
    job         sessionV3ExecutorJob
    sink        *sessionV3DurableProgressSink
    tracker     *sessionV3AssistantLiveTracker
    streamed    strings.Builder

    providerFirstEventRecorded bool
    outputStreamingRecorded    bool
    streamEventCount           int
    progressErr                error

    activeReasoningKey string
    reasoningByKey     map[string]string
}

func (s *sessionV3ProviderStreamState) Handle(
    event provideriface.StreamEvent,
)

func (s *sessionV3ProviderStreamState) FinishStep() error

func (s *sessionV3ProviderStreamState) EnsureResponseText(
    text string,
) error

func (s *sessionV3ProviderStreamState) StreamedText() string
func (s *sessionV3ProviderStreamState) OffsetEnd() uint64
```

Create a new `sessionV3ProviderStreamState` and tracker for each provider tool-loop step. Reuse the same durable sink for the entire run. The provider interface contract for this pass is that all callback invocations finish before `CreateResponseStreaming` returns; concurrent invocations before return are serialized by `s.mu`.

The callback passed to the provider becomes only:

```go
response, err := runner.CreateResponseStreaming(streamCtx, req, streamState.Handle)
```

### Exact common and assistant path inside `Handle`

`Handle` must lock once and use `defer s.mu.Unlock()`. Before the type switch, do this for **every nonempty provider event kind**, including a reasoning event that arrives before assistant text:

```go
if s.progressErr != nil {
    return
}

s.streamEventCount++
if !s.providerFirstEventRecorded {
    s.progressErr = s.sink.TryRecordPhase(
        RunPhaseProviderFirstEvent,
        "session.provider.first_event",
    )
    s.providerFirstEventRecorded = s.progressErr == nil
}
if s.progressErr != nil {
    return
}
```

Then the assistant case is exactly:

```go
case provideriface.StreamEventOutputTextDelta:
    if event.Delta == "" {
        return
    }

    if !s.outputStreamingRecorded {
        s.progressErr = s.sink.TryRecordPhase(
            RunPhaseOutputStreaming,
            "session.output.streaming",
        )
        s.outputStreamingRecorded = s.progressErr == nil
    }
    if s.progressErr != nil {
        return
    }

    now := time.Now().UnixMilli()
    s.streamed.WriteString(event.Delta)
    patch, durable := s.tracker.Append(event.Delta, now)

    s.exec.server.v3LiveHub.publish(
        s.job.Principal.AccountScopeID,
        patch,
    )
    s.progressErr = s.sink.TryAppendAssistant(durable)
```

Do not use `strings.TrimSpace(event.Delta)` to decide whether to stream. A delta containing only spaces or a newline is still canonical output and must advance sequence and byte offsets. Only the empty string is ignored.

Both hub publication and durable enqueue are bounded in-memory operations. Keep them under the callback mutex to preserve event order even if a provider invokes callbacks concurrently before `CreateResponseStreaming` returns.

### Exact reasoning path inside `Handle`

Keep current reasoning snapshot semantics, but call only sink `Try*` methods. Do not call `recordReasoningEvent` in the callback.

When the reasoning key changes:

```text
TryCompleteReasoning(previous key)
TryStartReasoning(new key)
```

For each changed reasoning snapshot:

```text
TryReplaceReasoning(step, key, latestMergedSnapshot)
```

Reasoning is not published to `v3LiveHub` in this pass.

After `CreateResponseStreaming` returns, call `streamState.FinishStep()` before the barrier. `FinishStep` must queue `TryCompleteReasoning` for the active key and return the first callback/sink error.

Add this helper in `sessions_v3_provider_stream.go`:

```go
func sessionV3ProviderStepAssistantText(
    response provideriface.Response,
    streamed string,
) string
```

Its rules are exact:

1. If `streamed != ""`, return it unchanged. Streamed bytes are canonical for that step, even when `response.Text` is trimmed or formatted differently.
2. Otherwise, if `strings.TrimSpace(response.Text) != ""`, return `response.Text` unchanged.
3. Otherwise, concatenate final-phase `response.AssistantMessages` with `"\n\n"` between selected non-whitespace messages, preserving each selected `message.Text` unchanged.
4. Return `""` only when all sources are empty/whitespace.

After `FinishStep`, compute `stepText := sessionV3ProviderStepAssistantText(response, streamState.StreamedText())`. If `streamState.OffsetEnd() == 0` and `strings.TrimSpace(stepText) != ""`, call `EnsureResponseText(stepText)`. `EnsureResponseText` must queue provider-first/output-streaming phases if not already queued, then create one synthetic live range and one durable assistant append. It must not call a durable mutation directly.

Never call `strings.TrimSpace` on text that will be stored, published, hashed as canonical content, or passed to `recordPreToolAssistantSegment`. Use trimming only for the boolean “is this response empty?” check.

## Step 2.4 — Remove durable diagnostics from the hot callback

Delete this per-event call from the provider callback:

```go
e.recordSessionV3Diagnostic(
    job,
    "session.diagnostic.provider.stream",
    ...,
)
```

Do not replace it with another durable diagnostic.

A non-durable atomic counter or histogram is allowed. A log line per delta is not allowed.

Diagnostics before the provider request and after the provider response may remain because they are outside the high-rate callback.

## Step 2.5 — Integrate one sink per run and explicit barriers

In `providerAssistantResponse`:

1. Keep `session.provider.request_started` as the existing synchronous control-plane mutation before the provider call.
2. Create a child context:

```go
streamCtx, cancelStream := context.WithCancel(ctx)
defer cancelStream()
```

3. Create one sink for the whole provider tool loop.
4. Pass the sink into `runProviderToolLoop`.
5. Always call `CloseAndFlush` after the loop returns.
6. Error precedence is deterministic:

```text
callback/sink first error
then provider return error
then CloseAndFlush error
```

Always invoke `CloseAndFlush` even when the provider loop returns an error so worker goroutines and waiter channels are released. Use:

```go
flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer flushCancel()
closeErr := sink.CloseAndFlush(flushCtx)
```

Do not pass the cancelled provider child context to final cleanup.

Use a result struct instead of adding more positional return values:

```go
type sessionV3ProviderLoopResult struct {
    Response          provideriface.Response
    FinalContent      string
    DurableFlushCount int
    FinalStreamID     string
    FinalStep         int
    FinalOffsetEnd    uint64
}
```

Change:

```go
func (e *sessionV3Executor) runProviderToolLoop(...) (sessionV3ProviderLoopResult, error)
```

### Barriers and exact step integration inside `runProviderToolLoop`

For each step, use this order outside the provider callback:

```text
response, providerErr := runner.CreateResponseStreaming(...)
finishErr := streamState.FinishStep()
stepText := sessionV3ProviderStepAssistantText(response, streamState.StreamedText())
ensureErr := nil
if providerErr == nil && finishErr == nil && no assistant range exists && stepText is non-whitespace:
    ensureErr = streamState.EnsureResponseText(stepText)
barrierErr := sink.FlushBarrier(ctx)
stepErr := firstNonNil(finishErr, ensureErr, sink.Err(), providerErr, barrierErr)
```

Add `func (s *sessionV3DurableProgressSink) Err() error`, which only locks, returns `firstErr`, and unlocks. Stop the run when `stepErr != nil`. This ordering gives callback/sink failures priority over provider-return errors while still flushing already accepted progress. The barrier happens before `recordProviderUsage`, pre-tool message persistence, and tool execution, so those synchronous control-plane mutations cannot race ahead of accepted stream progress.

Then:

```text
if response contains tool calls:
    persist the pre-tool assistant message with Content=stepText unchanged
    persist/invoke tools only after the barrier succeeded
else:
    return FinalContent=stepText and the exact final tracker identity/offset
```

Required barriers are therefore:

```text
1. after every CreateResponseStreaming step
2. before recordProviderUsage for that step
3. before recordPreToolAssistantSegment
4. before invoking the first tool for that step
5. before returning the final provider response
```

The first barrier already satisfies 2–5 when the order above is followed; do not add redundant extra persistence waits.

Delete the old `flushCount == 0 -> recordRunProgress(...)` fallback. A no-delta response goes through `EnsureResponseText`, live publication, sink append, and the same barrier.

In `providerAssistantResponse`, set:

```go
content := loopResult.FinalContent
if strings.TrimSpace(content) == "" {
    return sessionV3AssistantResponse{}, errors.New("provider returned empty assistant response")
}
```

Store `content` unchanged. Do not prefer or trim `response.Text` after the loop has selected canonical bytes.

Change `recordPreToolAssistantSegment` from:

```go
content := strings.TrimSpace(response.Content)
```

to:

```go
content := response.Content
if strings.TrimSpace(content) == "" {
    return sessionruntime.SessionMutationResult{}, nil
}
```

## Step 2.6 — Put stream metadata on committed assistant messages

Add to `sessionV3AssistantResponse`:

```go
StreamID       string
StreamStep     int
StreamOffsetEnd uint64
```

In `metadata(runID)`, add when present:

```go
metadata["stream_id"] = r.StreamID
metadata["stream_step"] = r.StreamStep
metadata["stream_offset_end"] = r.StreamOffsetEnd
```

For a pre-tool segment, set these fields from that exact provider step before calling `recordPreToolAssistantSegment`.

For the final assistant, set them from `sessionV3ProviderLoopResult.Final*` before `completeRun`. Assert before completion that `StreamOffsetEnd == uint64(len([]byte(Content)))` for the selected final stream.

Do not use the final stream ID on an earlier pre-tool message.

## Step 2.7 — Add an enforceable source guard

Because `Handle` is now an isolated function, add a test that reads `sessions_v3_provider_stream.go`, extracts the body of:

```go
func (s *sessionV3ProviderStreamState) Handle
```

and rejects these identifiers/calls:

```text
ApplySessionMutation
applySessionV3PrimaryMutation
appendSessionV3Diagnostic
recordSessionV3Diagnostic
recordRunPhase
recordRunProgress
recordReasoningEvent
recordProviderToolEvent
WriteText
Sleep
.Flush(
.CloseAndFlush(
```

Do not extract the function with a single regular expression. Implement a small brace-depth scanner starting at the `func (s *sessionV3ProviderStreamState) Handle` declaration so nested switches/closures cannot truncate the inspected body.

This is not the only test, but it prevents a future “small” change from putting writes back into the hot callback.

## Required tests

Add these to `sessions_v3_primary_test.go` unless a small sink-only unit test belongs in a new `sessions_v3_durable_progress_test.go`.

### `TestV3AssistantCallbackPublishesAllDeltasWhileDurableWriterIsBlocked`

Arrange:

1. Configure the fake provider handler to emit 10,000 one-byte output deltas in a tight loop.
2. Construct the sink through `newSessionV3DurableProgressSinkWithWriter` with a blocking writer whose first method closes `writerEntered` and waits on `releaseWriter`.
3. Subscribe a synthetic live subscriber directly to `server.v3LiveHub` for the session.
4. Start the run.

Hard proof:

```text
The provider handler closes allDeltasEmitted after calling onEvent 10,000 times.
The test waits for allDeltasEmitted before releasing the durable writer.
If allDeltasEmitted cannot close, the callback is still waiting on durability and the test fails.
```

Before releasing persistence, drain the live subscriber and assert:

```text
combined text equals 10,000 expected bytes
first live seq is 1
last live seq is 10,000
first offset is 0
last offset is 10,000
```

Then release persistence and assert the run completes with one canonical assistant message containing exactly the same text.

Do not use a millisecond latency threshold as the primary proof. Channel ordering is deterministic.

### `TestV3DurableProgressSinkCoalescesTenThousandDeltasByStream`

Use `newSessionV3DurableProgressSinkWithWriter` and a blocking writer. Call `TryAppendAssistant` 10,000 times for one stream, including a newline every tenth delta.

Assert under the sink mutex/test snapshot before releasing the writer:

```text
current assistant keys + sealed assistant aggregates + in-flight assistant aggregates is bounded by a small constant, not 10,000
pendingBytes + inFlightBytes equals 10,000
no TryAppend call waited on the writer
```

The exact aggregate count may be 1–3 depending on whether the worker moved one epoch in flight, but it must not scale with delta count. Repeat with 100 interleaved synthetic stream IDs and assert no more than 100 current keys plus the one bounded in-flight/sealed epoch set.

### `TestV3DurableProgressBacklogFailsRunWithoutBlocking`

Append enough data to exceed 1 MiB while persistence is blocked.

Assert:

```text
TryAppendAssistant returns ErrSessionV3DurableProgressBacklog immediately
provider context is cancelled
no text is silently discarded and reported as success
run ends failed/cancelled with an explicit backlog reason
```

### `TestV3DurableProgressFlushesBeforePreToolAssistantMessage`

Use a provider response with assistant text followed by one function call.

Capture mutation order and assert:

```text
session.assistant.delta for stream step 1
comes before
session.message.appended pre-tool assistant message for stream step 1
```

Assert both carry the same `stream_id` and the committed message’s `stream_offset_end` equals the durable delta end.

### `TestV3CommittedAssistantMetadataMatchesFinalStream`

Run a simple no-tool response. Assert the final message metadata contains:

```text
stream_id = assistant:<run>:step:1
stream_step = 1
stream_offset_end = UTF-8 byte length of content
```

Include non-ASCII text such as `"héllo 🌍"` and compare byte length, not character count.

### `TestV3ProviderStreamPreservesCanonicalBytesExactly`

Emit deltas:

```text
"  hé"
"llo 🌍  "
```

Return a `response.Text` value with different trimming. Assert live text, durable reconstructed text, and the committed assistant message are all exactly `"  héllo 🌍  "`. This test must fail if any integration path calls `strings.TrimSpace` on canonical content.

### `TestV3ProviderResponseWithoutDeltasUsesOneSyntheticRange`

Return nonempty `response.Text` and invoke the callback zero times. Assert one logical live range starts at sequence 1/offset 0, at least one coalesced durable checkpoint covers the same bytes, and the canonical message is identical. Assert there is no direct `recordRunProgress` fallback call outside the sink writer.

### `TestV3DurableProgressWriterRunsWithoutHoldingSinkMutex`

Block inside the writer, then concurrently call `TryAppendAssistant` for additional deltas. Assert those calls complete and merge before releasing the writer. This directly proves the sink lock is not held during persistence.

### `TestV3DurableProgressCloseEnqueueRace`

Race `TryAppendAssistant` against `CloseAndFlush` under `go test -race`. Assert no panic, no send on closed channel, one stable terminal result, and idempotent repeated close.

### `TestV3ProviderHotCallbackHasNoDurableCalls`

The source guard described above.

### `TestV3ProviderStreamDiagnosticsDoNotPersistPerDelta`

Set `SWARM_V3_DIAGNOSTICS=1`. Add a package-private diagnostic-stage observer around `recordSessionV3Diagnostic` if no observer exists. Emit at least 100 deltas.

Assert:

```text
stage session.diagnostic.provider.stream was recorded zero times
request/response diagnostics outside the callback may still occur
assistant durable mutations are coalesced rather than one per delta
```

Do **not** assert that no durable mutation overlaps in wall-clock time with a callback; the durable worker is intentionally allowed to run concurrently. The proof is that the callback does not invoke or wait for it.

### Update existing tests

Update, do not delete:

```text
TestSessionsV3ExecutorCoalescesProviderDeltas
TestSessionsV3ExecutorCoalescesProviderReasoningDeltas
TestSessionsV3ExecutorFlushesProviderDeltaAtSizeBoundary
```

They must now validate the new sink output and new stream/offset fields while retaining their original durable coalescing assertions.

## Checkpoint 2 pass/fail gate

Run:

```bash
go test ./swarmd/internal/api \
  -run 'TestV3(Assistant|CommittedAssistant|DurableProgress|Provider)|TestSessionsV3ExecutorCoalescesProvider' \
  -count=1

go test -race ./swarmd/internal/api \
  -run 'TestV3AssistantCallbackPublishesAllDeltasWhileDurableWriterIsBlocked|TestV3DurableProgress(SinkCoalescesTenThousandDeltasByStream|WriterRunsWithoutHoldingSinkMutex|CloseEnqueueRace)' \
  -count=1
```

**Pass only when all are true:**

```text
[ ] 10,000 callbacks complete before the blocked durable writer is released.
[ ] One continuous stream occupies one durable aggregate, not 10,000 queue entries.
[ ] 100 synthetic stream IDs occupy 100 keyed aggregates.
[ ] Durable overflow fails explicitly and never blocks.
[ ] No durable call exists inside sessionV3ProviderStreamState.Handle.
[ ] Diagnostics enabled produces no per-delta provider-stream diagnostic writes.
[ ] Pre-tool and final messages carry exact stream metadata and untrimmed canonical bytes.
[ ] The writer never holds the sink mutex; close/enqueue races pass under `-race`.
[ ] Durable `delta_index` is monotonic across provider steps.
[ ] Existing primary session tests pass.
[ ] Backend default gate is still false and Desktop still does not request live_patch_v1.
```

Commit with:

```text
stream-decouple cp2: remove persistence from provider callback
```

---

# Checkpoint 3 — Add the disabled Desktop live bypass and one-commit-per-paint accumulator

## Goal

Build the frontend fast path behind a disabled production gate. A test can turn it on, but the production controller must still request no capability after this checkpoint.

## Files to create

```text
web/src/features/desktop/realtime/v3-live-patch-coordinator.ts
web/src/features/desktop/realtime/v3-live-patch-coordinator.spec.ts
```

## Files to modify

```text
web/src/features/desktop/session-v3/types.ts
web/src/features/desktop/session-v3/transport.ts
web/src/features/desktop/realtime/v3-realtime-controller.ts
web/src/features/desktop/realtime/v3-realtime-controller.spec.ts
web/src/features/desktop/state/desktop-v3-cache-types.ts
web/src/features/desktop/state/desktop-v3-cache-reducer.ts
web/src/features/desktop/state/desktop-v3-cache-store.ts
web/src/features/desktop/state/desktop-v3-cache.spec.ts
```

## Production gate for this checkpoint

Add:

```ts
export const DESKTOP_V3_LIVE_PATCH_ENABLED = false
```

The production controller must pass this constant into the transport/coordinator. Unit tests pass `true` explicitly.

Do not change it to `true` until Checkpoint 5.

## Step 3.1 — Add frontend wire types

In `session-v3/types.ts`:

```ts
export const SESSION_V3_REALTIME_LIVE_PATCH_CAPABILITY = 'live_patch_v1'

export interface SessionV3RealtimeLivePatchWire {
  session_id: string
  run_id: string
  stream_id: string
  stream_kind: 'assistant_text'
  operation: 'append'
  step: number
  step_id: string
  live_seq_start: number
  live_seq_end: number
  offset_start: number
  offset_end: number
  text: string
  recorded_at: number
}
```

Add:

```ts
capabilities?: string[]
```

to `SessionV3RealtimeResumeWire`, and:

```ts
live?: SessionV3RealtimeLivePatchWire
capabilities?: string[]
```

to `SessionV3RealtimeFrameWire`.

In `desktop-v3-cache-types.ts`, add `'live.patch'` to `RealtimeKind` and add:

```ts
capabilities?: string[]
live?: SessionV3RealtimeLivePatchWire
```

to `RealtimeMessage`. Do not leave the transport wire and cache/controller frame types disagreeing.

In `transport.ts`, add:

```ts
export interface DesktopV3RealtimeLivePatchEvent
  extends DesktopV3RealtimeTransportMeta {
  patch: SessionV3RealtimeLivePatchWire
}
```

Add a runtime validator; TypeScript types are not wire validation:

```ts
export function requireSessionV3RealtimeLivePatch(
  frame: SessionV3RealtimeFrameWire,
): SessionV3RealtimeLivePatchWire
```

It must throw unless identities are nonempty, top-level/session IDs match, operation/kind are supported, sequences/ranges are positive and ordered, `step > 0`, and:

```ts
utf8ByteLength(patch.text) === patch.offset_end - patch.offset_start
```

A malformed `live.patch` is a protocol error and must use the existing force-reopen/rehydrate path; it must not enter either queue.

## Step 3.2 — Parse once and bypass the durable queue

Current `DesktopV3RealtimeTransport` pushes raw messages into `messageQueue` and awaits `handleMessage`. Replace that behavior.

Change the queue to parsed durable frames:

```ts
private durableMessageQueue: Array<{
  socket: WebSocket
  frame: SessionV3RealtimeFrameWire
  bytes: number
  generation: number
}> = []

private durableMessageQueueBytes = 0
```

Add transport options:

```ts
livePatchEnabled?: boolean
onLivePatch?: (event: DesktopV3RealtimeLivePatchEvent) => void
```

The `message` listener must:

1. Parse the frame immediately with `parseRealtimeFrame`.
2. Compute raw UTF-8 byte length once.
3. If `kind === 'live.patch'`:
   - validate `generation` and current socket,
   - call `requireSessionV3RealtimeLivePatch(frame)`,
   - return immediately when `livePatchEnabled !== true`,
   - call `onLivePatch` synchronously with `{ patch, generation }`,
   - do not push to the durable queue,
   - do not call or await `onFrame`,
   - do not advance the endpoint cursor.
4. For every other kind, enqueue the parsed durable frame and add its bytes to `durableMessageQueueBytes`.
5. Subtract a durable frame's bytes in a `finally` block after it is removed/processed. Generation reset, stop, and overflow must clear both the array and byte counter together.

Rename `drainSocketMessages` to `drainDurableMessages` and make it consume parsed frames.

Bound the durable queue:

```text
maximum 512 frames
maximum 2 MiB raw frame bytes
```

On either overflow:

```text
clear only that generation’s durable queue
emit error status
go through existing forceReopen/rehydrate behavior
```

Do not apply the live overflow policy to the durable queue and do not silently discard a durable frame.

Update `frameAdvancesEndpointCursor` so `live.patch` is explicitly absent.

## Step 3.3 — Request capability only when the test/feature gate is enabled

Change `buildDesktopV3RealtimeResume` input to accept:

```ts
capabilities?: string[]
```

When `livePatchEnabled` is true, include:

```ts
capabilities: [SESSION_V3_REALTIME_LIVE_PATCH_CAPABILITY]
```

When false, omit the property entirely.

This preserves legacy behavior through Checkpoint 4.

## Step 3.4 — Implement the paint-bound coordinator

In `v3-live-patch-coordinator.ts`, create:

```ts
export interface DesktopV3LivePatchCoordinatorDeps {
  getSnapshot: () => DesktopV3CacheState
  commitSnapshot: (
    previous: DesktopV3CacheState,
    next: DesktopV3CacheState,
    actions: DesktopV3CacheAction[],
  ) => void
  requestFrame: (callback: FrameRequestCallback) => number
  cancelFrame: (id: number) => void
  setTimer: (callback: () => void, ms: number) => number
  clearTimer: (id: number) => void
  isDocumentHidden: () => boolean
}

export class DesktopV3LivePatchCoordinator {
  accept(patch: SessionV3RealtimeLivePatchWire, generation: number): void
  flushNow(): void
  flushStreams(keys: string[]): void
  resetGeneration(generation: number): void
  dispose(): void
  debugSnapshotForTests(): DesktopV3LivePatchDebugSnapshot
}
```

Use this key:

```ts
function livePatchKey(patch): string {
  return `${patch.session_id}\u0000${patch.run_id}\u0000${patch.stream_id}`
}
```

Pending entry:

```ts
interface PendingLiveAppend {
  sessionId: string
  runId: string
  streamId: string
  step: number
  stepId: string
  liveSeqStart: number
  liveSeqEnd: number
  offsetStart: number
  offsetEnd: number
  text: Utf8AppendBuffer
  bytes: number
  recordedAt: number
}
```

Add this small helper in the same file:

```ts
class Utf8AppendBuffer {
  private storage = new Uint8Array(256)
  private length = 0

  append(text: string): number
  byteLength(): number
  toString(): string
}
```

`append` encodes with one shared `TextEncoder`, doubles `storage` until the new bytes fit, copies the encoded bytes once, advances `length`, and returns the appended byte count. Never grow beyond the per-stream 64 KiB logical cap. `toString` decodes `storage.subarray(0, length)` with a fatal UTF-8 `TextDecoder`.

Use one `Utf8AppendBuffer` per pending stream. Do not concatenate the whole pending string on every incoming patch and do not keep one JavaScript array element per provider delta. When converting one pending entry back into a batch patch, use its earliest `liveSeqStart`/`offsetStart`, latest `liveSeqEnd`/`offsetEnd`/`recordedAt`, and `text: pending.text.toString()`.

Because storage doubles, allocated capacity may be less than twice logical pending bytes. The logical 64/256 KiB caps remain the admission rule; tests must also assert debug-reported allocated capacity stays below twice the configured logical cap plus one initial 256-byte buffer per active stream.

### `accept` continuity rules

For an already pending key, require both:

```text
patch.live_seq_start == pending.liveSeqEnd + 1
patch.offset_start == pending.offsetEnd
```

For a new pending key, find the matching draft/segment in cache by `stream_id` and derive:

```text
expectedSeq = existing.liveSeqEnd + 1, or 1 when no stream exists
expectedOffset = existing.offsetEnd, or 0 when no stream exists
```

Before declaring a gap, handle a complete duplicate:

```text
patch.live_seq_end <= existing.liveSeqEnd
and patch.offset_end <= existing.offsetEnd
```

A complete duplicate is ignored. Partial overlap in a transient live patch is not accepted; durable overlap is handled in Checkpoint 4.

If either expected sequence or expected offset does not match:

1. Add the key to a `pausedStreams` set.
2. Drop this and later live patches for that same key/generation.
3. Do not reconnect the whole socket.
4. Do not fabricate text.
5. A different `stream_id` remains eligible.

`accept` must also check the cache entry's `livePaused` flag so a durable mismatch discovered by the reducer cannot be bypassed by the next live patch.

### Memory cap behavior

If one stream exceeds 64 KiB or total pending exceeds 256 KiB:

1. Pause that stream.
2. Remove its pending entry.
3. Continue other streams.
4. Do not perform repeated emergency commits in the same scheduling window.

### Scheduling rules

Visible document:

```text
schedule exactly one requestAnimationFrame
```

Hidden document:

```text
schedule exactly one 50 ms timer
```

If more patches arrive while one callback is scheduled, merge them; do not schedule another callback.

Maintain a monotonically increasing `scheduleToken`. Every scheduled callback captures it and returns without mutating state if the token is stale. `flushNow`, `flushStreams`, `resetGeneration`, and `dispose` must cancel the current frame/timer, increment the token, and clear the stored scheduler ID before committing or clearing data. This is the insurance against an already-scheduled callback running after a durable terminal message.

One scheduler callback must generate one `realtime.applyLivePatchBatch` action and one store commit, regardless of patch count or stream count. Its exact commit sequence is:

```ts
const action: DesktopV3CacheAction = {
  type: 'realtime.applyLivePatchBatch',
  patches,
}
const previous = deps.getSnapshot()
const next = applyDesktopV3LivePatchBatch(previous, patches)
deps.commitSnapshot(previous, next, [action])
```

## Step 3.5 — Add a targeted live batch reducer

Add action:

```ts
| {
    type: 'realtime.applyLivePatchBatch'
    patches: SessionV3RealtimeLivePatchWire[]
  }
```

Add this reducer case even though the optimized store helper calls the pure function directly:

```ts
case 'realtime.applyLivePatchBatch':
  return applyDesktopV3LivePatchBatch(state, action.patches)
```

Create and export:

```ts
export function applyDesktopV3LivePatchBatch(
  state: DesktopV3CacheState,
  patches: SessionV3RealtimeLivePatchWire[],
): DesktopV3CacheState
```

This function must be pure and must copy only affected paths:

```text
top-level state
liveRunsBySession
one affected session run map per session
one affected LiveRunOverlay per run
assistantDraft/assistantSegments touched by the stream
```

It must preserve reference identity for:

```text
messagesBySession
sessionsById
permissionsBySession
plansBySession
unaffected sessions in liveRunsBySession
unaffected runs in the same session
```

For a new stream ID:

- If the patch matches `assistantDraft.streamId`, append to that draft.
- Otherwise, if the patch matches an existing `assistantSegments[].streamId`, update that segment in place.
- Otherwise, if `assistantDraft` contains a different stream ID, move that draft into `assistantSegments` first.
- Keep at most one segment per `streamId`; use an upsert helper rather than blindly appending duplicate segments.
- Start a new draft for the patch’s stream ID only when the stream is genuinely new.

Use a stable segment ID such as `live-assistant:${runId}:${streamId}`.

Extend `assistantDraft` with optional compatibility fields:

```ts
streamId?: string
streamStep?: number
stepId?: string
liveSeqEnd?: number
offsetEnd?: number
durableOffsetEnd?: number
livePaused?: boolean
```

Extend each assistant segment with the same optional fields. They are optional because legacy durable events still create run-level overlays without stream identity; every object created by the new live reducer must populate them.

For this checkpoint, applying a contiguous live patch simply appends `text` and advances `liveSeqEnd`/`offsetEnd`.

Add this store function:

```ts
export function commitDesktopV3LivePatchBatch(
  patches: SessionV3RealtimeLivePatchWire[],
): void
```

It must execute exactly:

```ts
const previous = getDesktopV3CacheSnapshot()
const action: DesktopV3CacheAction = {
  type: 'realtime.applyLivePatchBatch',
  patches,
}
const next = applyDesktopV3LivePatchBatch(previous, patches)
commitDesktopV3CacheSnapshot(previous, next, [action])
```

It must not call `reduceDesktopV3CacheActions` and must not use `structuredClone`.

Wire the coordinator to this function in production, still behind `DESKTOP_V3_LIVE_PATCH_ENABLED = false`.

## Required tests

### `Desktop V3 live patch bypasses an unresolved durable frame`

File: `v3-realtime-controller.spec.ts`

1. Make `onFrame` return a Promise that does not resolve for a durable frame.
2. Emit that durable frame.
3. Emit 100 live patches.
4. Assert `onLivePatch` received all 100 before resolving the durable Promise.

This proves live delivery does not wait behind the durable queue.

### `Desktop V3 live patch never enters the durable queue`

Use `debugSnapshotForTests` on transport. Emit 10,000 live frames.

Assert:

```text
durableQueueFrames == 0
durableQueueBytes == 0
```

### `Desktop V3 live accumulators do not rebuild text per patch`

Add source guards that reject `pending.text +=`, `pending.text = pending.text +`, `chunks.push`, and equivalent per-delta string arrays in `v3-live-patch-coordinator.ts`, and reject string concatenation/per-delta string slices in `sessions_v3_live_hub.go`. Require `Utf8AppendBuffer.append` on the frontend and `bytes.Buffer.WriteString` on the backend.

### `Desktop V3 ten thousand patches commit once per animation frame`

Use an injected fake scheduler. Accept 10,000 one-byte contiguous patches for one stream without firing the frame callback.

Assert before frame:

```text
store commits == 0
pending keys == 1
pending bytes == 10,000
allocated pending capacity remains within the documented doubling bound
```

Fire one frame callback. Assert:

```text
store commits == 1
assistant draft text is exactly expected 10,000 bytes
liveSeqEnd == 10,000
offsetEnd == 10,000
```

### `Desktop V3 one frame batches one hundred synthetic streams`

Create 100 distinct synthetic `run_id`/`stream_id` pairs (they may share one session), then interleave 100 patches per pair. Fire one frame.

Assert:

```text
one store commit
100 run-scoped stream aggregates
exact text per stream
no duplicate assistant segment for any stream_id
```

Again, this is not a subagent test.

### `Desktop V3 live patch does not advance endpoint cursor`

Start with cursor `C0`. Emit and flush live patches. Assert cursor remains `C0`.

Then emit a durable event at cursor `C1`; assert it advances to `C1`.

### `Desktop V3 live patch path copies only affected references`

Prepare two sessions and two runs. Apply one live batch to one run.

Assert strict reference equality for every unrelated branch and inequality only for the affected top-level/session/run path.

### `Desktop V3 live gap pauses only one stream`

Apply stream A offset 0–2, stream B offset 0–2, then an invalid A patch starting at offset 7, then a valid B patch starting at offset 2.

Assert:

```text
A is paused and did not fabricate bytes 2–7
B continues and contains all expected text
no reconnect callback was invoked
```

### `Desktop V3 rejects malformed live frames before either queue`

Emit frames with a mismatched top-level session, bad sequence range, and incorrect UTF-8 offset length. Assert `onLivePatch` is not called, durable queue depth stays zero for those frames, and the existing reopen/rehydrate path is triggered.

### `Desktop V3 sustained stream commits once per paint window`

Across 120 fake paint windows, enqueue 100 one-byte patches before firing each window. Assert exactly 120 store commits, monotonically increasing exact text after every window, and no durable-queue growth. This proves the implementation does not merely collapse an entire test burst once; it remains paint-bounded over time.

## Checkpoint 3 pass/fail gate

First, record the actual frontend scripts instead of guessing:

```bash
node -e 'const p=require("./web/package.json"); console.log(JSON.stringify(p.scripts,null,2)); if(!p.scripts || !p.scripts.test) process.exit(2)'
```

Then run the repository’s existing `test` script against these files. Using npm to invoke the existing script is acceptable even when the lockfile is pnpm/yarn:

```bash
cd web
npm run test -- \
  src/features/desktop/realtime/v3-live-patch-coordinator.spec.ts \
  src/features/desktop/realtime/v3-realtime-controller.spec.ts \
  src/features/desktop/state/desktop-v3-cache.spec.ts
```

Also run the existing typecheck script if one is present in the printed scripts. Do not invent a new test runner.

**Pass only when all are true:**

```text
[ ] Live frames bypass an unresolved durable frame.
[ ] 10,000 live frames create zero durable-queue entries.
[ ] 10,000 patches result in exactly one paint-window store commit.
[ ] One live update preserves every unrelated state reference.
[ ] A gap pauses one stream without reconnecting the global socket.
[ ] Malformed live frames enter neither queue.
[ ] Sustained traffic produces exactly one commit per fake paint window.
[ ] Endpoint cursor remains unchanged by live frames.
[ ] Backend and frontend production defaults remain false.
```

Commit with:

```text
stream-decouple cp3: add disabled frontend live bypass
```

---

# Checkpoint 4 — Reconcile live patches with durable checkpoints and exact committed streams

## Goal

Guarantee that live text appears once, durable checkpoints do not duplicate it, pre-tool commits clear only their own stream, and reconnect gaps never fabricate text. The production capability remains disabled during this checkpoint.

## Files to modify

```text
web/src/features/desktop/realtime/v3-live-patch-coordinator.ts
web/src/features/desktop/realtime/v3-live-patch-coordinator.spec.ts
web/src/features/desktop/realtime/v3-realtime-controller.ts
web/src/features/desktop/realtime/v3-realtime-controller.spec.ts
web/src/features/desktop/state/desktop-v3-cache-types.ts
web/src/features/desktop/state/desktop-v3-cache-reducer.ts
web/src/features/desktop/state/desktop-v3-cache.spec.ts
web/src/features/desktop/state/desktop-v3-cache-wire.ts
web/src/features/desktop/state/desktop-v3-cache-selectors.ts
web/src/features/desktop/state/live-assistant-segments.ts
web/src/features/desktop/state/live-assistant-segments.spec.ts
web/src/features/desktop/chat/components/desktop-v3-existing-conversation-pane.tsx
```

Modify the exact subset that exists/owns the behavior; do not create a second assistant-stream state authority.

## Step 4.1 — Flush pending live work before a related durable frame

Add to the coordinator:

```ts
beforeDurableFrame(frame: RealtimeMessage): void
afterDurableFrame(frame: RealtimeMessage): void
```

Add a helper that extracts stream effects from durable frames:

```ts
interface DurableAssistantStreamEffect {
  key: string
  sessionId: string
  runId: string
  streamId: string
  kind: 'checkpoint' | 'committed-message'
}
```

Read stream identity from:

```text
assistant delta event payload.stream_id
committed message.metadata.stream_id
```

Build the effect using `normalizeRealtimeEventFrame(frame)` and the existing decoded payload helpers; do not write a second JSON payload parser. Recognize assistant checkpoints from event types `session.assistant.delta`/`session.message.delta`, and assistant committed messages from a decoded `payload.message` (or the existing direct-message fallback) whose role is `assistant` and metadata contains `stream_id`.

`beforeDurableFrame` must:

1. If the frame affects an assistant stream and pending live data exists, call `flushStreams([effect.key])` before durable reduction. Do not flush unrelated streams.
2. If it is a committed assistant message, flush already-accepted pending text for that key, then tombstone that exact key and delete any newly pending entry before awaiting the durable commit. Increment the scheduler token so an old rAF/timer cannot recreate it.
3. Leave other stream IDs untouched.

`afterDurableFrame` may remove pending bookkeeping, but a committed stream tombstone remains until socket generation reset.

In `DesktopV3RealtimeControllerRuntime.handleFrame`, use this exact order:

```ts
this.livePatchCoordinator.beforeDurableFrame(frame)
await commitDesktopV3StreamFrame(this.streamCommit, frame)
this.livePatchCoordinator.afterDurableFrame(frame)
```

On commit failure, keep existing durable recovery behavior and reset the live coordinator generation.

## Step 4.2 — Reconcile durable assistant deltas by UTF-8 byte range

In `applyLiveRunOverlayFromEvent`, retain the old legacy behavior when `payload.stream_id` is absent.

When `stream_id`, `offset_start`, and `offset_end` are present:

1. Validate `offset_end-offset_start === utf8ByteLength(delta)`. On failure, leave visible text unchanged and set `livePaused=true` on an existing matching stream.
2. Find the matching assistant draft or segment by `streamId`.
3. Treat its content as bytes `0..offsetEnd`. New live-created streams always satisfy this invariant.
4. Compare every overlapping byte before deciding to ignore or append. Equal offsets alone are not proof that live and durable text match.

Add helpers:

```ts
export function utf8SuffixAfterBytes(text: string, skipBytes: number): string
export function utf8RangeEquals(
  visibleText: string,
  visibleRangeStart: number,
  durableText: string,
  durableRangeStart: number,
  overlapStart: number,
  overlapEnd: number,
): boolean
```

Use `TextEncoder` for comparison. `utf8SuffixAfterBytes` must use:

```ts
const encoded = new TextEncoder().encode(text)
const decoder = new TextDecoder('utf-8', { fatal: true })
return decoder.decode(encoded.slice(skipBytes))
```

If a byte slice cuts through a code point, decoding fails, or overlapping bytes differ, mark the exact existing stream `livePaused=true`, do not append, and do not insert `�`.

Use these cases after overlap validation:

### Case A — no matching stream exists

```text
durable offset_start == 0
```

Create a stream-aware assistant draft from the durable delta with:

```text
offsetEnd = durable offset_end
durableOffsetEnd = durable offset_end
liveSeqEnd = 0
```

If no stream exists and `durable offset_start > 0`, do not fabricate its prefix and do not create visible text. The next live patch will fail the expected-zero check and become paused; hydrate/canonical completion remains the repair path.

### Case B — durable range is already fully visible

```text
durable offset_end <= visible offsetEnd
```

After proving the overlap bytes match, append nothing and set:

```text
durableOffsetEnd = max(current, durable offset_end)
```

### Case C — durable range begins exactly at the visible end

```text
durable offset_start == visible offsetEnd
```

Append the entire durable delta and advance `offsetEnd` and `durableOffsetEnd`.

### Case D — durable range partially overlaps visible text

```text
durable offset_start < visible offsetEnd < durable offset_end
```

First compare the durable prefix with the corresponding visible bytes. Then compute:

```text
skipBytes = visible offsetEnd - durable offset_start
```

Append only `utf8SuffixAfterBytes(delta, skipBytes)` and advance both offsets.

### Case E — durable gap

```text
durable offset_start > visible offsetEnd
```

Set `livePaused=true` on the exact stream, append nothing, and fabricate nothing.

The coordinator's `accept` method must observe `livePaused` from cache and add that key to its generation-local paused set before dropping later live patches.

## Step 4.3 — Clear only the committed stream

Modify both:

```text
finalizeLiveRunForCommittedMessage
reconcileLiveRunWithCommittedMessage
```

For an assistant message:

1. Read `stream_id` from message metadata.
2. If present:
   - delete `assistantDraft` only if its `streamId` matches,
   - filter only matching `assistantSegments`,
   - preserve all other stream IDs in the run.
3. If absent, use the existing legacy run-level clearing behavior for compatibility.

Example that must work:

```text
step 1 stream is committed as pre-tool message
step 2 stream is currently live
step 1 commit removes only step 1
step 2 remains visible
```

Do not infer the stream from message order.

## Step 4.4 — Preserve raw assistant bytes when drafts become segments or render items

Update `flushLiveAssistantDraftToSegment` and every stream-aware segment upsert so `segment.content` receives the draft's content unchanged.

In `buildDesktopV3LiveRunRenderItems`, change the assistant-segment path from trimming the value that is rendered to trimming only for the emptiness check:

```ts
const content = segment.content
if (!content.trim()) continue
if (options.assistantMessages?.has(normalizeReplayContent(content))) continue
items.push({
  type: 'live-assistant',
  id: segment.id,
  content,
  timelineSeq: segment.timelineSeq,
})
```

Do not change canonical-dedup normalization in this checkpoint; it may normalize a copy for comparison, but the content passed to rendering and stored in state must remain byte-for-byte unchanged.

## Step 4.5 — Stop retaining high-frequency deltas as event history

In `applyCacheEvent`, add:

```ts
function shouldRetainRealtimeEvent(eventType: string): boolean {
  switch (eventType) {
    case 'session.assistant.delta':
    case 'session.message.delta':
    case 'session.reasoning.delta':
    case 'session.tool.delta':
      return false
    default:
      return true
  }
}
```

Only append `event.sessionEvent` to `eventsBySession` when this returns true.

Still apply every event to the live overlay. This change affects retention, not rendering or durable replay correctness.

Update existing tests that expected assistant delta entries in `eventsBySession`; they must now assert the overlay is correct and retained high-frequency delta count is zero.

## Step 4.6 — Define reconnect behavior without inventing a baseline protocol

This pass does not add a server-side active-stream baseline.

On socket generation change:

```text
clear pending patches
clear stream tombstones
clear paused-stream set
```

On the first patch for a stream after reconnect:

1. Look up the latest applied live/durable offset in cache.
2. Accept only if `offset_start` equals that offset.
3. Otherwise pause that stream and rely on durable checkpoints.
4. Continue accepting a new stream ID, such as the next provider step or next run.

This deliberately favors correctness over showing uncommitted bytes after reconnect. It must never fabricate the missing suffix.

## Required tests

### `Desktop V3 durable checkpoint overlapping pending live text renders once`

Arrange pending live text `"hello world"`, offset 0–11, then deliver a durable checkpoint for the same stream with `delta="hello world"`, offset 0–11.

Assert final visible text is exactly `"hello world"`, not duplicated.

### `Desktop V3 durable checkpoint appends only unseen UTF-8 suffix`

Live text: `"hé"` with correct byte offset 3. Durable checkpoint: `"héllo 🌍"` offset 0 through its full byte length.

Assert the final text is exactly `"héllo 🌍"` and no replacement character appears.

### `Desktop V3 invalid UTF-8 overlap pauses repair`

Create an overlap whose byte split lands inside `é` or the emoji. Assert the stream is paused and text is unchanged.

### `Desktop V3 same offsets with different bytes pauses repair`

Make live text and durable text cover the same byte range but differ by one byte. Assert the durable event is not silently treated as already visible, the stream is paused, and existing text is unchanged.

### `Desktop V3 pre-tool committed message clears only matching stream`

Create:

```text
assistant:run-1:step:1 as a segment
assistant:run-1:step:2 as current draft
```

Commit a message with metadata `stream_id=assistant:run-1:step:1`.

Assert step 1 is removed and step 2 remains byte-for-byte unchanged.

### `Desktop V3 terminal commit prevents scheduled live resurrection`

Accept a live patch and schedule an rAF without firing it. Process the committed assistant message for that stream. Then fire the old rAF callback.

Assert the live draft does not reappear.

### `Desktop V3 assistant segment preserves leading and trailing whitespace`

Create a live stream whose text is `"  héllo 🌍  "`, force it from draft to segment, and build render items. Assert the segment state and render item content retain the exact string. Trimming is allowed only for the empty/dedup comparison.

### `Desktop V3 reconnect gap falls back to durable progress`

State has durable offset 60. Reset generation. Receive a live patch beginning at 100.

Assert it is paused and nothing is fabricated.

Then deliver a durable checkpoint covering 60–100. Assert durable text is applied. A later live patch for the same paused stream remains ignored. A new step-2 stream beginning at offset 0 is accepted.

### `Desktop V3 high-frequency deltas are not retained`

Apply 10,000 assistant delta events, 10,000 reasoning delta events, and 10,000 tool delta events through the reducer.

Assert:

```text
overlays reflect the latest/correct values
eventsBySession contains zero of those delta event types
non-delta lifecycle/terminal events remain retained
```

### Update the existing repair/live ordering test

The current test that asserts event IDs `4` and `5` are retained must instead assert:

```text
assistantDraft content is abcde
lastEventSeqSeen is 5
endpoint cursor is cursor-live-5
eventsBySession has no retained assistant delta rows
```

## Checkpoint 4 pass/fail gate

```bash
cd web
npm run test -- \
  src/features/desktop/realtime/v3-live-patch-coordinator.spec.ts \
  src/features/desktop/realtime/v3-realtime-controller.spec.ts \
  src/features/desktop/state/desktop-v3-cache.spec.ts \
  src/features/desktop/state/live-assistant-segments.spec.ts
```

Run the existing typecheck script when present.

**Pass only when all are true:**

```text
[ ] Durable/live overlap produces exact text once.
[ ] UTF-8 overlap uses byte offsets, byte equality, and fatal decoding.
[ ] Draft-to-segment/render transitions preserve exact assistant bytes.
[ ] Step-1 commit cannot erase step-2 live text.
[ ] A scheduled callback cannot resurrect a committed stream.
[ ] Reconnect gaps pause only the affected stream and fabricate nothing.
[ ] High-frequency delta retention is zero.
[ ] Backend and frontend production live defaults remain disabled.
```

Commit with:

```text
stream-decouple cp4: reconcile live and durable assistant streams
```

---

# Checkpoint 5 — Enable the capability and prove the complete high-rate primary path

## Goal

Turn on the already-tested path, prove it with deterministic backend and frontend stress tests, run all regressions, and leave a measurable rollback boundary.

## Files to modify

Only the two defaults, final wiring, and stress tests should need changes now:

```text
swarmd/internal/api/server.go
web/src/features/desktop/realtime/v3-realtime-controller.ts
web/src/features/desktop/realtime/v3-realtime-controller.spec.ts
web/src/features/desktop/chat/components/desktop-chat-v3-markdown-stream-duplicates.e2e.spec.ts
swarmd/internal/api/sessions_v3_primary_test.go
swarmd/internal/api/sessions_v3_realtime_ws_test.go
```

Do not add a new architecture in this checkpoint. A large redesign here means an earlier checkpoint was incomplete.

## Step 5.1 — Enable both production gates

Change exactly:

```go
const v3LivePatchDefaultEnabled = true
```

and:

```ts
export const DESKTOP_V3_LIVE_PATCH_ENABLED = true
```

Do not delete the per-server/per-transport booleans; tests and emergency rollback still need explicit control.

The production `DesktopV3RealtimeControllerRuntime` must now:

1. Construct one `DesktopV3LivePatchCoordinator` for the retained global socket.
2. Pass `livePatchEnabled: true` to `DesktopV3RealtimeTransport`.
3. Pass `onLivePatch` directly to coordinator `accept`.
4. Add `live_patch_v1` to resume capabilities.
5. Reset the coordinator on socket generation/reconnect/stop.
6. Preserve exactly one global `/v3/realtime/stream` socket.

Do not add `/v3/sessions/{id}/stream` or another WebSocket.

## Step 5.2 — Add deterministic end-to-end backend stress

### `TestV3PrimaryAssistantTenThousandDeltasEndToEnd`

This test must use the real `sessionV3Executor`, fake provider runner, durable sink, live hub, session service, and canonical final message path.

Use 10,000 deterministic chunks. Include multibyte chunks periodically, for example:

```go
if i%1000 == 0 {
    chunk = "🌍"
} else {
    chunk = "x"
}
```

Build `expected` once in the test.

Block the durable progress mutation before it completes. Require this ordering:

```text
provider emitted all 10,000 callbacks
live subscriber received full logical sequence/text
only then test releases durable mutation
run completes
canonical assistant message equals expected
```

Assert:

```text
last live sequence == 10,000
last live offset == len([]byte(expected))
durable checkpoint offsets are contiguous
committed message metadata stream_offset_end == len([]byte(expected))
only one canonical final assistant message exists
```

### `TestV3PrimaryAssistantSlowBrowserDoesNotSlowProvider`

Create one live subscriber that never drains and one that drains. Emit enough text to overflow the slow subscriber.

Assert all provider callbacks complete before any slow-browser handling is awaited, fast subscriber continues, and the canonical run completes.

### `TestV3PrimaryAssistantLiveWebSocketContinuesWhileDurableBlocked`

Use the real `/v3/realtime/stream` test server, enable the server gate, complete resume/subscription replay, then start a primary run with the durable writer blocked. Read and reconstruct `live.patch` frames from the real WebSocket. Assert the complete logical live sequence and text arrive before releasing the writer. Then release it and assert canonical completion. This closes the gap between the direct-hub callback proof and the socket contract proof.

## Step 5.3 — Add deterministic frontend stress

### `Desktop V3 high-rate live stream renders exact text without chunked store commits`

Using `FakeWebSocket` and fake frame scheduler:

1. Open the one transport.
2. Assert the resume requests `live_patch_v1`.
3. Emit 10,000 live patches before firing one frame.
4. Assert zero durable-queue frames and zero store commits before the frame.
5. Fire one frame.
6. Assert exactly one live store commit and exact text.
7. Continue for 120 paint windows with additional patches and assert exactly one commit per window.
8. Emit overlapping durable checkpoints.
9. Emit the committed assistant message.
10. Assert exact final rendered message and no live duplicate.
11. Assert endpoint cursor advanced only on durable frames.

### Update the markdown duplicate E2E test

The existing markdown stream duplicate regression must run with the live path enabled. Feed markdown tokens split across awkward boundaries, such as:

```text
"**bo"
"ld** and `co"
"de`\n\n- it"
"em"
```

Assert the live rendered content and canonical committed content are identical and each token appears once.

## Step 5.4 — Add source guards that lock in the architecture

### Backend source guard

Fail when `sessionV3ProviderStreamState.Handle` contains any forbidden identifier from Checkpoint 2.

Also require it contains:

```text
v3LiveHub.publish
TryAppendAssistant
```

### Frontend source guard

Use the same brace-depth scanner approach to extract the `kind === 'live.patch'` branch. Fail when it contains:

```text
messageQueue.push
durableMessageQueue.push
await onFrame
advanceEndpointCursor
```

Fail when `v3-live-patch-coordinator.ts` contains:

```text
structuredClone
```

Require the production runtime to contain exactly one construction/use of `/v3/realtime/stream` through the existing transport and no session-specific stream endpoint.

## Step 5.5 — Run the full regression suites

Backend targeted first:

```bash
go test ./swarmd/internal/api \
  -run 'TestV3PrimaryAssistant|TestV3RealtimeLive|TestV3Assistant|TestV3DurableProgress|TestSessionsV3Executor' \
  -count=1

go test -race ./swarmd/internal/api \
  -run 'TestV3PrimaryAssistant(TenThousandDeltasEndToEnd|SlowBrowserDoesNotSlowProvider|LiveWebSocketContinuesWhileDurableBlocked)|TestV3RealtimeLiveHub' \
  -count=1
```

Then all backend packages:

```bash
go test ./swarmd/internal/api -count=1
go test ./swarmd/internal/session/... -count=1
go test ./swarmd/internal/store/pebble/... -count=1
```

Frontend targeted:

```bash
cd web
npm run test -- \
  src/features/desktop/realtime/v3-live-patch-coordinator.spec.ts \
  src/features/desktop/realtime/v3-realtime-controller.spec.ts \
  src/features/desktop/state/desktop-v3-cache.spec.ts \
  src/features/desktop/state/live-assistant-segments.spec.ts \
  src/features/desktop/chat/components/desktop-chat-v3-markdown-stream-duplicates.e2e.spec.ts
```

Then run the repository’s full existing frontend test script and existing typecheck/build scripts exactly as printed from `web/package.json`.

## Final hard acceptance matrix

The feature is complete only when every row is proven by an automated test:

| Requirement | Hard proof |
|---|---|
| Provider callback does not wait for Pebble | 10,000 callbacks finish while progress mutation is blocked |
| Provider callback has no durable call sites | Source guard on isolated `Handle` body |
| Diagnostics cannot reintroduce callback writes | Runtime test with `SWARM_V3_DIAGNOSTICS=1` |
| Live hub is not O(total deltas) | 10,000 same-stream patches occupy one key; 100 interleaved streams occupy 100 keys |
| Text accumulation is bounded and linear | Go uses one `bytes.Buffer` per stream; frontend uses one growable UTF-8 buffer; source guards reject concatenation/per-delta arrays |
| Slow browser cannot slow provider | Subscriber-local overflow test |
| Only one socket writer exists | At least two writes observed; max active `WriteText` equals 1 |
| Stalled socket cannot pin the writer forever | Real stopped-reader test exits through write deadline |
| Live frames bypass durable frontend work | Live callbacks run while durable `onFrame` Promise is blocked |
| Live frames do not fill raw/durable queue | Queue depth remains 0 after 10,000 live frames |
| Frontend publication is paint-bounded | One commit for 10,000-patch burst and exactly one per each of 120 sustained fake paint windows |
| Live frames do not change durable cursor | Cursor unchanged until durable frame |
| Durable overlap does not duplicate or hide mismatch | Exact UTF-8 overlap plus same-range/different-byte repair test |
| Pre-tool commit is stream-specific | Step-1 commit preserves step-2 draft |
| Reconnect never fabricates text | Gap pauses stream and durable fallback test |
| Delta history does not grow forever | 30,000 high-frequency deltas retained as 0 event-history rows |
| Canonical completion remains authoritative | Final message exact—including leading/trailing whitespace—one copy, live stream removed |
| Rollback gates work | Backend and frontend defaults are off through CP4, both enabled only in CP5 |
| Existing clients remain compatible | Legacy resume test and full old suites |

## Final pass condition stated as one sentence

The implementation passes only when a primary provider can emit 10,000 assistant deltas while the durable writer is blocked, a real live WebSocket can deliver those ranges independently, the browser publishes them at no more than one commit per paint window without advancing its durable cursor, and later durable checkpoints plus the committed assistant message converge to the exact same untrimmed UTF-8 text once—with no callback write, no per-delta queue growth, no stalled-socket leak, and no retained delta history.

Commit with:

```text
stream-decouple cp5: enable and stress primary live streaming
```

---

# 4. What is deliberately deferred until after this plan

The next plan may migrate these producers onto the same generic keys:

```text
subagent child assistant streams
task launch progress
tool stdout/delta streams
live reasoning replace streams
active-stream reconnect baselines
```

Do not begin that work until all five checkpoints above pass. When subagents are repaired, they should create independent `stream_id` values and use the same keyed hub/coordinator; they must not introduce another socket, another store, or a per-delta FIFO.
