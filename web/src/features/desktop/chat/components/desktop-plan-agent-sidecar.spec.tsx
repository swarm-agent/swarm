import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { DesktopPermissionRecord } from "../../types/realtime";
import { DesktopPlanAgentSidecar } from "./desktop-plan-agent-sidecar";
import { normalizeStructuredPlanDocument } from "./structured-plan-document";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const permission: DesktopPermissionRecord = {
  id: "permission-1",
  sessionId: "parent-1",
  runId: "run-1",
  callId: "call-1",
  toolName: "exit_plan_mode",
  toolArguments: "{}",
  status: "pending",
  decision: "",
  reason: "",
  requirement: "permission",
  mode: "plan",
  createdAt: 1,
  updatedAt: 1,
  resolvedAt: 0,
  permissionRequestedAt: 1,
};

const document = normalizeStructuredPlanDocument({
  id: "plan-1",
  revision_id: "3",
  info: { goal: "Review this plan" },
  checkpoints: [{ id: "cp-1", title: "Implement" }],
});
if (!document) throw new Error("expected normalized plan document");

const markup = renderToStaticMarkup(
  <DesktopPlanAgentSidecar
    parentSessionId="parent-1"
    permission={permission}
    document={document}
    embedded
    mobileOpen
    modelLabel="model-a"
    onClose={() => undefined}
    onSendChanges={async () => undefined}
  />,
);

assert(markup.includes('data-embedded="true"'), "expected integrated plan sidebar");
assert(
  markup.includes("Ask me anything about the plan"),
  "expected automatic plan guidance",
);
assert(markup.includes("Plan Agent · model-a"), "expected originating model label");
assert(
  !markup.includes("Parent conversation context for this plan review"),
  "raw parent context must not render",
);

console.log("desktop plan agent sidecar tests passed");
