# swarm-cli

## Build

```bash
bun install
./build.sh
```

Binary output: `packages/swarm/dist/swarm-linux-x64/bin/swarm`

## Configuration

Config location: `.swarm/swarm.json`

```json
{
  "model": "anthropic/claude-sonnet-4-5",
  "sandbox": {
    "enabled": true
  }
}
```

## SDK Usage

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

const { spawn, server } = await createSwarm()

const handle = spawn("List all TypeScript files")
await handle.wait()

server.close()
```
