import { z } from "zod"
import path from "path"
import { Global } from "../global"
import { Log } from "../util/log"
import { Bus } from "../bus"
import { NamedError } from "../util/error"
import { Config } from "../config/config"
import { ContainerRuntime } from "../container/runtime"

const log = Log.create({ service: "profile" })

export namespace Profile {
  // Profile state stored on disk
  export const Info = z.object({
    name: z.string(),
    config: Config.ContainerProfileConfig,
    containerID: z.string().optional(),
    status: z.enum(["stopped", "starting", "running", "error"]),
    lastStarted: z.number().optional(),
    lastActivity: z.number().optional(),
    sessionCount: z.number().default(0),
    error: z.string().optional(),
  })
  export type Info = z.infer<typeof Info>

  // Events
  export const Event = {
    Created: Bus.event("profile.created", z.object({ profile: Info })),
    Updated: Bus.event("profile.updated", z.object({ profile: Info })),
    Deleted: Bus.event("profile.deleted", z.object({ name: z.string() })),
    Starting: Bus.event("profile.starting", z.object({ name: z.string() })),
    Started: Bus.event("profile.started", z.object({ name: z.string(), containerID: z.string() })),
    Stopping: Bus.event("profile.stopping", z.object({ name: z.string() })),
    Stopped: Bus.event("profile.stopped", z.object({ name: z.string() })),
    Error: Bus.event("profile.error", z.object({ name: z.string(), error: z.string() })),
  }

  // Errors
  export const NotFoundError = NamedError.create("ProfileNotFoundError", z.object({ name: z.string() }))
  export const AlreadyExistsError = NamedError.create("ProfileAlreadyExistsError", z.object({ name: z.string() }))
  export const RuntimeError = NamedError.create("ProfileRuntimeError", z.object({ name: z.string(), message: z.string() }))

  // Storage path
  function profilePath(name: string): string {
    return path.join(Global.Path.data, "profiles", `${name}.json`)
  }

  function profilesDir(): string {
    return path.join(Global.Path.data, "profiles")
  }

  // CRUD Operations
  export async function create(config: Config.ContainerProfileConfig): Promise<Info> {
    const existing = await get(config.name)
    if (existing) {
      throw new AlreadyExistsError({ name: config.name })
    }

    const info: Info = {
      name: config.name,
      config,
      status: "stopped",
      sessionCount: 0,
    }

    await Bun.write(profilePath(config.name), JSON.stringify(info, null, 2))
    log.info("profile created", { name: config.name })
    Bus.publish(Event.Created, { profile: info })
    return info
  }

  export async function get(name: string): Promise<Info | undefined> {
    try {
      const text = await Bun.file(profilePath(name)).text()
      const data = JSON.parse(text)
      return Info.parse(data)
    } catch (err: any) {
      if (err.code === "ENOENT") return undefined
      throw err
    }
  }

  export async function list(): Promise<Info[]> {
    const dir = profilesDir()
    const profiles: Info[] = []

    try {
      const glob = new Bun.Glob("*.json")
      for await (const file of glob.scan({ cwd: dir, absolute: true })) {
        try {
          const text = await Bun.file(file).text()
          const data = JSON.parse(text)
          profiles.push(Info.parse(data))
        } catch {
          // Skip invalid files
        }
      }
    } catch {
      // Directory doesn't exist yet
    }

    return profiles
  }

  export async function update(name: string, config: Partial<Config.ContainerProfileConfig>): Promise<Info> {
    const existing = await get(name)
    if (!existing) {
      throw new NotFoundError({ name })
    }

    const updated: Info = {
      ...existing,
      config: { ...existing.config, ...config, name }, // name can't change
    }

    await Bun.write(profilePath(name), JSON.stringify(updated, null, 2))
    log.info("profile updated", { name })
    Bus.publish(Event.Updated, { profile: updated })
    return updated
  }

  export async function remove(name: string): Promise<void> {
    const existing = await get(name)
    if (!existing) {
      throw new NotFoundError({ name })
    }

    // Stop container if running
    if (existing.status === "running" && existing.containerID) {
      await stop(name)
    }

    const fs = await import("fs/promises")
    await fs.unlink(profilePath(name))
    log.info("profile deleted", { name })
    Bus.publish(Event.Deleted, { name })
  }

  // Container Lifecycle
  export async function start(name: string): Promise<string> {
    const profile = await get(name)
    if (!profile) {
      throw new NotFoundError({ name })
    }

    if (profile.status === "running" && profile.containerID) {
      // Check if container is actually running
      const exists = await ContainerRuntime.exists(profile.containerID)
      if (exists) {
        log.info("container already running", { name, containerID: profile.containerID })
        return profile.containerID
      }
    }

    Bus.publish(Event.Starting, { name })
    await updateStatus(name, "starting")

    try {
      const config = await Config.get()
      const runtime = config.container?.runtime ?? "podman"

      const containerID = await ContainerRuntime.create({
        runtime,
        name: `swarm-${name}-${Date.now().toString(36)}`,
        image: profile.config.image,
        volumes: profile.config.volumes ?? [],
        environment: profile.config.environment ?? {},
        network: profile.config.network ?? { mode: "bridge" },
        workdir: profile.config.workdir ?? "/workspace",
        user: profile.config.user,
      })

      await ContainerRuntime.start(containerID)

      const updated = await updateStatus(name, "running", containerID)
      log.info("container started", { name, containerID })
      Bus.publish(Event.Started, { name, containerID })

      return containerID
    } catch (err: any) {
      await updateStatus(name, "error", undefined, err.message)
      Bus.publish(Event.Error, { name, error: err.message })
      throw new RuntimeError({ name, message: err.message })
    }
  }

  export async function stop(name: string): Promise<void> {
    const profile = await get(name)
    if (!profile) {
      throw new NotFoundError({ name })
    }

    if (!profile.containerID) {
      log.info("no container to stop", { name })
      return
    }

    Bus.publish(Event.Stopping, { name })

    try {
      await ContainerRuntime.stop(profile.containerID)
      await ContainerRuntime.remove(profile.containerID)
    } catch {
      // Container may already be stopped
    }

    await updateStatus(name, "stopped", undefined)
    log.info("container stopped", { name })
    Bus.publish(Event.Stopped, { name })
  }

  export async function restart(name: string): Promise<string> {
    await stop(name)
    return start(name)
  }

  export async function status(name: string): Promise<{
    status: Info["status"]
    containerID?: string
    running: boolean
  }> {
    const profile = await get(name)
    if (!profile) {
      throw new NotFoundError({ name })
    }

    let running = false
    if (profile.containerID) {
      running = await ContainerRuntime.exists(profile.containerID)
      if (!running && profile.status === "running") {
        // Container died, update status
        await updateStatus(name, "stopped", undefined)
      }
    }

    return {
      status: profile.status,
      containerID: profile.containerID,
      running,
    }
  }

  export async function exec(name: string, command: string[]): Promise<ContainerRuntime.ExecResult> {
    const profile = await get(name)
    if (!profile) {
      throw new NotFoundError({ name })
    }

    if (!profile.containerID || profile.status !== "running") {
      throw new RuntimeError({ name, message: "Container not running" })
    }

    // Update activity
    await updateActivity(name)

    return ContainerRuntime.exec(profile.containerID, command)
  }

  // Helpers
  async function updateStatus(
    name: string,
    status: Info["status"],
    containerID?: string,
    error?: string
  ): Promise<Info> {
    const profile = await get(name)
    if (!profile) {
      throw new NotFoundError({ name })
    }

    const updated: Info = {
      ...profile,
      status,
      containerID,
      error,
      lastStarted: status === "running" ? Date.now() : profile.lastStarted,
    }

    await Bun.write(profilePath(name), JSON.stringify(updated, null, 2))
    return updated
  }

  async function updateActivity(name: string): Promise<void> {
    const profile = await get(name)
    if (!profile) return

    const updated: Info = {
      ...profile,
      lastActivity: Date.now(),
    }

    await Bun.write(profilePath(name), JSON.stringify(updated, null, 2))
  }

  // Initialize from config - create profiles defined in config if they don't exist
  export async function initFromConfig(): Promise<void> {
    const config = await Config.get()
    if (!config.container?.profiles) return

    for (const [name, profileConfig] of Object.entries(config.container.profiles)) {
      const existing = await get(name)
      if (!existing) {
        await create({ ...profileConfig, name })
        log.info("created profile from config", { name })
      }
    }
  }
}
