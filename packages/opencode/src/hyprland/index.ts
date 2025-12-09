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
}

const SESSIONS_DIR = path.join(os.homedir(), ".config", "swarm")
const SESSIONS_FILE = path.join(SESSIONS_DIR, "sessions.json")

// Whether hyprland session tracking is enabled (set via config)
let enabled = false

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
  await ensureDir()
  const tmpFile = `${SESSIONS_FILE}.tmp.${process.pid}`
  await fs.writeFile(tmpFile, JSON.stringify({ sessions }, null, 2))
  await fs.rename(tmpFile, SESSIONS_FILE)
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

export const Hyprland = {
  /**
   * Initialize hyprland tracking (call with config value)
   */
  init(isEnabled: boolean): void {
    enabled = isEnabled
    log.info("hyprland tracking", { enabled })
  },

  /**
   * Check if hyprland tracking is enabled
   */
  isEnabled(): boolean {
    return enabled
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
   */
  async register(cwd: string): Promise<void> {
    if (!enabled) return

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
    if (!enabled) return
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
    if (!enabled) return
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
   * Prunes stale sessions first to ensure fresh data
   */
  async getSessions(): Promise<SessionEntry[]> {
    await Hyprland.prune()
    return readSessions()
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
