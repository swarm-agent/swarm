import { Config } from "../config/config"
import { Log } from "../util/log"

const log = Log.create({ service: "notification" })

// Timer for delayed notifications
let blockedTimer: ReturnType<typeof setTimeout> | null = null
let currentBlockedReason: string | null = null

/**
 * Check if we're in an SSH session
 */
function isSSH(): boolean {
  return !!(process.env.SSH_CLIENT || process.env.SSH_TTY || process.env.SSH_CONNECTION)
}

/**
 * Send an OSC 9 desktop notification
 * Works through SSH - escape codes travel to local terminal
 */
function sendOSC9(message: string) {
  process.stdout.write(`\x1b]9;${message}\x07`)
}

/**
 * Send a native Linux notification via notify-send
 */
function sendNotifySend(title: string, body: string) {
  Bun.spawn(["notify-send", "-a", "swarm", "-i", "terminal", title, body], {
    stdout: "ignore",
    stderr: "ignore",
  })
}

/**
 * Send notification - uses notify-send locally, OSC 9 over SSH
 */
function sendNotification(title: string, body: string) {
  if (isSSH()) {
    // Over SSH: use OSC 9 (travels through to local Ghostty)
    sendOSC9(`${title}: ${body}`)
  } else {
    // Local Linux: use notify-send with "swarm" as app name
    sendNotifySend(title, body)
  }
}

/**
 * Set terminal title with structured status info
 * Format: swarm:<status>:<agent>:<context>:<cwd>
 * Local swarm-cli can read this via Hyprland to show remote sessions
 */
function setTitle(status: string, extra?: { agent?: string; context?: string; cwd?: string }) {
  const parts = ["swarm", status]
  if (extra?.agent) parts.push(extra.agent)
  if (extra?.context) parts.push(extra.context)
  if (extra?.cwd) parts.push(extra.cwd)
  process.stdout.write(`\x1b]0;${parts.join(":")}\x07`)
}

/**
 * Clear the swarm title (reset to default)
 */
function clearTitle() {
  process.stdout.write(`\x1b]0;\x07`)
}

export const Notification = {
  /**
   * Notify that the session is blocked, with optional delay
   * If still blocked after delayMs, sends desktop notification
   */
  async blocked(reason: string, extra?: { agent?: string; context?: string; cwd?: string }) {
    const config = await Config.get()
    const notifConfig = config.notifications

    // Always set title for status bar integration
    setTitle("blocked", { ...extra, cwd: extra?.cwd || process.cwd() })
    currentBlockedReason = reason

    if (!notifConfig?.enabled) {
      log.info("notifications disabled, skipping")
      return
    }

    const delayMs = notifConfig.delayMs ?? 5000

    // Clear any existing timer
    if (blockedTimer) {
      clearTimeout(blockedTimer)
      blockedTimer = null
    }

    if (delayMs === 0) {
      // Immediate notification
      sendNotification("swarm", `Blocked: ${reason}`)
      log.info("sent immediate notification", { reason })
    } else {
      // Delayed notification
      blockedTimer = setTimeout(() => {
        if (currentBlockedReason === reason) {
          sendNotification("swarm", `Blocked: ${reason}`)
          log.info("sent delayed notification", { reason, delayMs })
        }
        blockedTimer = null
      }, delayMs)
      log.info("scheduled notification", { reason, delayMs })
    }
  },

  /**
   * Clear blocked state (user responded)
   */
  async unblocked(extra?: { agent?: string; context?: string; cwd?: string }) {
    // Cancel pending notification
    if (blockedTimer) {
      clearTimeout(blockedTimer)
      blockedTimer = null
      log.info("cancelled pending notification")
    }
    currentBlockedReason = null

    // Update title to working status
    setTitle("working", { ...extra, cwd: extra?.cwd || process.cwd() })
  },

  /**
   * Set idle status
   */
  async idle(extra?: { agent?: string; context?: string; cwd?: string }) {
    if (blockedTimer) {
      clearTimeout(blockedTimer)
      blockedTimer = null
    }
    currentBlockedReason = null
    setTitle("idle", { ...extra, cwd: extra?.cwd || process.cwd() })
  },

  /**
   * Clear all notification state (on exit)
   */
  async clear() {
    if (blockedTimer) {
      clearTimeout(blockedTimer)
      blockedTimer = null
    }
    currentBlockedReason = null
    clearTitle()
  },

  /**
   * Send a custom notification (for other events)
   */
  async notify(title: string, body?: string) {
    const config = await Config.get()
    if (!config.notifications?.enabled) return
    sendNotification(title, body || "")
    log.info("sent custom notification", { title, body })
  },
}
