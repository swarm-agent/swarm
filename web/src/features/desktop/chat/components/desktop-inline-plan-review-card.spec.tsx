import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { DesktopPermissionRecord } from "../../types/realtime";
import { DesktopInlinePlanReviewCard } from "./desktop-inline-plan-review-card";

function permission(
  requirement: string,
  toolName = "plan_manage",
): DesktopPermissionRecord {
  return {
    id: `perm-${requirement}`,
    sessionId: "session-1",
    runId: "run-1",
    callId: "call-1",
    toolName,
    toolArguments: JSON.stringify({
      title: "Inline plan",
      document: {
        info: { goal: "Review the objective inline" },
        checkpoints: [
          {
            id: "cp-1",
            title: "First checkpoint",
            tasks: ["Implement it"],
            acceptance_criteria: ["It works"],
          },
        ],
      },
      approved_arguments: {
        action:
          requirement === "plan_new_request" ? "request_new_plan" : "save",
      },
    }),
    status: "pending",
    decision: "",
    reason: "",
    requirement,
    mode: "auto",
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
  };
}

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

for (const [requirement, toolName] of [
  ["permission", "exit_plan_mode"],
  ["plan_update", "plan_manage"],
  ["plan_followup_request", "plan_manage"],
  ["plan_revision_request", "plan_manage"],
  ["plan_amendment_request", "plan_manage"],
  ["plan_new_request", "plan_manage"],
] as const) {
  const record = permission(requirement, toolName);
  const markup = renderToStaticMarkup(
    <DesktopInlinePlanReviewCard
      permission={record}
      parentSessionId="session-1"
      pendingPosition={1}
      pendingCount={2}
      onResolve={async () => undefined}
    />,
  );
  assert(
    markup.includes('data-testid="desktop-inline-plan-review"'),
    `expected inline card for ${requirement}`,
  );
  assert(
    markup.includes(`data-permission-id="${record.id}"`),
    `expected permission identity for ${requirement}`,
  );
  assert(
    markup.includes("Review the objective inline"),
    `expected objective-first plan for ${requirement}`,
  );
  assert(
    markup.includes("First checkpoint"),
    `expected checkpoint for ${requirement}`,
  );
  assert(markup.includes(">Reject<") && markup.includes(">Accept once<"), `expected concise review controls for ${requirement}`);
  if (requirement !== "plan_update") {
    assert(markup.includes(">Always allow<"), `expected persistent plan acceptance control for ${requirement}`);
  } else {
    assert(!markup.includes(">Always allow<"), `expected generic plan update to remain outside plan acceptance policy for ${requirement}`);
  }
  assert(markup.includes(">Copy<"), `expected visible plan copy control for ${requirement}`);
  assert(!markup.includes("Ask Swarm"), `expected mobile plan chat control only when the opener is provided for ${requirement}`);
  assert(!markup.includes("Accept edit") && !markup.includes("Reject edit") && !markup.includes("Request another revision"), `expected legacy review labels to be removed for ${requirement}`);
  if (requirement === "permission" || requirement === "plan_new_request") {
    const automaticIndex = markup.indexOf("Starts automatically after approval");
    const rejectIndex = markup.indexOf(">Reject<");
    assert(markup.indexOf(">Always allow<") > rejectIndex, `expected persistence separate from automatic execution state for ${requirement}`);
    assert(automaticIndex >= 0 && automaticIndex < rejectIndex, `expected automatic execution state on the left of the bottom action row for ${requirement}`);
    assert(!markup.includes('role="switch"'), `expected no manual execution switch for ${requirement}`);
  }
  assert(!markup.includes("Message to Swarm (optional)"), `expected Plan Agent to replace standalone rejection note for ${requirement}`);
}

const mobileMarkup = renderToStaticMarkup(
  <DesktopInlinePlanReviewCard
    permission={permission("permission", "exit_plan_mode")}
    parentSessionId="session-1"
    pendingPosition={1}
    pendingCount={1}
    onResolve={async () => undefined}
    onAskForChanges={() => undefined}
  />,
);
assert(mobileMarkup.includes(">Ask Swarm<"), "expected mobile plan chat opener when provided");
assert(mobileMarkup.includes("xl:hidden"), "expected plan chat opener to stay mobile-only");
assert(mobileMarkup.indexOf(">Ask Swarm<") < mobileMarkup.indexOf(">Copy<"), "expected plan chat opener to the left of Copy");

console.log("desktop inline plan review card tests passed");
