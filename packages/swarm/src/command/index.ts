import z from "zod"
import { Config } from "../config/config"
import { Instance } from "../project/instance"
import PROMPT_INITIALIZE from "./template/initialize.txt"
import { Bus } from "../bus"
import { Identifier } from "../id/id"

export namespace Command {
  export const Default = {
    INIT: "init",
  } as const

  export const Event = {
    Executed: Bus.event(
      "command.executed",
      z.object({
        name: z.string(),
        sessionID: Identifier.schema("session"),
        arguments: z.string(),
        messageID: Identifier.schema("message"),
      }),
    ),
  }

  export const Info = z
    .object({
      name: z.string(),
      description: z.string().optional(),
      agent: z.string().optional(),
      model: z.string().optional(),
      template: z.string(),
      subtask: z.boolean().optional(),
    })
    .meta({
      ref: "Command",
    })
  export type Info = z.infer<typeof Info>

  const state = Instance.state(async () => {
    const cfg = await Config.get()

    const result: Record<string, Info> = {}

    for (const [name, command] of Object.entries(cfg.command ?? {})) {
      result[name] = {
        name,
        agent: command.agent,
        model: command.model,
        description: command.description,
        template: command.template,
        subtask: command.subtask,
      }
    }

    if (result[Default.INIT] === undefined) {
      result[Default.INIT] = {
        name: Default.INIT,
        description: "create/update AGENTS.md",
        agent: "memory",
        template: PROMPT_INITIALIZE.replace("${path}", Instance.worktree),
      }
    }

    // /memory command for post-commit updates
    if (result["memory"] === undefined) {
      result["memory"] = {
        name: "memory",
        description: "update AGENTS.md with recent changes",
        agent: "memory",
        template: `Update AGENTS.md based on recent git activity.

1. Run \`git log --oneline -5\` to see recent commits
2. Run \`git diff HEAD~1 --stat\` to see what files changed
3. Read the current AGENTS.md
4. Add an entry to the Session Log with today's date and a summary
5. If architecture or patterns changed, update those sections
6. Write the updated AGENTS.md
7. Stage and amend the commit:
   \`\`\`bash
   git add AGENTS.md
   git commit --amend --no-edit
   \`\`\`

$ARGUMENTS`,
      }
    }

    return result
  })

  export async function get(name: string) {
    return state().then((x) => x[name])
  }

  export async function list() {
    return state().then((x) => Object.values(x))
  }
}
