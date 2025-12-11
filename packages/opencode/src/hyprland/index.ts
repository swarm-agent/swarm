import fs from "fs/promises"
import path from "path"
import os from "os"
import { Log } from "../util/log"

const log = Log.create({ service: "hyprland" })

const HYPR_SOCKET_DIR = `/run/user/${process.getuid?.() ?? 1000}/hypr`

/**
 * Find the active Hyprland instance signature by checking which socket dir
 * has a valid .socket.sock file. Picks the newest one if multiple exist.
 */
async function findHyprlandInstance(): Promise<string | null> {
  try {
    const entries = await fs.readdir(HYPR_SOCKET_DIR)
    if (entries.length === 0) return null

    // Sort by the timestamp in the directory name (second number after underscore)
    // Format: <signature>_<timestamp1>_<timestamp2>
    const sorted = entries.sort((a, b) => {
      const aMatch = a.match(/_(\d+)_(\d+)$/)
      const bMatch = b.match(/_(\d+)_(\d+)$/)
      const aTime = aMatch ? parseInt(aMatch[1]) : 0
      const bTime = bMatch ? parseInt(bMatch[1]) : 0
      return bTime - aTime // newest first
    })

    // Check each one for a valid socket
    for (const dir of sorted) {
      const socketPath = path.join(HYPR_SOCKET_DIR, dir, ".socket.sock")
      try {
        const stat = await fs.stat(socketPath)
        if (stat.isSocket()) {
          return dir
        }
      } catch {
        // Socket doesn't exist, try next
      }
    }
    return null
  } catch {
    return null
  }
}

/**
 * Get env with correct HYPRLAND_INSTANCE_SIGNATURE
 */
async function getHyprEnv(): Promise<Record<string, string> | null> {
  const instance = await findHyprlandInstance()
  if (!instance) return null
  return {
    ...process.env,
    HYPRLAND_INSTANCE_SIGNATURE: instance,
  } as Record<string, string>
}

export type SessionStatus = "idle" | "working" | "blocked"

export interface SessionEntry {
  pid: number
  cwd: string
  hyprWorkspace: number | null
  hyprWindowPid: number | null
  status: SessionStatus
  startedAt: number
  lastUpdated: number
  // Rich session info
  sessionId?: string
  agent?: string
  model?: string
  // Remote session (detected from terminal title)
  remote?: boolean
}

const SESSIONS_DIR = path.join(os.homedir(), ".config", "swarm")
const SESSIONS_FILE = path.join(SESSIONS_DIR, "sessions.json")

// Current session info stored in memory
let currentSession: {
  pid: number
  cwd: string
  hyprWorkspace: number | null
  hyprWindowPid: number | null
} | null = null

async function ensureDir() {
  await fs.mkdir(SESSIONS_DIR, { recursive: true })
}

async function readSessions(): Promise<SessionEntry[]> {
  try {
    const content = await fs.readFile(SESSIONS_FILE, "utf-8")
    const data = JSON.parse(content)
    return Array.isArray(data.sessions) ? data.sessions : []
  } catch {
    return []
  }
}

async function writeSessions(sessions: SessionEntry[]) {
  try {
    await ensureDir()
    const tmpFile = `${SESSIONS_FILE}.tmp.${process.pid}.${Date.now()}`
    await fs.writeFile(tmpFile, JSON.stringify({ sessions }, null, 2))
    await fs.rename(tmpFile, SESSIONS_FILE)
  } catch (err) {
    // Race condition or write failure - log but don't crash
    log.info("writeSessions failed (race condition)", { error: String(err) })
  }
}

async function getHyprlandInfo(): Promise<{ workspace: number; windowPid: number } | null> {
  try {
    const env = await getHyprEnv()
    if (!env) return null

    const proc = Bun.spawn(["hyprctl", "activewindow", "-j"], {
      stdout: "pipe",
      stderr: "pipe",
      env,
    })
    const output = await new Response(proc.stdout).text()
    await proc.exited
    if (proc.exitCode !== 0) return null

    const data = JSON.parse(output)
    if (data.workspace?.id && data.pid) {
      return {
        workspace: data.workspace.id,
        windowPid: data.pid,
      }
    }
    return null
  } catch {
    return null
  }
}

/**
 * Get all Hyprland clients (windows) and their workspaces
 * Returns a Map of windowPid -> workspaceId
 */
async function getHyprlandClients(): Promise<Map<number, number>> {
  const clients = new Map<number, number>()
  try {
    const env = await getHyprEnv()
    if (!env) return clients

    const proc = Bun.spawn(["hyprctl", "clients", "-j"], {
      stdout: "pipe",
      stderr: "pipe",
      env,
    })
    const output = await new Response(proc.stdout).text()
    await proc.exited
    if (proc.exitCode !== 0) return clients

    const data = JSON.parse(output)
    if (Array.isArray(data)) {
      for (const client of data) {
        if (client.pid && client.workspace?.id) {
          clients.set(client.pid, client.workspace.id)
        }
      }
    }
  } catch {
    // Hyprland not available
  }
  return clients
}

/**
 * Parse a swarm terminal title into session info
 * Format: swarm:<status>:<agent>:<context>:<cwd>
 */
function parseSwarmTitle(title: string): { status: SessionStatus; agent?: string; context?: string; cwd?: string } | null {
  if (!title.startsWith("swarm:")) return null

  const parts = title.split(":")
  if (parts.length < 2) return null

  const status = parts[1] as SessionStatus
  if (!["idle", "working", "blocked"].includes(status)) return null

  return {
    status,
    agent: parts[2] || undefined,
    context: parts[3] || undefined,
    cwd: parts[4] || undefined,
  }
}

/**
 * Get remote sessions by scanning Hyprland window titles for swarm:* patterns
 * Returns session entries for windows with swarm status in their title
 */
async function getRemoteSessions(): Promise<SessionEntry[]> {
  const remote: SessionEntry[] = []
  try {
    const env = await getHyprEnv()
    if (!env) return remote

    const proc = Bun.spawn(["hyprctl", "clients", "-j"], {
      stdout: "pipe",
      stderr: "pipe",
      env,
    })
    const output = await new Response(proc.stdout).text()
    await proc.exited
    if (proc.exitCode !== 0) return remote

    const data = JSON.parse(output)
    if (Array.isArray(data)) {
      for (const client of data) {
        const title = client.title || ""
        const parsed = parseSwarmTitle(title)
        if (parsed && client.pid && client.workspace?.id) {
          remote.push({
            pid: client.pid,
            cwd: parsed.cwd || "remote",
            hyprWorkspace: client.workspace.id,
            hyprWindowPid: client.pid,
            status: parsed.status,
            startedAt: Date.now(),
            lastUpdated: Date.now(),
            agent: parsed.agent,
            remote: true,
          })
        }
      }
    }
  } catch {
    // Hyprland not available
  }
  return remote
}

export const Hyprland = {
  /**
   * Initialize hyprland tracking (no-op, kept for backwards compatibility)
   * @deprecated Hyprland now auto-detects, no initialization needed
   */
  init(_isEnabled?: boolean): void {
    log.info("hyprland auto-detection enabled")
  },

  /**
   * Check if Hyprland is available (socket exists and responds)
   * This is used for workspace-specific features
   */
  async isAvailable(): Promise<boolean> {
    const info = await getHyprlandInfo()
    return info !== null
  },

  /**
   * Check if hyprland tracking is enabled
   * @deprecated Always returns true now - session tracking always enabled
   */
  isEnabled(): boolean {
    return true
  },

  /**
   * Get the current Hyprland workspace for the active window
   */
  async getWorkspace(): Promise<number | null> {
    const info = await getHyprlandInfo()
    return info?.workspace ?? null
  },

  /**
   * Register this session on startup
   * Always registers - Hyprland workspace info is optional
   */
  async register(cwd: string): Promise<void> {
    const hyprInfo = await getHyprlandInfo()
    const pid = process.pid

    currentSession = {
      pid,
      cwd,
      hyprWorkspace: hyprInfo?.workspace ?? null,
      hyprWindowPid: hyprInfo?.windowPid ?? null,
    }

    // Prune dead sessions first
    await Hyprland.prune()

    const sessions = await readSessions()
    const filtered = sessions.filter((s) => s.pid !== pid)

    const entry: SessionEntry = {
      pid,
      cwd,
      hyprWorkspace: hyprInfo?.workspace ?? null,
      hyprWindowPid: hyprInfo?.windowPid ?? null,
      status: "idle",
      startedAt: Date.now(),
      lastUpdated: Date.now(),
    }

    filtered.push(entry)
    await writeSessions(filtered)

    log.info("registered session", {
      pid,
      hyprWorkspace: entry.hyprWorkspace,
      cwd,
    })
  },

  /**
   * Unregister this session on exit
   */
  async unregister(): Promise<void> {
    if (!currentSession) return

    const sessions = await readSessions()
    const filtered = sessions.filter((s) => s.pid !== currentSession!.pid)
    await writeSessions(filtered)

    log.info("unregistered session", { pid: currentSession.pid })
    currentSession = null
  },

  /**
   * Update the status of this session
   */
  async setStatus(status: SessionStatus): Promise<void> {
    if (!currentSession) return

    const sessions = await readSessions()
    const entry = sessions.find((s) => s.pid === currentSession!.pid)
    if (entry) {
      entry.status = status
      entry.lastUpdated = Date.now()
      await writeSessions(sessions)
      log.info("status updated", { pid: currentSession.pid, status })
    }
  },

  /**
   * Update rich session info (sessionId, agent, model)
   * Works from any thread by looking up session by PID
   */
  async setSessionInfo(info: { sessionId?: string; agent?: string; model?: string }): Promise<void> {
    // Don't require enabled/currentSession - just try to update by PID
    const sessions = await readSessions()
    const entry = sessions.find((s) => s.pid === process.pid)
    if (!entry) return // No session registered for this PID

    if (info.sessionId !== undefined) entry.sessionId = info.sessionId
    if (info.agent !== undefined) entry.agent = info.agent
    if (info.model !== undefined) entry.model = info.model
    entry.lastUpdated = Date.now()
    await writeSessions(sessions)
    log.info("session info updated", { pid: process.pid, ...info })
  },

  /**
   * Get all sessions (for status bar display)
   * Includes local sessions from file + remote sessions from window titles
   * Prunes stale sessions first to ensure fresh data
   */
  async getSessions(): Promise<SessionEntry[]> {
    await Hyprland.prune()
    const local = await readSessions()
    const remote = await getRemoteSessions()

    // Filter out remote sessions that match local PIDs (avoid duplicates)
    const localPids = new Set(local.map((s) => s.pid))
    const uniqueRemote = remote.filter((r) => !localPids.has(r.pid))

    return [...local, ...uniqueRemote]
  },

  /**
   * Get sessions filtered by Hyprland workspace
   */
  async getSessionsByWorkspace(workspace: number): Promise<SessionEntry[]> {
    const sessions = await readSessions()
    return sessions.filter((s) => s.hyprWorkspace === workspace)
  },

  /**
   * Get blocked sessions (prunes stale first)
   */
  async getBlockedSessions(): Promise<SessionEntry[]> {
    await Hyprland.prune()
    const sessions = await readSessions()
    return sessions.filter((s) => s.status === "blocked")
  },

  /**
   * Prune stale sessions:
   * 1. PIDs that no longer exist (process died)
   * 2. Window PIDs no longer on their claimed workspace (terminal closed/moved)
   */
  async prune(): Promise<void> {
    const sessions = await readSessions()
    const clients = await getHyprlandClients()
    const alive: SessionEntry[] = []

    for (const session of sessions) {
      // Check 1: Is the process still alive?
      let processAlive = false
      try {
        process.kill(session.pid, 0)
        processAlive = true
      } catch {
        log.info("pruned dead process", { pid: session.pid })
        continue
      }

      // Check 2: If we have hyprland info, verify window is still on claimed workspace
      if (session.hyprWindowPid !== null && session.hyprWorkspace !== null) {
        const currentWorkspace = clients.get(session.hyprWindowPid)
        if (currentWorkspace === undefined) {
          // Window no longer exists in hyprland (terminal closed)
          log.info("pruned closed window", {
            pid: session.pid,
            hyprWindowPid: session.hyprWindowPid,
            claimedWorkspace: session.hyprWorkspace,
          })
          continue
        }
        if (currentWorkspace !== session.hyprWorkspace) {
          // Window moved to different workspace - update it
          log.info("window moved workspace", {
            pid: session.pid,
            from: session.hyprWorkspace,
            to: currentWorkspace,
          })
          session.hyprWorkspace = currentWorkspace
          session.lastUpdated = Date.now()
        }
      }

      alive.push(session)
    }

    if (alive.length !== sessions.length) {
      await writeSessions(alive)
    }
  },

  /**
   * Get current session info
   */
  getCurrentSession() {
    return currentSession
  },
}
