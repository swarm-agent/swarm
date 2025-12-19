# SDK Plan: `spawn()` - Headless Agent Runner

## Overview

A thin wrapper over the existing SDK that makes it dead simple to spawn headless agents with preset permission profiles.

## What Already Exists

```typescript
// packages/sdk/js - ALREADY BUILT
client.event.subscribe()     // SSE stream → ALL bus events
client.session.create()      // create session
client.session.prompt()      // send prompt, returns when done
client.session.abort()       // cancel
```

The `/event` endpoint streams **every bus event** - tool calls, messages, progress, completion.

---

## Tool Profiles (Presets)

| Profile | Tools | Bash | Use Case |
|---------|-------|------|----------|
| `analyze` | read, glob, grep, list | deny | Code review, understanding |
| `edit` | + edit, patch | deny | Safe refactoring |
| `full` | all | ask | Full tasks with oversight |
| `yolo` | all | allow | CI/automation (dangerous!) |

---

## CLI Interface

```bash
# Simple
swarm run "fix the tests"

# With profile
swarm run --analyze "what does auth.ts do"
swarm run --edit "add error handling"
swarm run --full "set up CI"
swarm run --yolo "refactor everything"

# JSON output for scripts
swarm run --json "fix bug" | jq .success

# Callback when done
swarm run --callback=https://my.api/done "run tests"
```

---

## SDK Interface

```typescript
import { spawn } from '@opencode-ai/sdk'

// Simple - string only
const result = await spawn("fix the tests").wait()

// With profile
const result = await spawn({ prompt: "refactor", profile: "edit" }).wait()

// With streaming
const agent = await spawn({ prompt: "refactor", profile: "edit" })
for await (const event of agent.stream()) {
  console.log(event.type, event.properties)
}

// Fire and forget with callback
spawn({
  prompt: "run full test suite",
  profile: "full",
  onEvent: (e) => console.log(e)
}).wait().then(r => sendToMyServer(r))
```

---

## Types

```typescript
type Profile = 'analyze' | 'edit' | 'full' | 'yolo'

interface SpawnOptions {
  prompt: string
  profile?: Profile              // default: 'edit'
  tools?: Record<string, boolean> // override specific tools
  timeout?: number               // ms, default 600000 (10min)
  onEvent?: (event: BusEvent) => void  // real-time events
}

interface SpawnHandle {
  sessionId: string
  wait(): Promise<SpawnResult>
  stream(): AsyncIterable<BusEvent>
  abort(): void
}

interface SpawnResult {
  success: boolean
  error?: string
  duration_ms: number
  tool_calls: number
  files_modified: string[]
  summary: string
}
```

---

## Files to Create

| File | Description |
|------|-------------|
| `packages/sdk/js/src/spawn.ts` | spawn() function |
| `packages/sdk/js/src/profiles.ts` | Tool profile definitions |
| `packages/opencode/src/cli/cmd/run.ts` | CLI command |

---

## Implementation Notes

1. **Leverages existing SSE** - `/event` already streams everything
2. **Leverages existing SDK** - just wraps session.create/prompt/abort
3. **Profiles = permission presets** - noob picks preset, pro overrides
4. **Streaming is free** - event.subscribe() already works
5. **No new server code** - everything exists, just need the wrapper

---

## Future Phases

- **Phase 2**: `swarm init --auto` - AI generates sandbox config
- **Phase 3**: Dashboard sandbox builder (web UI)
- **Phase 4**: Docker mode for full isolation
- **Phase 5**: Remote approval / phone-home
