import { Bus } from "@/bus"
import { Config } from "@/config/config"
import { Log } from "@/util/log"
import { BashEvent } from "@/tool/bash"
import { BackgroundAgent } from "@/background-agent"
import { Instance } from "@/project/instance"

const log = Log.create({ service: "memory" })

/**
 * Memory System - Auto-updates AGENTS.md after git commits
 *
 * Enable in config:
 * ```json
 * {
 *   "memory": {
 *     "enabled": true,
 *     "autoCommit": true,
 *     "model": "anthropic/claude-sonnet-4-20250514"
 *   }
 * }
 * ```
 */
export namespace Memory {
  let initialized = false

  /**
   * Initialize the memory system - subscribes to bash events
   * Call this once during app startup
   */
  export async function init() {
    if (initialized) return
    initialized = true

    const cfg = await Config.get()

    // Only subscribe if memory system is enabled
    if (!cfg.memory?.enabled) {
      log.info("memory system disabled")
      return
    }

    log.info("memory system enabled", {
      autoCommit: cfg.memory?.autoCommit ?? true,
      model: cfg.memory?.model ?? "default",
    })

    // Subscribe to bash events
    Bus.subscribe(BashEvent.Executed, async (evt) => {
      const { command, exitCode, sessionID, isCommit, cwd } = evt.properties

      // Only trigger on successful git commits (not amends)
      if (!isCommit || exitCode !== 0) return

      // Check if autoCommit is enabled (default true when memory is enabled)
      const config = await Config.get()
      if (config.memory?.autoCommit === false) {
        log.info("autoCommit disabled, skipping memory update")
        return
      }

      log.info("git commit detected, spawning memory agent", { command, sessionID, cwd })

      try {
        // Spawn background memory agent to update AGENTS.md
        await BackgroundAgent.spawn({
          parentSessionID: sessionID,
          agent: "memory",
          cwd,
          description: "Update AGENTS.md after commit",
          prompt: `A git commit was just made in ${cwd}. Update AGENTS.md with this change.

1. Run \`git log --oneline -1\` to see the commit message
2. Run \`git diff HEAD~1 --stat\` to see what files changed
3. Read the current AGENTS.md (create if it doesn't exist)
4. Add an entry to the Session Log with today's date and a brief summary
5. If architecture or patterns changed significantly, update those sections
6. Write the updated AGENTS.md
7. Amend the commit to include the AGENTS.md changes:
   \`\`\`bash
   git add AGENTS.md
   git commit --amend --no-edit
   \`\`\`

Keep the Session Log entry concise - one line describing what changed.`,
        })

        log.info("memory agent spawned successfully")
      } catch (err) {
        log.error("failed to spawn memory agent", { error: err })
      }
    })
  }

  /**
   * Check if memory system is enabled
   */
  export async function isEnabled(): Promise<boolean> {
    const cfg = await Config.get()
    return cfg.memory?.enabled ?? false
  }
}
