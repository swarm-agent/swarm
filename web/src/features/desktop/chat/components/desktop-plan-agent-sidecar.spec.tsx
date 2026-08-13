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

const source = readFileSync(fileURLToPath(new URL("./desktop-plan-agent-sidecar.tsx", import.meta.url)), "utf8");
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

const mobileInlineMarkup = renderToStaticMarkup(
  <DesktopPlanAgentSidecar
    parentSessionId="parent-1"
    permission={permission}
    document={document}
    embedded
    mobileInline
    mobileOpen
    modelLabel="model-a"
    onClose={() => undefined}
  />,
);

assert.match(markup, /data-embedded="true"/, "expected integrated plan sidebar");
assert.match(mobileInlineMarkup, /data-mobile-inline="true"/, "expected an inline mobile Plan chat variant");
assert.match(mobileInlineMarkup, />Ask Swarm Plan</, "inline mobile Plan chat should use the requested waiting-state title");
assert.doesNotMatch(mobileInlineMarkup, /fixed inset-0/, "inline mobile Plan chat should not open as a full-screen sheet");
assert.match(mobileInlineMarkup, /h-\[min\(62dvh,36rem\)\]/, "expanded mobile Plan chat should stay above the main composer with a bounded height");
assert.match(markup, /Ask about the plan or request changes conversationally/, "expected plan-agent guidance");
assert.match(markup, />Plan</, "expected Plan-only sidebar heading");
assert.doesNotMatch(markup, /Plan &amp; AI/, "AI tabs must be absent from the MVP sidebar");
assert.doesNotMatch(markup, /Ask AI/, "AI sidechat controls must be absent");
assert.match(markup, /data-testid="desktop-plan-agent-scroller"/, "Plan conversation should use the shared sticky-bottom scroller");
assert.match(source, /h-\[88dvh\][\s\S]*max-h-\[88dvh\][\s\S]*min-\[1300px\]:h-auto/, "mobile Plan popout should have a bounded dynamic-viewport height while desktop keeps its flexible sidebar height");
assert.match(source, /min-\[1300px\]:min-h-0 min-\[1300px\]:flex-1[\s\S]*min-\[1300px\]:h-auto min-\[1300px\]:max-h-none min-\[1300px\]:flex-1/, "embedded desktop Plan should fill the sidebar column instead of shrinking its composer toward the top");
assert.match(source, /touch-pan-y[\s\S]*overflow-y-auto[\s\S]*overscroll-contain[\s\S]*-webkit-overflow-scrolling:touch/, "mobile Plan conversation should explicitly support touch scrolling");
assert.match(markup, /data-testid="desktop-plan-agent-tail-anchor"/, "Plan conversation should expose the shared CSS tail anchor");
assert.doesNotMatch(markup, /Jump to latest Plan message/, "jump control remains hidden while initially pinned");
assert.match(markup, /Saved edits update the parent approval card live\./, "plan edits should advertise live parent updates");
assert.match(markup, /aria-label="Send to Plan"/, "idle real session should expose its send control");
assert.match(markup, /aria-label="Start microphone dictation"/, "Plan composer should expose the canonical microphone affordance");
assert.match(markup, /aria-label="Context window unavailable"/, "Plan composer should expose the real compact/context control before usage arrives");
assert.match(markup, /data-testid="desktop-plan-composer"/, "expected dedicated Plan composer wrapper");
assert.match(markup, /class="[^"]*border-t[^\"]*bg-\[var\(--app-surface\)\][^"]*" data-testid="desktop-plan-composer"/, "Plan composer should share the canonical chat composer boundary");
assert.doesNotMatch(markup, /data-testid="desktop-plan-composer"[^>]*pb-1\.5/, "Plan composer should not be taller than the canonical chat composer");
assert.match(markup, /min-w-0 items-center justify-between gap-2 overflow-hidden bg-transparent px-4 py-3 text-\[11px\]/, "Plan control row should use the canonical chat padding for the matched 311×144 geometry");
assert.doesNotMatch(markup, /border-y border-\[var\(--app-border-strong\)\]/, "Plan control row should not add a separator absent from the canonical chat composer");
assert.match(source, /className=\{dictationEnabled[\s\S]*inline-flex h-9 w-9[\s\S]*className="h-10 w-10 shrink-0 rounded-lg p-0"/, "Plan microphone and send controls should match the canonical chat control heights");
assert.match(markup, /!min-h-\[32px\][\s\S]*sm:!min-h-\[56px\][\s\S]*lg:!min-h-\[52px\]/, "Plan input should use canonical chat baseline heights");
assert.match(markup, /max-h-\[50vh\][\s\S]*resize-none[\s\S]*overflow-y-hidden/, "Plan input should grow upward before scrolling");
assert.doesNotMatch(markup, /ChatMarkdown/, "sidechat must reuse canonical Desktop V3 render items rather than a custom message renderer");
assert.doesNotMatch(markup, /Parent conversation context for this plan review/, "raw parent context must not render");

assert.match(source, /event\.key !== "Enter" \|\| event\.shiftKey \|\| event\.nativeEvent\.isComposing/, "Enter should submit while Shift+Enter and IME composition preserve multiline input");
assert.match(source, /if \(!textarea\.value\)[\s\S]*removeProperty\("height"\)[\s\S]*textarea\.style\.height = "auto"[\s\S]*Math\.min\(textarea\.scrollHeight, viewportMaxHeight\)/, "Plan textarea should keep its baseline while empty and auto-grow only from typed content");
assert.match(source, /speechRecognitionConstructor\(\)[\s\S]*recognition\.onresult[\s\S]*setDraft\(appendDictation/, "Plan microphone should feed browser dictation into the draft");
assert.match(source, /DesktopV3CompactButton[\s\S]*remaining_tokens[\s\S]*Remaining context[\s\S]*compactDesktopV3Session/, "Plan should reuse the canonical compact button, display remaining context, and compact its durable sidechat session");
assert.doesNotMatch(source, /MessageCircle|reopen|compact Plan chat/i, "Plan must not invent a compact reopen-chat control");

console.log("desktop plan agent sidecar tests passed");
