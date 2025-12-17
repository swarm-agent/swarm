import { spawn, execSync } from "node:child_process"
import { existsSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"
import { createOpencodeClient } from "./client.js"
import { createSpawn } from "./spawn.js"

export type ContainerOptions = {
  /** Unique name for this container */
  name: string
  /** Workspace directory to mount at /workspace */
  workspace: string
  /** Additional volumes to mount */
  volumes?: Array<{ host: string; container: string; readonly?: boolean }>
  /** Port to expose (0 = auto-assign) */
  port?: number
  /** Container runtime: podman (default) or docker */
  runtime?: "podman" | "docker"
  /** Swarm CLI directory (default: auto-detect) */
  swarmDir?: string
  /** Auth file path (default: ~/.local/share/opencode/auth.json) */
  authFile?: string
  /** Config directory (default: swarmDir/.opencode) */
  configDir?: string
  /** Image to use (default: docker.io/oven/bun:1.3) */
  image?: string
  /** System prompt for all sessions */
  system?: string
}

export type ContainerHandle = {
  /** Container name */
  name: string
  /** Container ID */
  id: string
  /** API URL */
  url: string
  /** Port number */
  port: number
  /** API client */
  client: ReturnType<typeof createOpencodeClient>
  /** Spawn function for creating sessions */
  spawn: ReturnType<typeof createSpawn>
  /** Stop and remove the container */
  stop: () => Promise<void>
  /** Check if container is running */
  isRunning: () => boolean
}

// Track used ports to avoid conflicts
const usedPorts = new Set<number>()

function findAvailablePort(start: number = 4200): number {
  let port = start
  while (usedPorts.has(port) || isPortInUse(port)) {
    port++
    if (port > 65535) throw new Error("No available ports")
  }
  usedPorts.add(port)
  return port
}

function isPortInUse(port: number): boolean {
  try {
    execSync(`ss -tln | grep -q ':${port} '`, { stdio: "ignore" })
    return true
  } catch {
    return false
  }
}

function detectSwarmDir(): string {
  // Check common locations
  const candidates = [
    join(homedir(), "swarm-cli"),
    join(homedir(), ".local/share/swarm-cli"),
    "/opt/swarm-cli",
  ]

  for (const dir of candidates) {
    const binPath = join(dir, "packages/opencode/dist/swarm-linux-x64/bin/swarm")
    if (existsSync(binPath)) {
      return dir
    }
  }

  throw new Error(
    "Could not find swarm-cli directory. Please specify swarmDir option or set SWARM_CLI_DIR env var"
  )
}

function detectRuntime(): "podman" | "docker" {
  try {
    execSync("podman --version", { stdio: "ignore" })
    return "podman"
  } catch {
    try {
      execSync("docker --version", { stdio: "ignore" })
      return "docker"
    } catch {
      throw new Error("Neither podman nor docker found. Please install one of them.")
    }
  }
}

export async function spawnContainer(options: ContainerOptions): Promise<ContainerHandle> {
  const runtime = options.runtime ?? detectRuntime()
  const swarmDir = options.swarmDir ?? process.env.SWARM_CLI_DIR ?? detectSwarmDir()
  const authFile = options.authFile ?? join(homedir(), ".local/share/opencode/auth.json")
  const configDir = options.configDir ?? join(swarmDir, ".opencode")
  const image = options.image ?? "docker.io/oven/bun:1.3"
  const port = options.port === 0 || options.port === undefined ? findAvailablePort() : options.port
  const containerName = `swarm-${options.name}`

  // Validate paths
  if (!existsSync(options.workspace)) {
    throw new Error(`Workspace does not exist: ${options.workspace}`)
  }
  if (!existsSync(authFile)) {
    throw new Error(`Auth file not found: ${authFile}. Run 'swarm auth' first.`)
  }
  if (!existsSync(swarmDir)) {
    throw new Error(`Swarm directory not found: ${swarmDir}`)
  }

  const binPath = join(swarmDir, "packages/opencode/dist/swarm-linux-x64/bin/swarm")
  if (!existsSync(binPath)) {
    throw new Error(`Swarm binary not found: ${binPath}. Build it first with ./build.sh`)
  }

  // Check if container already exists
  try {
    execSync(`${runtime} inspect ${containerName}`, { stdio: "ignore" })
    throw new Error(`Container ${containerName} already exists. Stop it first or use a different name.`)
  } catch (e: any) {
    if (e.message?.includes("already exists")) throw e
    // Container doesn't exist, good
  }

  // Build volume mounts
  const volumeArgs: string[] = [
    `-v`, `${options.workspace}:/workspace:rw`,
    `-v`, `${swarmDir}:/swarm:ro`,
    `-v`, `${authFile}:/root/.local/share/opencode/auth.json:ro`,
  ]

  // Add additional volumes
  for (const vol of options.volumes ?? []) {
    const mode = vol.readonly ? "ro" : "rw"
    volumeArgs.push(`-v`, `${vol.host}:${vol.container}:${mode}`)
  }

  // Build the command
  const args = [
    "run", "-d",
    "--name", containerName,
    "-p", `127.0.0.1:${port}:4096`,
    ...volumeArgs,
    "-e", `OPENCODE_CONFIG_DIR=/swarm/.opencode`,
    "-w", "/workspace",
    image,
    "/swarm/packages/opencode/dist/swarm-linux-x64/bin/swarm",
    "serve",
    "--port", "4096",
    "--hostname", "0.0.0.0",
  ]

  // Spawn container
  const result = execSync(`${runtime} ${args.join(" ")}`, { encoding: "utf-8" })
  const containerId = result.trim()

  // Wait for server to be ready
  const url = `http://127.0.0.1:${port}`
  await waitForServer(url, 10000)

  const client = createOpencodeClient({ baseUrl: url })
  const spawnFn = createSpawn(client, { system: options.system })

  const handle: ContainerHandle = {
    name: containerName,
    id: containerId,
    url,
    port,
    client,
    spawn: spawnFn,
    stop: async () => {
      try {
        execSync(`${runtime} rm -f ${containerName}`, { stdio: "ignore" })
        usedPorts.delete(port)
      } catch {
        // Already stopped
      }
    },
    isRunning: () => {
      try {
        const status = execSync(`${runtime} inspect -f '{{.State.Running}}' ${containerName}`, {
          encoding: "utf-8",
        }).trim()
        return status === "true"
      } catch {
        return false
      }
    },
  }

  return handle
}

async function waitForServer(url: string, timeoutMs: number): Promise<void> {
  const start = Date.now()
  while (Date.now() - start < timeoutMs) {
    try {
      const response = await fetch(`${url}/session`)
      if (response.ok) return
    } catch {
      // Not ready yet
    }
    await new Promise((r) => setTimeout(r, 200))
  }
  throw new Error(`Server at ${url} did not become ready within ${timeoutMs}ms`)
}

export function listContainers(runtime?: "podman" | "docker"): Array<{
  name: string
  id: string
  status: string
  port: number | null
}> {
  const rt = runtime ?? detectRuntime()
  try {
    const output = execSync(
      `${rt} ps -a --filter "name=swarm-" --format '{{.Names}}\t{{.ID}}\t{{.Status}}\t{{.Ports}}'`,
      { encoding: "utf-8" }
    )

    return output
      .trim()
      .split("\n")
      .filter((line) => line.trim())
      .map((line) => {
        const [name, id, status, ports] = line.split("\t")
        const portMatch = ports?.match(/:(\d+)->/)
        return {
          name: name ?? "",
          id: id ?? "",
          status: status ?? "",
          port: portMatch ? parseInt(portMatch[1], 10) : null,
        }
      })
  } catch {
    return []
  }
}

export function stopContainer(name: string, runtime?: "podman" | "docker"): void {
  const rt = runtime ?? detectRuntime()
  const containerName = name.startsWith("swarm-") ? name : `swarm-${name}`
  execSync(`${rt} rm -f ${containerName}`, { stdio: "ignore" })
}

export function stopAllContainers(runtime?: "podman" | "docker"): void {
  const rt = runtime ?? detectRuntime()
  const containers = listContainers(rt)
  for (const c of containers) {
    execSync(`${rt} rm -f ${c.name}`, { stdio: "ignore" })
  }
}

/**
 * Subscribe to SSE events from a container and optionally auto-approve permissions
 */
export async function* subscribeEvents(
  url: string,
  options?: { autoApprove?: boolean; sessionFilter?: string }
): AsyncGenerator<ContainerEvent> {
  const response = await fetch(`${url}/event`, {
    headers: { Accept: "text/event-stream" },
  })

  if (!response.ok) {
    throw new Error(`Failed to connect to event stream: ${response.status}`)
  }

  const reader = response.body?.getReader()
  if (!reader) throw new Error("No response body")

  const decoder = new TextDecoder()
  let buffer = ""

  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split("\n")
      buffer = lines.pop() ?? ""

      for (const line of lines) {
        if (line.startsWith("data: ")) {
          try {
            const event = JSON.parse(line.slice(6)) as ContainerEvent

            // Auto-approve permissions if enabled
            if (options?.autoApprove && event.type === "permission.updated") {
              const sessionID = event.properties?.sessionID
              const permissionID = event.properties?.id
              if (sessionID && permissionID) {
                // Skip if sessionFilter is set and doesn't match
                if (options.sessionFilter && sessionID !== options.sessionFilter) {
                  continue
                }
                // Approve the permission
                await fetch(`${url}/session/${sessionID}/permissions/${permissionID}`, {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ response: "once" }),
                })
              }
            }

            yield event
          } catch {
            // Skip malformed events
          }
        }
      }
    }
  } finally {
    reader.releaseLock()
  }
}

export type ContainerEvent = {
  type: string
  properties?: Record<string, any>
}

/**
 * Send a message to a session and stream the response with auto-approval
 */
export async function sendMessage(
  url: string,
  sessionId: string,
  message: string,
  options?: { autoApprove?: boolean; onText?: (text: string) => void; timeout?: number }
): Promise<{ success: boolean; text: string }> {
  const timeout = options?.timeout ?? 120000 // 2 minutes default

  // Connect to SSE BEFORE sending the message to avoid race conditions
  const response = await fetch(`${url}/event`, {
    headers: { Accept: "text/event-stream" },
  })

  if (!response.ok) {
    return { success: false, text: `Failed to connect to event stream: ${response.status}` }
  }

  const reader = response.body?.getReader()
  if (!reader) {
    return { success: false, text: "No response body from event stream" }
  }

  // Now send the message (SSE connection is already established)
  const sendResponse = await fetch(`${url}/session/${sessionId}/message`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      parts: [{ type: "text", text: message }],
    }),
  })

  if (!sendResponse.ok) {
    reader.releaseLock()
    return { success: false, text: `Failed to send message: ${sendResponse.status}` }
  }

  // Listen for completion
  let fullText = ""
  let messageStarted = false
  const startTime = Date.now()
  const decoder = new TextDecoder()
  let buffer = ""

  try {
    while (true) {
      // Check timeout
      if (Date.now() - startTime > timeout) {
        return { success: false, text: fullText || "Timeout waiting for response" }
      }

      const { done, value } = await reader.read()
      if (done) break

      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split("\n")
      buffer = lines.pop() ?? ""

      for (const line of lines) {
        if (line.startsWith("data: ")) {
          try {
            const event = JSON.parse(line.slice(6)) as ContainerEvent

            // Auto-approve permissions
            if (options?.autoApprove !== false && event.type === "permission.updated") {
              const permSessionID = event.properties?.sessionID
              const permissionID = event.properties?.id
              if (permSessionID === sessionId && permissionID) {
                await fetch(`${url}/session/${sessionId}/permissions/${permissionID}`, {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ response: "once" }),
                })
              }
            }

            // Track when a message actually starts processing
            // sessionID is nested in event.properties.info for message.updated
            if (event.type === "message.updated" && event.properties?.info?.sessionID === sessionId) {
              messageStarted = true
            }

            // Capture text from assistant messages
            // For message.part.updated, data is in event.properties.part
            if (event.type === "message.part.updated" && event.properties?.part?.sessionID === sessionId) {
              if (event.properties?.part?.type === "text") {
                fullText = event.properties.part.text ?? ""
                options?.onText?.(fullText)
              }
            }

            // Only complete after message has started
            if (messageStarted && event.type === "session.completed" && event.properties?.sessionID === sessionId) {
              return { success: true, text: fullText }
            }

            // Session became idle after message (means it completed)
            if (messageStarted && event.type === "session.idle" && event.properties?.sessionID === sessionId) {
              return { success: true, text: fullText }
            }

            if (event.type === "session.error" && event.properties?.sessionID === sessionId) {
              return { success: false, text: event.properties.error?.message ?? "Unknown error" }
            }
          } catch {
            // Skip malformed events
          }
        }
      }
    }
  } finally {
    reader.releaseLock()
  }

  return { success: false, text: fullText }
}
