import React from "react";
import assert from "node:assert/strict";
import test from "node:test";
import { renderToStaticMarkup } from "react-dom/server";
import { readFile } from "node:fs/promises";

import { ToolMessageView } from "./chat-markdown";
import { buildStructuredToolMessage } from "../services/tool-message";

function buildTask(output: Record<string, unknown>, argumentsValue: Record<string, unknown> = {}) {
  const message = buildStructuredToolMessage({
    tool: "task",
    argumentsText: JSON.stringify(argumentsValue),
    outputText: JSON.stringify(output),
  });
  assert(message, "expected structured task message");
  return message;
}

function renderTask(output: Record<string, unknown>, argumentsValue: Record<string, unknown> = {}) {
  return renderToStaticMarkup(<ToolMessageView toolMessage={buildTask(output, argumentsValue)} />);
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

test("active Task Program keeps live tool state in its row without a stacked stream section", () => {
  const program = {
    id: "release_program",
    stages: [
      { id: "research", dependency_evidence: "Inputs are independent." },
      { id: "implement", depends_on: ["research"], dependency_evidence: "Uses research handoffs." },
    ],
    jobs: [
      { id: "scan", stage_id: "research", agent_type: "finder", title: "Scan contracts" },
      { id: "desktop", stage_id: "implement", depends_on: ["scan"], agent_type: "coder", title: "Build Desktop" },
    ],
  };
  const message = buildTask({
    tool: "task",
    path_id: "tool.task.v1",
    status: "running",
    program_id: "release_program",
    program_state: "running",
    active_stage_id: "research",
    program_status: {
      program_id: "release_program",
      program_state: "running",
      active_stage_id: "research",
      jobs: [
        { job_id: "scan", stage_id: "research", state: "running", child_session_id: "scan-child" },
        { job_id: "desktop", stage_id: "implement", state: "declared" },
      ],
    },
    launches: [{
      launch_index: 1,
      child_session_id: "scan-child",
      subagent: "finder",
      assignment_label: "Scan contracts",
      status: "running",
      current_tool: "search",
      current_tool_display: "search x2",
      tool_order: ["read", "search", "search"],
      source_arguments: { program_id: "release_program", program_job_id: "scan", program_stage_id: "research" },
    }],
  }, { action: "spawn", program });
  const markup = renderToStaticMarkup(<ToolMessageView toolMessage={message} />);

  assert.equal((markup.match(/data-task-program-card=/g) ?? []).length, 1);
  assert.match(markup, /data-task-program-card="release_program"/);
  assert.match(markup, /aria-expanded="true"/);
  assert.match(markup, /Task Program/);
  assert.match(markup, /0\/2 jobs · 0\/2 phases/);
  assert.equal((markup.match(/data-task-program-stage=/g) ?? []).length, 2);
  assert.match(markup, /data-stage-state="active"/);
  assert.match(markup, /data-stage-state="waiting"/);
  assert.match(markup, /waiting on dependencies/);
  assert.match(markup, /search x2/);
  assert.match(markup, />RUNNING</);
  assert.doesNotMatch(markup, /data-task-live-stream=/);
  assert.doesNotMatch(markup, /data-task-live-tools/);
  assert.doesNotMatch(markup, /data-task-live-assistant/);
  assert.doesNotMatch(markup, /tools:/i);
  assert.doesNotMatch(markup, /ITERATION SWARM/);
});

test("Task Program source exposes ordered phases and interactive rows without a stacked stream section", async () => {
  const source = await readFile(new URL("./chat-markdown.tsx", import.meta.url), "utf8");
  assert.match(source, /data-task-program-expanded/);
  assert.match(source, /program\.nextAction/);
  assert.match(source, /program\.stages\.map\(\(stage, stageIndex\)/);
  assert.match(source, /data-task-program-stage=\{stage\.id\}/);
  assert.match(source, /waiting on dependencies/);
  assert.match(source, /Dependencies: \{dependencyLabel\}/);
  assert.match(source, /MemoizedTaskAgentListRow/);
  assert.match(source, /const toolLabel = taskActivityLabel\(row\)/);
  assert.match(source, /\{toolLabel\}/);
  assert.doesNotMatch(source, /data-task-live-stream/);
  assert.doesNotMatch(source, /data-task-live-tools/);
  assert.doesNotMatch(source, /data-task-live-assistant/);
});

test("ordinary task rows do not render a Task Program card", () => {
  const markup = renderTask({
    tool: "task",
    path_id: "tool.task.v1",
    status: "running",
    launches: [{
      launch_index: 1,
      child_session_id: "ordinary",
      subagent: "finder",
      assignment_label: "Phase-like title",
      status: "running",
      current_tool: "read",
      current_tool_display: "read x3",
      tool_order: ["search", "read", "read", "read"],
    }],
  });
  assert.match(markup, /Subagent stream/);
  assert.match(markup, /read x3/);
  assert.match(markup, />RUNNING</);
  assert.doesNotMatch(markup, /search\s+read\s+read/);
  assert.doesNotMatch(markup, /data-task-live-stream=/);
  assert.doesNotMatch(markup, /data-task-program-card=/);
});

test("Iteration Swarm remains authoritative when unrelated program-shaped metadata is present", () => {
  const program = {
    id: "ignored_program",
    stages: [{ id: "only" }],
    jobs: [{ id: "worker", stage_id: "only", agent_type: "finder", title: "Worker" }],
  };
  const markup = renderTask({
    tool: "task",
    path_id: "tool.task.v1",
    task_mode: "swarm",
    program_id: "ignored_program",
    launches: [{ launch_index: 1, swarm_mode: true, subagent: "finder", status: "running" }],
  }, { mode: "swarm", program });

  assert.match(markup, /ITERATION SWARM/);
  assert.doesNotMatch(markup, /data-task-program-card=/);
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
