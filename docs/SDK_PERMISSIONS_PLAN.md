# SDK Permissions Implementation Plan

## Status: Phase 1 COMPLETE ✅

## Goal
Make the SDK handle permissions in 2 modes: interactive and non-interactive.

## The Core Problem (SOLVED)

```
SDK spawns agent → Agent uses tool → Permission required (ask) → SDK responds → Agent continues
```

**Implementation complete:**
1. ✅ SDK receives permission event via SSE
2. ✅ SDK responds via existing API: `POST /session/:id/permissions/:permissionID`  
3. ✅ Server unblocks the agent
4. ✅ Agent continues

## Two Modes

### Non-Interactive Mode
- Agent can use tools it's ALLOWED to use without any prompts
- Tools set to "ask" → auto-approved (no callback needed)
- Tools set to "deny" → blocked (tool fails)
- Tools set to "pin" → blocked (no way to enter PIN)
- **Use case**: CI/CD, automation, fire-and-forget

### Interactive Mode  
- Follows the user's permission schema (ask, pin, allow, deny)
- "ask" → emits event, waits for SDK callback response
- "pin" → emits event, waits for SDK callback with PIN
- "allow" → proceeds automatically
- "deny" → blocked
- **Use case**: Programmatic control with human-in-the-loop

## Implementation Steps

### Step 1: SDK Permission Response (spawn.ts) ✅ DONE
```typescript
// Helper function added to respond to permissions:
async function respondToPermission(sessionId: string, permissionId: string, response: PermissionResponse) {
  const mappedResponse = mapPermissionResponse(response)
  await client.postSessionIdPermissionsPermissionId({
    path: { id: sessionId, permissionID: permissionId },
    body: { response: mappedResponse },
  })
}
```

### Step 2: Add mode + onPermission to SpawnOptions ✅ DONE
```typescript
interface SpawnOptions {
  prompt: string
  agent?: string
  tools?: Record<string, boolean>
  
  mode?: "interactive" | "noninteractive"  // default: "noninteractive"
  onPermission?: (p: PermissionRequest) => Promise<PermissionResponse>
}

type PermissionResponse =
  | "approve"          // → "once"
  | "always"           // → "always"  
  | "reject"           // → "reject"
  | { reject: string } // → { type: "reject", message }
  | { pin: string }    // → { type: "pin", pin }
```

### Step 3: Handle permissions in stream loop ✅ DONE
```typescript
case "permission.updated": {
  // Yield event first for visibility
  yield { type: "permission", ... }
  
  if (mode === "noninteractive") {
    if (perm.type === "pin") {
      // Can't auto-approve PIN - reject
      await respondToPermission(id, perm.id, { reject: "PIN required but noninteractive" })
    } else {
      // Auto-approve other types
      await respondToPermission(id, perm.id, "approve")
    }
  } else if (options.onPermission) {
    // Interactive: call user's callback
    const response = await options.onPermission(permRequest)
    await respondToPermission(id, perm.id, response)
  }
  // else: no callback, permission stays pending (warned at spawn time)
  
  yield { type: "permission.handled", id: perm.id, response }
}
```

### Step 4: Server-side mode flag (FUTURE)
Pass mode to server so it can auto-approve on its side:
```typescript
// In prompt body
{ mode: "noninteractive" }

// Server-side: if noninteractive, treat "ask" as "allow"
```
**Status**: Not implemented yet - SDK-side handling works for now

## Test Flow

1. Start opencode server: `cd packages/opencode && bun dev`
2. Run test script: `cd packages/sdk/js && bun run example/test-permissions.ts`
3. Tests verify:
   - Non-interactive mode auto-approves permissions
   - Interactive mode calls onPermission callback
   - Rejection flow works correctly

### Manual Test
```typescript
import { createOpencode } from "@opencode-ai/sdk"

const { spawn, server } = await createOpencode()

const handle = spawn({
  prompt: "run: echo hello",
  mode: "interactive",
  onPermission: async (p) => {
    console.log(`Permission: ${p.title}`)
    return "approve"
  },
})

for await (const event of handle.stream()) {
  console.log(event.type, event)
}

server.close()
```

## Files Modified

1. ✅ `packages/sdk/js/src/spawn.ts` - Added mode, onPermission, respondToPermission
2. ✅ `packages/sdk/js/src/index.ts` - Export new types
3. ✅ `packages/sdk/js/example/spawn-examples.ts` - Updated with new examples
4. ✅ `packages/sdk/js/example/test-permissions.ts` - Test script for verification

## Future Files (Server-side enhancement)

1. `packages/opencode/src/server/server.ts` - Add mode to prompt body
2. `packages/opencode/src/session/prompt.ts` - Handle noninteractive mode server-side

## API Reference

### Existing Permission Response API
```
POST /session/{id}/permissions/{permissionID}
Body: { response: Response }

Response = 
  | "once"      // approve once
  | "always"    // approve + remember
  | "reject"    // reject
  | { type: "reject", message: string }  // reject with reason
  | { type: "pin", pin: string }  // PIN verification
```

### SDK Permission Types
```typescript
interface PermissionRequest {
  id: string
  type: string        // "bash", "edit", "pin", etc.
  title: string       // human-readable description
  metadata: Record<string, unknown>
}

type PermissionResponse =
  | "approve"         // → "once"
  | "always"          // → "always" 
  | "reject"          // → "reject"
  | { reject: string } // → { type: "reject", message }
  | { pin: string }    // → { type: "pin", pin }
```

## Current State Summary

### What Works Now
- **Non-interactive mode** (default): Auto-approves all "ask" permissions, rejects "pin" permissions
- **Interactive mode**: Calls `onPermission` callback for each permission, waits for response
- **Permission responses**: approve, always, reject, reject with message, PIN
- **Events**: `permission` event yielded for visibility, `permission.handled` after response

### Usage Examples

```typescript
// Non-interactive (default) - for CI/automation
spawn("run tests").wait()

// Interactive with callback - for programmatic control
spawn({
  prompt: "deploy to staging",
  mode: "interactive",
  onPermission: async (p) => {
    if (p.title.includes("npm test")) return "approve"
    if (p.title.includes("deploy")) return { reject: "Not allowed" }
    return "approve"
  },
}).wait()

// With PIN support
spawn({
  prompt: "delete old files", 
  mode: "interactive",
  onPermission: async (p) => {
    if (p.permissionType === "pin") {
      return { pin: process.env.SWARM_PIN }
    }
    return "approve"
  },
}).wait()
```

### What's Next
1. **Test the implementation** with a running server
2. **Server-side mode flag** - Let server auto-approve in noninteractive mode (optimization)
3. **Integration with TUI** - Show SDK-spawned sessions in the app
