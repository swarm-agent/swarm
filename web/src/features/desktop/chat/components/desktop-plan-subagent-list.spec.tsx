import React from "react";
import assert from "node:assert/strict";
import test from "node:test";
import { renderToStaticMarkup } from "react-dom/server";

import { DesktopPlanSubagentList } from "./desktop-plan-subagent-list";
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

test("plan subagents collapse to a count and retain navigable session rows", () => {
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
  assert.match(markup, /<summary[^>]*>\s*<span>2<\/span>/);
  assert.doesNotMatch(markup, />subagents<\/span>/);
  assert.match(markup, /aria-label="Open Child session 1\./);
  assert.match(markup, /aria-label="Open Child session 2\./);
  assert.match(markup, /aria-label="Stop Child session 1"/);
  assert.doesNotMatch(markup, /uppercase tracking/);
});
