export * from "./client.js"
export * from "./server.js"
export * from "./spawn.js"

import { createOpencodeClient } from "./client.js"
import { createOpencodeServer } from "./server.js"
import type { ServerOptions } from "./server.js"
import { createSpawn, type SpawnHandle, type SpawnOptions, type SpawnResult, type SpawnEvent } from "./spawn.js"

export type { SpawnHandle, SpawnOptions, SpawnResult, SpawnEvent }

export async function createOpencode(options?: ServerOptions) {
  const server = await createOpencodeServer({
    ...options,
  })

  const client = createOpencodeClient({
    baseUrl: server.url,
  })

  const spawn = createSpawn(client)

  return {
    client,
    server,
    spawn,
  }
}
