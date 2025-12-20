# Swarm CLI

AI-powered terminal for developers. This file provides guidance for AI agents working on this codebase.

## Quick Reference

| Command | Description |
|---------|-------------|
| `bun install` | Install dependencies |
| `./build.sh` | Build binary (auto-installs to ~/.local/bin) |
| `bun test` | Run all tests |
| `bun test test/path.test.ts` | Run single test file |
| `bun run typecheck` | Type check all packages (via turbo) |
| `bun dev` | Run development server |
| `./clean.sh` | Clean all build artifacts |
| `./create-release.sh` | Create GitHub release with platform binaries |

Binary output: `packages/swarm/dist/swarm-<platform>/bin/swarm`

## Project Structure

```
packages/
├── swarm/          # Main CLI application (Bun + TypeScript + SolidJS TUI)
│   ├── src/
│   │   ├── cli/cmd/tui/   # Terminal UI components (SolidJS + @opentui)
│   │   ├── tool/          # Tool implementations (bash, read, write, etc.)
│   │   ├── session/       # Session management, prompts
│   │   ├── config/        # Configuration loading
│   │   ├── server/        # HTTP API server
│   │   └── provider/      # LLM provider integrations
│   └── test/              # Test files (bun test)
├── sdk/js/         # JavaScript/TypeScript SDK (@swarm-ai/sdk)
├── plugin/         # Plugin system (@swarm-ai/plugin)
└── script/         # Build utilities (@swarm-ai/script)
```

## Configuration

Config file: `.swarm/swarm.json` or `.swarm/swarm.jsonc`

```json
{
  "model": "anthropic/claude-sonnet-4-5",
  "sandbox": {
    "enabled": true
  }
}
```

Config is loaded from multiple sources (merged in order):
1. Global config: `~/.config/swarm/swarm.json`
2. Project config: `.swarm/swarm.json` (searched up from cwd)
3. `SWARM_CONFIG` env var (path to custom config)
4. `SWARM_CONFIG_CONTENT` env var (inline JSON)

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

## Code Style

- **Runtime**: Bun with TypeScript ESM modules
- **UI Framework**: SolidJS with @opentui for terminal rendering
- **Imports**: Relative imports for local modules, path aliases `@/*` and `@tui/*`
- **Types**: Zod schemas for validation, TypeScript interfaces for structure
- **Naming**: camelCase for variables/functions, PascalCase for classes/namespaces
- **Error handling**: Use Result patterns, avoid throwing exceptions in tools
- **File structure**: Namespace-based organization (e.g., `Tool.define()`, `Session.create()`)
- **Logging**: Use `Log.create({ service: "name" })` pattern

## Architecture Patterns

### Tool Definition

Tools implement the `Tool.Info` interface:

```typescript
import { Tool } from "../tool/tool"
import z from "zod"

export const MyTool = Tool.define<z.ZodObject<...>, MetadataType>(
  "mytool",
  {
    description: "What this tool does",
    parameters: z.object({
      param: z.string().describe("Parameter description"),
    }),
    async execute(args, ctx) {
      // ctx.sessionID, ctx.abort, ctx.metadata() available
      return {
        title: "Result title",
        metadata: { /* streaming metadata */ },
        output: "Result output",
      }
    },
  }
)
```

### Context & DI

- Pass `sessionID` in tool context
- Use `App.provide()` for dependency injection
- Storage via `Storage` namespace for persistence

### API Server

The TypeScript server (`packages/swarm/src/server/`) exposes HTTP endpoints. When modifying endpoints, regenerate the client SDK.

## Testing

```bash
# Run all tests
bun test

# Run specific test file
bun test packages/swarm/test/tool/bash.test.ts

# Run tests matching pattern
bun test --grep "pattern"
```

Test files use `.test.ts` extension and are located in `packages/swarm/test/`.

## Key Files

| File | Purpose |
|------|---------|
| `packages/swarm/src/index.ts` | CLI entry point |
| `packages/swarm/src/tool/registry.ts` | Tool registration |
| `packages/swarm/src/config/config.ts` | Config loading logic |
| `packages/swarm/src/cli/cmd/tui/app.tsx` | TUI application root |
| `packages/sdk/js/src/index.ts` | SDK entry point |

## Development

See `packages/swarm/AGENTS.md` for detailed development guidelines including:
- Tool animation system (custom spinners, streaming output)
- TUI component patterns
- State management

## Session Log

| Date | Summary |
|------|---------|
| 2024-12-20 | Simplified background agent indicator to global muted display |
| 2024-12-20 | Use useRoute() directly in BackgroundAgentsBlock for proper SolidJS reactivity |
| 2024-12-20 | Fixed SolidJS reactivity for background agent session filtering in status indicator |
| 2024-12-20 | Fixed background agent indicator to require exact session ID match |
| 2024-12-20 | Added create-release.sh script for GitHub releases with multi-platform binaries |
| 2024-12-20 | Simplified status bar indicator back to icon + count (reverted complex display) |
| 2024-12-20 | Added background agents section to sidebar showing child agents tied to parent session |
| 2024-12-20 | Enhanced background agent indicator with description and completion state display |
| 2024-12-20 | Config now prefers .swarm/ over .opencode/, legacy paths used as fallback only |
| 2024-12-20 | Moved Memory.init() to InstanceBootstrap for proper Bus scope |

## Notes

- Memory tool creates sections if they don't exist
- TypeScript native preview (`tsgo`) used for type checking
- Bun preloads `@opentui/solid/preload` for JSX support
- Config supports JSONC (JSON with comments)
- Memory.init() must be called in InstanceBootstrap (not server.ts) to ensure proper Bus scope
