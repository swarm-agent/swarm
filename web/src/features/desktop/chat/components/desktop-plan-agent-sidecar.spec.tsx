import React from "react";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { renderToStaticMarkup } from "react-dom/server";
import type { DesktopPermissionRecord } from "../../types/realtime";
import { DesktopPlanAgentSidecar } from "./desktop-plan-agent-sidecar";
import { normalizeStructuredPlanDocument } from "./structured-plan-document";

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

assert.match(markup, /data-embedded="true"/, "expected integrated plan sidebar");
assert.match(markup, /Ask about the plan or request changes conversationally/, "expected plan-agent guidance");
assert.match(markup, />Plan</, "expected Plan-only sidebar heading");
assert.doesNotMatch(markup, /Plan &amp; AI/, "AI tabs must be absent from the MVP sidebar");
assert.doesNotMatch(markup, /Ask AI/, "AI sidechat controls must be absent");
assert.match(markup, /data-testid="desktop-plan-agent-scroller"/, "Plan conversation should use the shared sticky-bottom scroller");
assert.doesNotMatch(markup, /Jump to latest Plan message/, "jump control remains hidden while initially pinned");
assert.match(markup, /Saved edits update the parent approval card live\./, "plan edits should advertise live parent updates");
assert.match(markup, /aria-label="Send to Plan"/, "idle real session should expose its send control");
assert.match(markup, /aria-label="Start microphone dictation"/, "Plan composer should expose the canonical microphone affordance");
assert.match(markup, /data-testid="desktop-plan-composer"/, "expected dedicated Plan composer wrapper");
assert.match(markup, /!min-h-\[32px\][\s\S]*sm:!min-h-\[56px\][\s\S]*lg:!min-h-\[52px\]/, "Plan input should use canonical chat baseline heights");
assert.match(markup, /max-h-\[50vh\][\s\S]*resize-none[\s\S]*overflow-y-hidden/, "Plan input should grow upward before scrolling");
assert.doesNotMatch(markup, /ChatMarkdown/, "sidechat must reuse canonical Desktop V3 render items rather than a custom message renderer");
assert.doesNotMatch(markup, /Parent conversation context for this plan review/, "raw parent context must not render");

const source = readFileSync(fileURLToPath(new URL("./desktop-plan-agent-sidecar.tsx", import.meta.url)), "utf8");
assert.match(source, /event\.key !== "Enter" \|\| event\.shiftKey \|\| event\.nativeEvent\.isComposing/, "Enter should submit while Shift+Enter and IME composition preserve multiline input");
assert.match(source, /textarea\.style\.height = "auto"[\s\S]*Math\.min\(textarea\.scrollHeight, viewportMaxHeight\)/, "Plan textarea should auto-grow from its content height");
assert.match(source, /speechRecognitionConstructor\(\)[\s\S]*recognition\.onresult[\s\S]*setDraft\(appendDictation/, "Plan microphone should feed browser dictation into the draft");

console.log("desktop plan agent sidecar tests passed");
