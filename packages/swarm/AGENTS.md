# Swarm Development Guidelines

## Build/Test Commands

- **Install**: `bun install`
- **Run**: `bun run index.ts`
- **Typecheck**: `bun run typecheck` (npm run typecheck)
- **Test**: `bun test` (runs all tests)
- **Single test**: `bun test test/tool/tool.test.ts` (specific test file)

## Code Style

- **Runtime**: Bun with TypeScript ESM modules
- **Imports**: Use relative imports for local modules, named imports preferred
- **Types**: Zod schemas for validation, TypeScript interfaces for structure
- **Naming**: camelCase for variables/functions, PascalCase for classes/namespaces
- **Error handling**: Use Result patterns, avoid throwing exceptions in tools
- **File structure**: Namespace-based organization (e.g., `Tool.define()`, `Session.create()`)

## Architecture

- **Tools**: Implement `Tool.Info` interface with `execute()` method
- **Context**: Pass `sessionID` in tool context, use `App.provide()` for DI
- **Validation**: All inputs validated with Zod schemas
- **Logging**: Use `Log.create({ service: "name" })` pattern
- **Storage**: Use `Storage` namespace for persistence
- **API Client**: Go TUI communicates with TypeScript server via stainless SDK. When adding/modifying server endpoints in `packages/swarm/src/server/server.ts`, ask the user to generate a new client SDK to proceed with client-side changes.

## Tool Animation Pattern (TUI)

This documents the pattern for creating custom streaming tool animations in the TUI.

### Overview

Tool animations display tool execution state in the TUI. They receive props from the ToolRegistry and must handle all states: `pending`, `running`, `completed`, `error`.

### Key Files

- **Animation Components**: `/packages/swarm/src/cli/cmd/tui/ui/tool-animations.tsx`
- **Tool Registry**: `/packages/swarm/src/cli/cmd/tui/routes/session/index.tsx`
- **Spinner Definitions**: `/packages/swarm/src/cli/cmd/tui/ui/spinner-definitions.ts`

### Props Available to Tool Renderers

Tool renderers receive these props via `ToolProps<T>`:

```typescript
type ToolProps<T extends Tool.Info> = {
  input: Partial<Tool.InferParameters<T>> // Tool input params (command, filePath, etc.)
  metadata: Partial<Tool.InferMetadata<T>> // Streaming metadata during execution
  permission: Record<string, any> // Permission dialog data
  tool: string // Tool name
  output?: string // Final output (only when completed)
  state: ToolPart["state"] // Full state object with status, time, etc.
}
```

### State Object Structure

```typescript
// Pending state
{ status: "pending", input: {...}, raw: string }

// Running state
{ status: "running", input: {...}, title?: string, metadata?: {...}, time: { start: number } }

// Completed state
{ status: "completed", input: {...}, output: string, title: string, metadata: {...}, time: { start, end } }

// Error state
{ status: "error", input: {...}, error: string, metadata?: {...}, time: { start, end } }
```

### Pattern for Creating Tool Animations

#### Step 1: Create Animation Components in `tool-animations.tsx`

Create separate components for each state, plus a main component that switches between them:

```typescript
// 1. Define color palette for the tool
const TOOL_COLORS = {
  primary: RGBA.fromHex("#ff4444"),
  dim: RGBA.fromHex("#992929"),
  // etc.
}

// 2. Create spinner component (optional custom spinner)
export function ToolSpinner(props: { color?: RGBA }) {
  const [frame, setFrame] = createSignal(0)
  const frames = ["⋮", "⋰", "⋯", "⋱"]

  const interval = setInterval(() => {
    setFrame((f) => (f + 1) % frames.length)
  }, 150)
  onCleanup(() => clearInterval(interval))

  return <span style={{ fg: props.color }}>{frames[frame()]}</span>
}

// 3. Create pending state component
export function ToolPending(props: { input?: string; color?: RGBA }) {
  return (
    <box gap={0}>
      <text>
        <ToolSpinner color={props.color} /> <span>Waiting...</span>
        <Show when={props.input}>
          <span>{props.input}</span>
        </Show>
      </text>
    </box>
  )
}

// 4. Create running state component (with streaming output)
export function ToolRunning(props: {
  input?: string
  streamingOutput?: string
  startTime?: number
  color?: RGBA
}) {
  return (
    <box gap={1}>
      <text>
        <ToolSpinner color={props.color} />
        <span>{props.input}</span>
        <Show when={props.startTime}>
          <ElapsedTime startTime={props.startTime!} />
        </Show>
      </text>
      <Show when={props.streamingOutput}>
        <text>{props.streamingOutput}</text>
      </Show>
    </box>
  )
}

// 5. Create resolved state component (success/error)
export function ToolResolved(props: {
  success: boolean
  input?: string
  output?: string
  executionTime?: number
}) {
  return (
    <box gap={1}>
      <text>
        <span>{props.success ? "✓" : "✗"}</span>
        <span>{props.input}</span>
        <Show when={props.executionTime}>
          <span>{formatTime(props.executionTime!)}</span>
        </Show>
      </text>
      <Show when={props.output}>
        <text>{props.output}</text>
      </Show>
    </box>
  )
}

// 6. Create main animation component that handles all states
export function ToolAnimation(props: {
  status: "pending" | "running" | "completed" | "error"
  input?: string
  output?: string
  streamingOutput?: string
  startTime?: number
  executionTime?: number
  color?: RGBA
}) {
  return (
    <Switch>
      <Match when={props.status === "pending"}>
        <ToolPending input={props.input} color={props.color} />
      </Match>
      <Match when={props.status === "running"}>
        <ToolRunning
          input={props.input}
          streamingOutput={props.streamingOutput}
          startTime={props.startTime}
          color={props.color}
        />
      </Match>
      <Match when={props.status === "completed"}>
        <ToolResolved
          success={true}
          input={props.input}
          output={props.output}
          executionTime={props.executionTime}
        />
      </Match>
      <Match when={props.status === "error"}>
        <ToolResolved
          success={false}
          input={props.input}
          output={props.output}
          executionTime={props.executionTime}
        />
      </Match>
    </Switch>
  )
}
```

#### Step 2: Export from `tool-animations.tsx`

Add the new component to the exports.

#### Step 3: Import in `session/index.tsx`

```typescript
import {
  // ... existing imports
  ToolAnimation, // Add your new component
} from "@tui/ui/tool-animations"
```

#### Step 4: Register the Tool with Custom Renderer

```typescript
ToolRegistry.register<typeof YourTool>({
  name: "yourtool",
  container: "block",  // or "inline"
  render(props) {
    // Extract streaming output from metadata
    const streamingOutput = createMemo(() => props.metadata.output?.trim() ?? "")

    // Extract final output (only available when completed)
    const finalOutput = createMemo(() => props.output ? props.output.trim() : undefined)

    // Calculate execution time
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    // Get start time for running state
    const startTime = createMemo(() => {
      if (props.state.status !== "pending" && props.state.time?.start) {
        return props.state.time.start
      }
      return undefined
    })

    return (
      <ToolCard status={props.state.status} inline={false}>
        <ToolAnimation
          status={props.state.status}
          input={props.input.yourInputField as string | undefined}
          output={finalOutput()}
          streamingOutput={streamingOutput()}
          startTime={startTime()}
          executionTime={executionTime()}
          color={TOOL_COLORS.yourtool}
        />
      </ToolCard>
    )
  },
})
```

### Key Points

1. **Input is always available**: `props.input` contains the tool parameters in ALL states (pending, running, completed, error)

2. **Streaming output via metadata**: During execution, streaming output comes from `props.metadata.output` (or tool-specific metadata fields)

3. **Final output only when completed**: `props.output` is only set when status is "completed"

4. **Time tracking**:
   - `props.state.time.start` - available in running/completed/error states
   - `props.state.time.end` - only available in completed/error states

5. **Use `createMemo` for derived values**: Extract values reactively so the UI updates as state changes

6. **Wrap in `ToolCard`**: Use the `ToolCard` component for consistent styling and status-based borders

### Example: Bash Tool Animation (Reference Implementation)

See the `BashToolAnimation` component in `tool-animations.tsx` for a complete example with:

- Custom breathing text animation
- Custom spinner
- Streaming command output
- Success/failure indicators
- Execution time display
