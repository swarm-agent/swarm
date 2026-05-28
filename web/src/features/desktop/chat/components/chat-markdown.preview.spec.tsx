import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ToolMessageView } from "./chat-markdown";
import { buildStructuredToolMessage } from "../services/tool-message";

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

function renderToolMarkup(toolMessage: NonNullable<ReturnType<typeof buildStructuredToolMessage>>): string {
  return renderToStaticMarkup(<ToolMessageView toolMessage={toolMessage} nowMs={0} />);
}

function testDeniedExitPlanPermissionUsesFlatPreview(): void {
  const message = buildStructuredToolMessage({
    tool: "permission",
    callId: "call_denied_exit_plan",
    outputText: JSON.stringify({
      permission: {
        approved: false,
        status: "denied",
        reason: "Need rollout notes",
      },
      tool: {
        name: "exit_plan_mode",
        arguments: JSON.stringify({
          title: "Deployment Plan",
          plan_id: "plan_456",
        }),
      },
    }),
  });
  assert(Boolean(message), "expected structured exit-plan permission message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("plan denied · Deployment Plan"), "expected denied plan summary");
  assert(markup.includes("feedback: Need rollout notes"), "expected feedback preview");
  assert(!markup.includes("border border-[var(--app-border)]"), "exit-plan permission preview should be flat, not bordered");
}

function testPlanManageUsesFlatPreview(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_manage_save",
    outputText: JSON.stringify({
      tool: "plan_manage",
      action: "save",
      status: "ok",
      plan: {
        id: "plan_123",
        title: "Implementation Plan",
        plan: "# Plan\n1. Patch tool stream\n2. Test",
        update_summary: "polish tool stream",
      },
    }),
  });
  assert(Boolean(message), "expected structured plan_manage message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("save (Implementation Plan)"), "expected plan_manage summary");
  assert(markup.includes("action: save"), "expected plan_manage action preview");
  assert(!markup.includes("border border-[var(--app-border)]"), "plan_manage preview should be flat, not bordered");
}

function testTaskSwarmUsesCompactPreview(): void {
  const longAssignment = "Coordinate an extremely detailed research and implementation assignment that would normally push the desktop task header sideways";
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_swarm",
    outputText: JSON.stringify({
      tool: "task",
      description: longAssignment,
      launch_count: 12,
      launches: Array.from({ length: 12 }, (_, index) => ({
        launch_index: index + 1,
        status: index % 3 === 0 ? "running" : "done",
        resolved_agent_name: index % 2 === 0 ? "explorer" : "parallel",
        assignment_label: `${longAssignment} ${index + 1}`,
        current_tool: index % 3 === 0 ? "search" : "read",
        current_tool_ms: 1200 + index,
      })),
    }),
  });
  assert(Boolean(message), "expected structured task swarm message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Swarm mode"), "expected desktop swarm mode header");
  assert(markup.includes("@explorer"), "expected compact row agent label");
  assert(markup.includes("search"), "expected compact row current tool");
  assert(markup.includes("RUN"), "expected compact row status");
  assert(!markup.includes("Subagent stream"), "swarm mode should not render normal task table header");
  assert(!markup.includes("Current"), "swarm mode should not render detailed current column header");
  assert(!markup.includes(`task ${longAssignment}`), "swarm mode should suppress long task header summary");
}

function main(): void {
  testDeniedExitPlanPermissionUsesFlatPreview();
  testPlanManageUsesFlatPreview();
  testTaskSwarmUsesCompactPreview();
  console.log("chat-markdown preview tests passed");
}

main();
