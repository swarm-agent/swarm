import { Log } from "../util/log"
import { NamedError } from "../util/error"
import { z } from "zod"
import type { Config } from "../config/config"

const log = Log.create({ service: "container-runtime" })

export namespace ContainerRuntime {
  export interface Options {
    runtime: "podman" | "docker"
    name: string
    image: string
    volumes: Array<Config.ContainerVolumeConfig>
    environment: Record<string, string>
    network: Config.ContainerNetworkConfig
    workdir: string
    user?: string
    entrypoint?: string[]
    command?: string[]
  }

  export interface ExecResult {
    exitCode: number
    stdout: string
    stderr: string
  }

  export interface ContainerInfo {
    id: string
    name: string
    image: string
    status: string
    running: boolean
    created: string
  }

  export const RuntimeNotFoundError = NamedError.create(
    "ContainerRuntimeNotFoundError",
    z.object({ runtime: z.string() })
  )
  export const ContainerNotFoundError = NamedError.create(
    "ContainerNotFoundError",
    z.object({ containerID: z.string() })
  )
  export const ExecError = NamedError.create(
    "ContainerExecError",
    z.object({ containerID: z.string(), command: z.string(), message: z.string() })
  )

  // Check if runtime is available
  export async function checkRuntime(runtime: "podman" | "docker"): Promise<boolean> {
    try {
      const proc = Bun.spawn([runtime, "--version"], {
        stdout: "pipe",
        stderr: "pipe",
      })
      await proc.exited
      return proc.exitCode === 0
    } catch {
      return false
    }
  }

  // Create a container (does not start it)
  export async function create(opts: Options): Promise<string> {
    const available = await checkRuntime(opts.runtime)
    if (!available) {
      throw new RuntimeNotFoundError({ runtime: opts.runtime })
    }

    const args: string[] = [
      "create",
      "--name", opts.name,
      "--workdir", opts.workdir,
      // Security: prevent privilege escalation
      "--security-opt", "no-new-privileges:true",
    ]

    // Add volumes
    for (const vol of opts.volumes) {
      const hostPath = expandPath(vol.host)
      const mode = vol.readonly ? "ro" : "rw"
      args.push("-v", `${hostPath}:${vol.container}:${mode}`)
    }

    // Add environment variables
    for (const [key, value] of Object.entries(opts.environment)) {
      args.push("-e", `${key}=${value}`)
    }

    // Network mode
    if (opts.network.mode === "none") {
      args.push("--network", "none")
    } else if (opts.network.mode === "host") {
      args.push("--network", "host")
    }
    // bridge is default (isolated, container can reach out but nothing can reach in)

    // Expose ports - ALWAYS bind to localhost only (127.0.0.1) for security
    // This prevents the container from being accessible from the network
    if (opts.network.exposePorts) {
      for (const port of opts.network.exposePorts) {
        args.push("-p", `127.0.0.1:${port}:${port}`)
      }
    }

    // Socket bridges - mount host sockets into container
    if (opts.network.socketBridges) {
      for (const [socketPath] of Object.entries(opts.network.socketBridges)) {
        args.push("-v", `${socketPath}:${socketPath}`)
      }
    }

    // User
    if (opts.user) {
      args.push("--user", opts.user)
    }

    // Keep container running with interactive tty
    args.push("-it")

    // Image
    args.push(opts.image)

    // Command to keep container alive
    args.push("/bin/sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done")

    log.info("creating container", { runtime: opts.runtime, name: opts.name, args })

    const proc = Bun.spawn([opts.runtime, ...args], {
      stdout: "pipe",
      stderr: "pipe",
    })

    const [stdout, stderr] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
    ])
    await proc.exited

    if (proc.exitCode !== 0) {
      throw new Error(`Failed to create container: ${stderr}`)
    }

    const containerID = stdout.trim()
    log.info("container created", { containerID })
    return containerID
  }

  // Start a container
  export async function start(containerID: string): Promise<void> {
    const runtime = await detectRuntime(containerID)
    
    const proc = Bun.spawn([runtime, "start", containerID], {
      stdout: "pipe",
      stderr: "pipe",
    })

    const stderr = await new Response(proc.stderr).text()
    await proc.exited

    if (proc.exitCode !== 0) {
      throw new Error(`Failed to start container: ${stderr}`)
    }

    log.info("container started", { containerID })
  }

  // Stop a container
  export async function stop(containerID: string, timeout: number = 10): Promise<void> {
    const runtime = await detectRuntime(containerID)

    const proc = Bun.spawn([runtime, "stop", "-t", timeout.toString(), containerID], {
      stdout: "pipe",
      stderr: "pipe",
    })

    await proc.exited
    log.info("container stopped", { containerID })
  }

  // Remove a container
  export async function remove(containerID: string): Promise<void> {
    const runtime = await detectRuntime(containerID)

    const proc = Bun.spawn([runtime, "rm", "-f", containerID], {
      stdout: "pipe",
      stderr: "pipe",
    })

    await proc.exited
    log.info("container removed", { containerID })
  }

  // Execute command in container
  export async function exec(containerID: string, command: string[]): Promise<ExecResult> {
    const runtime = await detectRuntime(containerID)

    const args = ["exec", containerID, ...command]
    log.debug("exec", { containerID, command })

    const proc = Bun.spawn([runtime, ...args], {
      stdout: "pipe",
      stderr: "pipe",
    })

    const [stdout, stderr] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
    ])
    const exitCode = await proc.exited

    return { exitCode, stdout, stderr }
  }

  // Execute command with streaming output
  export async function execStream(
    containerID: string,
    command: string[],
    onStdout?: (data: string) => void,
    onStderr?: (data: string) => void
  ): Promise<number> {
    const runtime = await detectRuntime(containerID)

    const args = ["exec", containerID, ...command]
    const proc = Bun.spawn([runtime, ...args], {
      stdout: "pipe",
      stderr: "pipe",
    })

    // Stream stdout
    if (onStdout && proc.stdout) {
      const reader = proc.stdout.getReader()
      const decoder = new TextDecoder()
      ;(async () => {
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          onStdout(decoder.decode(value))
        }
      })()
    }

    // Stream stderr
    if (onStderr && proc.stderr) {
      const reader = proc.stderr.getReader()
      const decoder = new TextDecoder()
      ;(async () => {
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          onStderr(decoder.decode(value))
        }
      })()
    }

    return proc.exited
  }

  // Check if container exists
  export async function exists(containerID: string): Promise<boolean> {
    // Try podman first, then docker
    for (const runtime of ["podman", "docker"] as const) {
      try {
        const proc = Bun.spawn([runtime, "inspect", containerID], {
          stdout: "pipe",
          stderr: "pipe",
        })
        await proc.exited
        if (proc.exitCode === 0) return true
      } catch {
        continue
      }
    }
    return false
  }

  // Get container info
  export async function inspect(containerID: string): Promise<ContainerInfo> {
    const runtime = await detectRuntime(containerID)

    const proc = Bun.spawn(
      [runtime, "inspect", "--format", "{{json .}}", containerID],
      { stdout: "pipe", stderr: "pipe" }
    )

    const stdout = await new Response(proc.stdout).text()
    await proc.exited

    if (proc.exitCode !== 0) {
      throw new ContainerNotFoundError({ containerID })
    }

    const data = JSON.parse(stdout)
    return {
      id: data.Id,
      name: data.Name?.replace(/^\//, "") ?? containerID,
      image: data.Config?.Image ?? data.Image ?? "",
      status: data.State?.Status ?? "unknown",
      running: data.State?.Running ?? false,
      created: data.Created ?? "",
    }
  }

  // Get container logs
  export async function* logs(
    containerID: string,
    opts?: { follow?: boolean; tail?: number; signal?: AbortSignal }
  ): AsyncIterable<string> {
    const runtime = await detectRuntime(containerID)

    const args = ["logs"]
    if (opts?.follow) args.push("-f")
    if (opts?.tail) args.push("--tail", opts.tail.toString())
    args.push(containerID)

    const proc = Bun.spawn([runtime, ...args], {
      stdout: "pipe",
      stderr: "pipe",
    })

    // Handle abort signal
    if (opts?.signal) {
      opts.signal.addEventListener("abort", () => {
        proc.kill()
      }, { once: true })
    }

    const reader = proc.stdout.getReader()
    const decoder = new TextDecoder()

    try {
      while (true) {
        if (opts?.signal?.aborted) break
        const { done, value } = await reader.read()
        if (done) break
        yield decoder.decode(value)
      }
    } finally {
      // Ensure process is killed when iteration ends
      proc.kill()
    }
  }

  // Pull an image
  export async function pull(runtime: "podman" | "docker", image: string): Promise<void> {
    log.info("pulling image", { runtime, image })

    const proc = Bun.spawn([runtime, "pull", image], {
      stdout: "pipe",
      stderr: "pipe",
    })

    await proc.exited

    if (proc.exitCode !== 0) {
      const stderr = await new Response(proc.stderr).text()
      throw new Error(`Failed to pull image: ${stderr}`)
    }

    log.info("image pulled", { image })
  }

  // List containers by name prefix
  export async function listByPrefix(
    runtime: "podman" | "docker",
    prefix: string
  ): Promise<ContainerInfo[]> {
    const proc = Bun.spawn(
      [runtime, "ps", "-a", "--filter", `name=${prefix}`, "--format", "{{json .}}"],
      { stdout: "pipe", stderr: "pipe" }
    )

    const stdout = await new Response(proc.stdout).text()
    await proc.exited

    const containers: ContainerInfo[] = []
    for (const line of stdout.trim().split("\n")) {
      if (!line) continue
      try {
        const data = JSON.parse(line)
        containers.push({
          id: data.ID ?? data.Id,
          name: data.Names ?? data.Name ?? "",
          image: data.Image ?? "",
          status: data.Status ?? "",
          running: data.State === "running" || data.Status?.includes("Up"),
          created: data.CreatedAt ?? data.Created ?? "",
        })
      } catch {
        continue
      }
    }

    return containers
  }

  // Helper: detect which runtime manages a container
  async function detectRuntime(containerID: string): Promise<"podman" | "docker"> {
    for (const runtime of ["podman", "docker"] as const) {
      try {
        const proc = Bun.spawn([runtime, "inspect", containerID], {
          stdout: "pipe",
          stderr: "pipe",
        })
        await proc.exited
        if (proc.exitCode === 0) return runtime
      } catch {
        continue
      }
    }
    throw new ContainerNotFoundError({ containerID })
  }

  // Helper: expand ~ and . in paths
  function expandPath(p: string): string {
    if (p.startsWith("~/")) {
      return p.replace("~", Bun.env.HOME ?? "")
    }
    if (p === "." || p.startsWith("./")) {
      return process.cwd() + p.slice(1)
    }
    return p
  }
}
