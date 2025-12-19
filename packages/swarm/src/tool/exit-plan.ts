import { Tool } from "./tool"
import { Permission } from "../permission"
import { SessionPlan } from "../session/plan"
import z from "zod"

export const ExitPlanModeTool = Tool.define("exit-plan-mode", {
  description: "Exit plan mode by presenting the plan to the user for approval",
  parameters: z.object({
    plan: z.string().describe("The complete plan in markdown format"),
    planID: z.string().optional().describe("ID of existing plan to update (for re-submissions after rejection)"),
  }),
  async execute(params, ctx) {
    if (ctx.agent !== "plan") {
      throw new Error("This tool is only available in plan mode")
    }

    const isUpdate = !!params.planID

    // Save or update the plan
    const savedPlan = isUpdate
      ? await SessionPlan.update(ctx.sessionID, { content: params.plan, status: "pending" })
      : await SessionPlan.save(ctx.sessionID, params.plan)

    const permission = await Permission.ask({
      type: "exit-plan-mode",
      pattern: "plan-approval",
      sessionID: ctx.sessionID,
      messageID: ctx.messageID,
      callID: ctx.callID,
      title: isUpdate ? "Review updated plan" : "Review and approve this plan",
      metadata: {
        plan: params.plan,
        planID: savedPlan.id,
        isUpdate,
        timestamp: Date.now(),
        switchToAgent: "build",
      },
    })

    // Plan was approved - update status
    await SessionPlan.update(ctx.sessionID, { status: "approved" })

    // Get selected agent from permission metadata (if user selected one)
    const selectedAgent = permission?.metadata?.selectedAgent as string | undefined
    const targetAgent = selectedAgent || "build"

    // Get user message if they approved with comment (Shift+Enter)
    const userMessage = permission?.metadata?.userMessage as string | undefined

    const baseOutput = "The user has approved the plan. You may now proceed with implementation."
    const output = userMessage ? `${baseOutput}\n\n${userMessage}` : baseOutput

    return {
      title: isUpdate ? "Updated plan approved" : "Plan approved",
      metadata: {
        switchToAgent: targetAgent,
        userMessage,
        planID: savedPlan.id,
      },
      output,
    }
  },
})
