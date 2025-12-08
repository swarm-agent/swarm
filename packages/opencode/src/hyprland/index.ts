import fs from "fs/promises"
import path from "path"
import os from "os"
import { Log } from "../util/log"

const log = Log.create({ service: "hyprland" })

export type SessionStatus = "idle" | "working" | "blocked"

export interface SessionEntry {
  pid: number
  cwd: string
  hyprWorkspace: number | null
  hyprWindowPid: number | null
  status: SessionStatus
  startedAt: number
  lastUpdated: number
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
  await ensureDir()
  const tmpFile = `${SESSIONS_FILE}.tmp.${process.pid}`
  await fs.writeFile(tmpFile, JSON.stringify({ sessions }, null, 2))
  await fs.rename(tmpFile, SESSIONS_FILE)
}

async function getHyprlandInfo(): Promise<{ workspace: number; windowPid: number } | null> {
  try {
    const proc = Bun.spawn(["hyprctl", "activewindow", "-j"], {
      stdout: "pipe",
      stderr: "pipe",
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

export const Hyprland = {
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
   * Get all sessions (for status bar display)
   */
  async getSessions(): Promise<SessionEntry[]> {
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
   * Get blocked sessions
   */
  async getBlockedSessions(): Promise<SessionEntry[]> {
    const sessions = await readSessions()
    return sessions.filter((s) => s.status === "blocked")
  },

  /**
   * Prune dead sessions (PIDs that no longer exist)
   */
  async prune(): Promise<void> {
    const sessions = await readSessions()
    const alive: SessionEntry[] = []

    for (const session of sessions) {
      try {
        process.kill(session.pid, 0) // Check if process exists
        alive.push(session)
      } catch {
        log.info("pruned dead session", { pid: session.pid })
      }
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
