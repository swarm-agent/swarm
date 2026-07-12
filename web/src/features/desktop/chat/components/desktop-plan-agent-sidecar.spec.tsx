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
  toolArguments: JSON.stringify({ proposal_revision: 7 }),
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
  />,
);

assert(markup.includes('data-embedded="true"'), "expected integrated plan sidebar");
assert(
  markup.includes("Ask about the plan or request changes conversationally"),
  "expected plan-agent guidance",
);
assert(markup.includes(">Plan<"), "expected Plan-only sidebar heading");
assert(!markup.includes("Plan &amp; AI"), "AI tabs must be absent from the MVP sidebar");
assert(!markup.includes("Ask AI"), "AI sidechat controls must be absent");
assert(markup.includes('data-testid="desktop-plan-agent-scroller"'), "Plan conversation should use the shared sticky-bottom scroller");
assert(markup.includes("Jump to latest Plan message") === false, "jump control remains hidden while initially pinned");
assert(markup.includes("Saved edits update the parent approval card live."), "plan edits should advertise live parent updates");
assert(markup.includes("Send to Plan"), "idle real session should expose its send control");
assert(!markup.includes("ChatMarkdown"), "sidechat must reuse canonical Desktop V3 render items rather than a custom message renderer");
assert(
  !markup.includes("Parent conversation context for this plan review"),
  "raw parent context must not render",
);

console.log("desktop plan agent sidecar tests passed");
