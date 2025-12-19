# Swarm CLI

AI-powered terminal for developers. This file provides guidance for AI agents working on this codebase.

## Quick Reference

| Command | Description |
|---------|-------------|
| `bun install` | Install dependencies |
| `./build.sh` | Build binary |
| `bun test` | Run all tests |
| `bun run typecheck` | Type check |

Binary output: `packages/swarm/dist/swarm-<platform>/bin/swarm`

## Project Structure

```
packages/
├── swarm/          # Main CLI application
├── sdk/js/         # JavaScript/TypeScript SDK
├── plugin/         # Plugin system
└── script/         # Build utilities
```

## Configuration

Config file: `.swarm/swarm.json`

```json
{
  "model": "anthropic/claude-sonnet-4-5",
  "sandbox": {
    "enabled": true
  }
}
```

## SDK (Important!)

The SDK allows programmatic control of Swarm sessions:

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

// Create a swarm instance
const { spawn, server } = await createSwarm()

// Spawn an agent session
const handle = spawn("List all TypeScript files")
await handle.wait()

// Clean up
server.close()
```

### Custom Tools

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

const myTool = tool({
  name: "greet",
  description: "Greet a user",
  parameters: z.object({
    name: z.string().describe("Name to greet"),
  }),
  execute: async ({ name }) => {
    return `Hello, ${name}!`
  },
})

const { spawn, server } = await createSwarm({
  tools: [myTool],
})
```

## Development

See `packages/swarm/AGENTS.md` for detailed development guidelines including:
- Code style and conventions
- Architecture patterns
- Tool animation system
- Testing patterns
