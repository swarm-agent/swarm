import { Log } from "../util/log"

export namespace IdleShutdown {
  const log = Log.create({ service: "idle-shutdown" })

  let timer: Timer | undefined
  let lastActivity = Date.now()
  let timeoutMs = 0
  let started = false

  // Call this once at server startup with timeout from config
  // Safe to call multiple times - only initializes once
  export function start(timeoutMinutes: number | undefined) {
    if (started) return
    started = true

    if (!timeoutMinutes || timeoutMinutes <= 0) {
      log.debug("idle shutdown disabled")
      return
    }

    timeoutMs = timeoutMinutes * 60 * 1000
    log.info("idle shutdown enabled", { timeoutMinutes })

    // Start initial timer
    resetTimer()
  }

  // Call this on any activity (HTTP requests, etc)
  export function activity() {
    if (!timeoutMs) return
    lastActivity = Date.now()
    resetTimer()
  }

  function resetTimer() {
    if (!timeoutMs) return
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      const idleMs = Date.now() - lastActivity
      log.info("shutting down due to idle timeout", {
        idleMinutes: Math.round(idleMs / 60000),
      })
      process.exit(0)
    }, timeoutMs)
  }
}
