import { spawn } from "node:child_process"
import { type Config } from "./gen/types.gen.js"

export type ServerOptions = {
  hostname?: string
  port?: number
  signal?: AbortSignal
  timeout?: number
  config?: Config
  /** Container profile to use - auto-starts if not running */
  profile?: string
}

export type TuiOptions = {
  project?: string
  model?: string
  session?: string
  agent?: string
  signal?: AbortSignal
  config?: Config
}

export async function createOpencodeServer(options?: ServerOptions) {
  options = Object.assign(
    {
      hostname: "127.0.0.1",
      port: 4096,
      timeout: 5000,
    },
    options ?? {},
  )

  // Extract sandbox config to pass via OPENCODE_SANDBOX (applied LAST, cannot be overridden by app config)
  const { sandbox, ...restConfig } = options.config ?? {}
  const env: Record<string, string> = {
    ...process.env as Record<string, string>,
    OPENCODE_CONFIG_CONTENT: JSON.stringify(restConfig),
  }
  
  // SDK sandbox config is passed separately and applied LAST (replaces, not merges)
  if (sandbox !== undefined) {
    env.OPENCODE_SANDBOX = JSON.stringify(sandbox)
  }

  const args = [`serve`, `--hostname=${options.hostname}`, `--port=${options.port}`]
  
  // Pass profile to server for auto-start
  if (options.profile) {
    args.push(`--profile=${options.profile}`)
  }
  
  const proc = spawn(`swarm`, args, {
    signal: options.signal,
    env,
  })

  const url = await new Promise<string>((resolve, reject) => {
    const id = setTimeout(() => {
      reject(new Error(`Timeout waiting for server to start after ${options.timeout}ms`))
    }, options.timeout)
    let output = ""
    proc.stdout?.on("data", (chunk) => {
      output += chunk.toString()
      const lines = output.split("\n")
      for (const line of lines) {
        if (line.startsWith("opencode server listening") || line.startsWith("swarm server listening")) {
          const match = line.match(/on\s+(https?:\/\/[^\s]+)/)
          if (!match) {
            throw new Error(`Failed to parse server url from output: ${line}`)
          }
          clearTimeout(id)
          resolve(match[1]!)
          return
        }
      }
    })
    proc.stderr?.on("data", (chunk) => {
      output += chunk.toString()
    })
    proc.on("exit", (code) => {
      clearTimeout(id)
      let msg = `Server exited with code ${code}`
      if (output.trim()) {
        msg += `\nServer output: ${output}`
      }
      reject(new Error(msg))
    })
    proc.on("error", (error) => {
      clearTimeout(id)
      reject(error)
    })
    if (options.signal) {
      options.signal.addEventListener("abort", () => {
        clearTimeout(id)
        reject(new Error("Aborted"))
      })
    }
  })

  return {
    url,
    close() {
      proc.kill()
    },
  }
}

export function createOpencodeTui(options?: TuiOptions) {
  const args = []

  if (options?.project) {
    args.push(`--project=${options.project}`)
  }
  if (options?.model) {
    args.push(`--model=${options.model}`)
  }
  if (options?.session) {
    args.push(`--session=${options.session}`)
  }
  if (options?.agent) {
    args.push(`--agent=${options.agent}`)
  }

  // Extract sandbox config to pass via OPENCODE_SANDBOX (applied LAST, cannot be overridden by app config)
  const { sandbox, ...restConfig } = options?.config ?? {}
  const env: Record<string, string> = {
    ...process.env as Record<string, string>,
    OPENCODE_CONFIG_CONTENT: JSON.stringify(restConfig),
  }
  
  // SDK sandbox config is passed separately and applied LAST (replaces, not merges)
  if (sandbox !== undefined) {
    env.OPENCODE_SANDBOX = JSON.stringify(sandbox)
  }

  const proc = spawn(`swarm`, args, {
    signal: options?.signal,
    stdio: "inherit",
    env,
  })

  return {
    close() {
      proc.kill()
    },
  }
}
