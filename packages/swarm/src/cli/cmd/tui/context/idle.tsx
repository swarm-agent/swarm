import { createSignal, onCleanup, onMount } from "solid-js"
import { createSimpleContext } from "./helper"
import { useSync } from "./sync"
import { useExit } from "./exit"
import { Log } from "@/util/log"

const log = Log.create({ service: "tui.idle" })

export const { use: useIdle, provider: IdleProvider } = createSimpleContext({
  name: "Idle",
  init: () => {
    const sync = useSync()
    const exit = useExit()

    const [lastActivity, setLastActivity] = createSignal(Date.now())
    let timer: Timer | undefined
    let timeoutMs = 0

    onMount(() => {
      // Get timeout from config (in minutes), default 0 = disabled
      // Cast to any since SDK types may not have this field yet
      const config = sync.data.config as Record<string, unknown>
      const timeoutMinutes = (config.processIdleTimeout as number) ?? 0
      if (timeoutMinutes <= 0) {
        log.info("process idle timeout disabled")
        return
      }

      timeoutMs = timeoutMinutes * 60 * 1000
      log.info("idle timeout enabled", { timeoutMinutes, timeoutMs, pid: process.pid })

      // Start initial timer
      resetTimer()
    })

    onCleanup(() => {
      if (timer) {
        clearTimeout(timer)
        timer = undefined
      }
    })

    function resetTimer() {
      if (timeoutMs <= 0) return
      if (timer) clearTimeout(timer)

      timer = setTimeout(() => {
        const idleMs = Date.now() - lastActivity()
        log.info("shutting down due to idle timeout", {
          idleMinutes: Math.round(idleMs / 60000),
          idleMs,
        })
        exit()
      }, timeoutMs)
    }

    function touch() {
      if (timeoutMs <= 0) return
      setLastActivity(Date.now())
      log.debug("activity detected, resetting timer", { pid: process.pid })
      resetTimer()
    }

    return {
      touch,
      lastActivity,
    }
  },
})
