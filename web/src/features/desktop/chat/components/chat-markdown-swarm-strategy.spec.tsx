import React from "react";
import assert from "node:assert/strict";
import test from "node:test";
import { renderToStaticMarkup } from "react-dom/server";

import { ToolMessageView } from "./chat-markdown";
import { buildStructuredToolMessage } from "../services/tool-message";

function renderTask(output: Record<string, unknown>, argumentsValue: Record<string, unknown> = {}) {
  const message = buildStructuredToolMessage({
    tool: "task",
    argumentsText: JSON.stringify(argumentsValue),
    outputText: JSON.stringify(output),
  });
  assert(message, "expected structured task message");
  return renderToStaticMarkup(<ToolMessageView toolMessage={message} />);
}

test("legacy explore payload renders as Iteration Swarm", () => {
  const markup = renderTask({
    tool: "task",
    path_id: "tool.task.v1",
    task_mode: "swarm",
    launches: [{
      launch_index: 1,
      swarm_mode: true,
      subagent: "finder",
      assignment_label: "Alternative approach",
      status: "running",
    }],
  }, { mode: "swarm" });

  assert.match(markup, /ITERATION SWARM/);
  assert.match(markup, /Fast parallel iterations · choose or synthesize/);
  assert.doesNotMatch(markup, /SWARM MODE/);
});

test("Assembly swarm renders part identity and pending parent integration", () => {
  const contract = "Combine committed parts into the parent deliverable.";
  const markup = renderTask({
    tool: "task",
    path_id: "tool.task.v1",
    task_mode: "swarm",
    swarm_strategy: "assembly",
    integration_contract: contract,
    integration_required: true,
    integration_status: "pending_parent_assembly",
    ready_for_dependent_work: false,
    launches: [{
      launch_index: 1,
      child_session_id: "part-1",
      swarm_mode: true,
      swarm_strategy: "assembly",
      assembly_part: { name: "Backend API", owned_scope: ["swarmd/internal/api/**"] },
      integration_contract: contract,
      integration_required: true,
      subagent: "coder",
      status: "done",
    }],
  }, { mode: "swarm", swarm_strategy: "assembly" });

  assert.match(markup, /ASSEMBLY SWARM/);
  assert.match(markup, /Complementary parts · parent integration required/);
  assert.match(markup, /Contract: Combine committed parts into the parent deliverable\./);
  assert.match(markup, /Backend API/);
  assert.doesNotMatch(markup, /ready for dependent work/i);
});
