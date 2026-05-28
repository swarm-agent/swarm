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

function main(): void {
  testDeniedExitPlanPermissionUsesFlatPreview();
  testPlanManageUsesFlatPreview();
  console.log("chat-markdown preview tests passed");
}

main();
