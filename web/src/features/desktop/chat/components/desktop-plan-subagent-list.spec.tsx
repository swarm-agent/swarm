import React from "react";
import assert from "node:assert/strict";
import test from "node:test";
import { renderToStaticMarkup } from "react-dom/server";

import { DesktopPlanSubagentList, desktopPlanSubagentActivityLabel } from "./desktop-plan-subagent-list";
import type { TaskToolRow } from "../types/chat";
import type { DesktopV3TaskChildViewModel } from "../../state/desktop-v3-cache-selectors";

function child(index: number): {
  row: TaskToolRow;
  view: DesktopV3TaskChildViewModel;
} {
  const sessionId = `child-${index}`;
  return {
    row: {
      launchIndex: index,
      childSessionId: sessionId,
      status: "running",
      phase: "running",
      agent: `agent-${index}`,
      assignmentLabel: `Child session ${index}`,
      modelLabel: "model",
      tool: "bash",
      time: "",
      previewKind: "",
      previewText: "",
      launchStartedAtMs: 1,
      currentToolStartedAtMs: 1,
      elapsedMs: 1_000,
      currentToolMs: 1_000,
      terminal: false,
    },
    view: {
      sessionId,
      hydrated: true,
      loading: false,
      unavailable: false,
      stale: false,
      terminal: false,
      status: "running",
      runId: `run-${index}`,
      currentTool: "bash",
      toolActivitySummary: "read x5",
      startedAt: 1,
      elapsedMs: 1_000,
      modelLabel: "model",
      contextWindow: 100,
      remainingTokens: 50,
      contextUpdatedAt: 1,
      workspacePath: "/workspace",
      workspaceName: "workspace",
      targetSwarmId: "",
      error: "",
    },
  };
}

test("plan subagent activity prefers canonical summaries while preserving state fallbacks", () => {
  const running = child(1);
  assert.equal(desktopPlanSubagentActivityLabel(running.view, running.row, "running", false), "read x5");
  assert.equal(desktopPlanSubagentActivityLabel({ ...running.view, loading: true }, running.row, "running", false), "loading");
  assert.equal(desktopPlanSubagentActivityLabel({ ...running.view, unavailable: true }, running.row, "running", false), "unavailable");
  assert.equal(desktopPlanSubagentActivityLabel({ ...running.view, stale: true }, running.row, "running", false), "stale");
  assert.equal(desktopPlanSubagentActivityLabel({ ...running.view, terminal: true }, running.row, "completed", true), "read x5");
  assert.equal(desktopPlanSubagentActivityLabel({ ...running.view, currentTool: "", toolActivitySummary: "" }, { ...running.row, tool: "-" }, "pending", false), "pending");
});

test("plan subagents collapse to a labeled bot control and retain navigable session rows", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanSubagentList
      children={[child(1), child(2)]}
      mode="compact"
      actions={{
        workspaceSlug: "workspace",
        parentSessionId: "parent",
        onNavigate: () => undefined,
      }}
    />,
  );

  assert.match(markup, /^<details(?![^>]* open)[^>]*data-plan-subagent-list/);
  assert.match(markup, /<summary[^>]*aria-label="Show 2 subagents"[^>]*>/);
  assert.match(markup, /<summary[^>]*>\s*<svg[^>]*class="lucide lucide-bot/);
  assert.match(markup, /<span[^>]*>2 subagents<\/span>/);
  assert.match(markup, /aria-label="Open Child session 1\./);
  assert.match(markup, /aria-label="Open Child session 2\./);
  assert.match(markup, /aria-label="Stop Child session 1"/);
  assert.match(markup, /read x5/);
  assert.doesNotMatch(markup, /uppercase tracking/);
});

test("plan subagent list describes Assembly children as parts with parent integration remaining", () => {
  const part = child(1);
  part.row = {
    ...part.row,
    swarmMode: true,
    swarmStrategy: "assembly",
    assemblyPart: { name: "Backend API", instructions: "Implement API", ownedScope: ["swarmd/internal/api/**"] },
    assignmentLabel: "Backend API",
    integrationContract: "Combine committed parts into the parent deliverable.",
    integrationRequired: true,
    terminal: true,
    status: "completed",
  };
  part.view = { ...part.view, terminal: true, status: "completed" };
  const markup = renderToStaticMarkup(<DesktopPlanSubagentList children={[part]} mode="compact" />);
  assert.match(markup, /aria-label="Show 1 part"/);
  assert.match(markup, /Assembly Swarm · complementary parts/);
  assert.match(markup, /Parent integration required after child completion\./);
  assert.match(markup, /Contract: Combine committed parts into the parent deliverable\./);
  assert.doesNotMatch(markup, /Show 1 subagent/);
});

test("plan subagent list describes legacy swarm_mode children as Explore alternatives", () => {
  const alternative = child(1);
  alternative.row = { ...alternative.row, swarmMode: true };
  const markup = renderToStaticMarkup(<DesktopPlanSubagentList children={[alternative]} mode="compact" />);
  assert.match(markup, /aria-label="Show 1 alternative"/);
  assert.match(markup, /Explore Swarm · independent alternatives/);
});
