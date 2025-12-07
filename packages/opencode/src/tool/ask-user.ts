import { Tool } from "./tool"
import { Permission } from "../permission"
import { Log } from "../util/log"
import z from "zod"

const log = Log.create({ service: "ask-user-tool" })

const OptionSchema = z.object({
  value: z.string().describe("Unique value for this option"),
  label: z.string().describe("Display label for this option"),
  description: z.string().optional().describe("Optional description shown below the label"),
  allowCustom: z.boolean().optional().describe("If true, allows user to type custom text for this option"),
})

const QuestionSchema = z.object({
  id: z.string().describe("Unique identifier for this question"),
  text: z.string().describe("The question text"),
  type: z.enum(["single", "multiple"]).default("single").describe("single = one answer, multiple = can select many"),
  options: z.array(OptionSchema).min(1).describe("Available choices for this question"),
  required: z.boolean().default(true).describe("Whether an answer is required"),
})

export type AskUserQuestion = z.infer<typeof QuestionSchema>
export type AskUserOption = z.infer<typeof OptionSchema>

export const AskUserTool = Tool.define("ask-user", {
  description: `Ask the user one or more questions with multiple choice answers. Use this tool when you need:
- Clarification on requirements or preferences
- The user to make decisions between options
- To understand priorities or constraints
- Confirmation before proceeding with significant changes

Questions are presented in an interactive UI where users can navigate between questions, select answers, and submit. The tool blocks until the user responds or cancels.`,
  parameters: z.object({
    title: z.string().describe("Title shown at top of the question dialog"),
    context: z.string().optional().describe("Optional context or explanation shown before questions"),
    questions: z.array(QuestionSchema).min(1).max(5).describe("List of questions to ask the user (max 5)"),
  }),
  async execute(params, ctx) {
    log.info("ask-user execute called", { title: params.title, sessionID: ctx.sessionID })
    const permission = await Permission.ask({
      type: "ask-user",
      pattern: "user-questions",
      sessionID: ctx.sessionID,
      messageID: ctx.messageID,
      callID: ctx.callID,
      title: params.title,
      metadata: {
        context: params.context,
        questions: params.questions,
        timestamp: Date.now(),
      },
    })

    log.info("ask-user permission returned", {
      hasPermission: !!permission,
      permissionId: permission?.id,
      metadata: permission?.metadata,
      answers: permission?.metadata?.answers,
    })

    const answers = permission?.metadata?.answers as Record<string, string | string[]> | undefined

    if (!answers) {
      log.warn("ask-user: no answers found, throwing RejectedError")
      throw new Permission.RejectedError(
        ctx.sessionID,
        "ask-user",
        ctx.callID,
        {},
        "User cancelled the questions dialog without providing answers.",
      )
    }

    return {
      title: "User answered questions",
      metadata: {
        questionCount: params.questions.length,
        answers,
      },
      output: formatAnswersForAgent(params.questions, answers),
    }
  },
})

function formatAnswersForAgent(questions: AskUserQuestion[], answers: Record<string, string | string[]>): string {
  const lines = ["## User Responses\n"]

  for (const q of questions) {
    const answer = answers[q.id]
    if (answer === undefined) {
      lines.push(`**${q.text}**`)
      lines.push(`Answer: (not answered)\n`)
      continue
    }

    const option = q.options.find((o) => o.value === answer)
    const displayAnswer = Array.isArray(answer)
      ? answer.map((a) => q.options.find((o) => o.value === a)?.label ?? a).join(", ")
      : (option?.label ?? answer)

    lines.push(`**${q.text}**`)
    lines.push(`Answer: ${displayAnswer}\n`)
  }

  return lines.join("\n")
}
