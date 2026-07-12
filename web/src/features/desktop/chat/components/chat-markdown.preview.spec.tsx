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
  return renderToStaticMarkup(<ToolMessageView toolMessage={toolMessage} />);
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

function testTaskSwarmUsesCompactPreview(): void {
  const longAssignment = "Coordinate an extremely detailed research and implementation assignment that would normally push the desktop task header sideways";
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_swarm",
    outputText: JSON.stringify({
      tool: "task",
      description: longAssignment,
      launch_count: 12,
      launches: Array.from({ length: 12 }, (_, index) => ({
        launch_index: index + 1,
        child_session_id: `child-session-${index + 1}`,
        status: index % 3 === 0 ? "running" : "done",
        resolved_agent_name: index % 2 === 0 ? "explorer" : "parallel",
        assignment_label: `${longAssignment} ${index + 1}`,
        current_tool: index % 3 === 0 ? "search" : "read",
        current_tool_ms: 1200 + index,
      })),
    }),
  });
  assert(Boolean(message), "expected structured task swarm message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Swarm mode"), "expected desktop swarm mode header");
  assert(markup.includes("@explorer"), "expected compact row agent label");
  assert(markup.includes("search"), "expected compact row current tool");
  assert(markup.includes("RUN"), "expected compact row status");
  assert(!markup.includes("Subagent stream"), "swarm mode should not render normal task table header");
  assert(!markup.includes("Current"), "swarm mode should not render detailed current column header");
  assert(!markup.includes("child child-session"), "swarm mode should not render child session ids");
  assert(!markup.includes(`task ${longAssignment}`), "swarm mode should suppress long task header summary");
}

function testTaskRunningTimerUsesStartTimestamp(): void {
  const startedAt = Date.now() - 2400;
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_running_timer",
    outputText: JSON.stringify({
      tool: "task",
      description: "Map backend timer behavior",
      launch_count: 1,
      status: "running",
      launches: [{
        launch_index: 1,
        status: "running",
        resolved_agent_name: "explorer",
        assignment_label: "Backend timer mapper",
        current_tool: "search",
        launch_started_at_ms: startedAt,
        current_tool_ms: 999999,
        elapsed_ms: 999999,
      }],
    }),
    state: "running",
  });

  assert(Boolean(message), "expected running task message");
  assert(message!.taskRows[0]?.terminal === false, "running row must not be terminal");
  assert(message!.taskRows[0]?.time === "", `running row should not use backend duration text: ${message!.taskRows[0]?.time}`);
  const markup = renderToolMarkup(message!);
  assert(markup.includes("RUN"), "expected running status");
  assert(markup.includes("search"), "expected current tool");
  assert(!markup.includes("999.999") && !markup.includes("1000.0s"), "running timer must ignore stream-coupled duration fields");
}

function testTaskTerminalTimerUsesFinalElapsed(): void {
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_terminal_timer",
    outputText: JSON.stringify({
      tool: "task",
      description: "Map backend timer behavior",
      launch_count: 1,
      status: "ok",
      launches: [{
        launch_index: 1,
        child_session_id: "child-visible-regression",
        status: "ok",
        resolved_agent_name: "explorer",
        assignment_label: "Backend timer mapper",
        elapsed_ms: 3400,
      }],
    }),
  });

  assert(Boolean(message), "expected terminal task message");
  assert(message!.taskRows[0]?.terminal === true, "ok row must be terminal");
  const markup = renderToolMarkup(message!);
  assert(markup.includes("3.4s"), "expected final elapsed duration");
  assert(!markup.includes("child child-visible"), "task rows should not render child session ids");
}

function testBashToolUsesDedicatedFullWidthCard(): void {
  const command = "for i in {1..80}; do echo line-$i; done";
  const output = Array.from({ length: 80 }, (_, index) => `line-${index + 1}`).join("\n");
  const message = buildStructuredToolMessage({
    tool: "bash",
    callId: "call_bash_card",
    argumentsText: JSON.stringify({ command }),
    outputText: JSON.stringify({ command, stdout: output, exit_code: 0 }),
    durationMs: 1200,
  });
  assert(Boolean(message), "expected structured bash message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Copy"), "bash card should include copy control");
  assert(markup.includes("max-height"), "bash output should render in a bounded scroll container");
  assert(markup.includes("overflow-auto"), "bash output should be scrollable");
  assert(markup.includes("line-80"), "bash card should render final output line without data truncation");
  assert(!markup.includes("odd:bg"), "bash output should not use striped preview rows");
}

function testManageSessionsUsesRelativeDesktopNavigation(): void {
  const sessionId = "session-123";
  const href = `/workspace-abc/${sessionId}`;
  const message = buildStructuredToolMessage({
    tool: "manage_sessions",
    callId: "call_manage_sessions_navigation",
    outputText: JSON.stringify({
      action: "get",
      id: sessionId,
      title: "Durable session title",
      updated_at: 1783769434173,
      state: "needs_review",
      navigation: {
        kind: "session",
        session_id: sessionId,
        workspace_slug: "workspace-abc",
        href,
      },
    }),
  });
  assert(Boolean(message), "expected structured manage_sessions message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes(`href="${href}"`), "expected canonical relative session href");
  assert(markup.includes("Durable session title"), "expected durable title as the visible session name");
  assert(markup.includes(sessionId), "expected session id as secondary metadata");
  assert(markup.includes("Open session"), "expected internal session action");
  assert(markup.includes("needs review"), "expected normalized session state");
  assert(!markup.includes("http://") && !markup.includes("https://"), "must not synthesize an absolute origin");
  assert(!markup.includes('target="_blank"'), "relative Desktop session navigation should stay internal");
}

function testManageSessionsDeployRendersNavigableResultsAndHonestFailures(): void {
  const message = buildStructuredToolMessage({
    tool: "manage-sessions",
    callId: "call_manage_sessions_deploy_card",
    argumentsText: JSON.stringify({ action: "deploy" }),
    outputText: JSON.stringify({
      action: "deploy",
      results: [
        { proposal_id: "proposal-1", session_id: "session-deploy-1", title: "Approved primary", status: "started", mode: "auto", navigation: { session_id: "session-deploy-1", href: "/workspace/session-deploy-1" } },
        { proposal_id: "proposal-2", title: "Approved extra", status: "error", error: "worktree allocation failed" },
      ],
    }),
  });
  assert(Boolean(message), "expected deploy tool message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes("Session deployment") && markup.includes("Approved primary"), "expected deployment result card");
  assert(markup.includes('href="/workspace/session-deploy-1"') && markup.includes("Open session"), "expected navigable deployed session");
  assert(markup.includes("proposal-2 failed:") && markup.includes("worktree allocation failed"), "expected honest per-item failure");
  assert(!markup.includes('results":'), "raw deployment JSON must not render");
}

function testManageSessionsListRendersCardsWithoutRawJson(): void {
  const message = buildStructuredToolMessage({
    tool: "manage-sessions",
    callId: "call_manage_sessions_list_card",
    argumentsText: JSON.stringify({ action: "list" }),
    outputText: JSON.stringify({
      action: "list",
      has_more: true,
      items: [{ id: "session-list-1", title: "Release readiness", message_count: 18, workspace_name: "Swarm", navigation: { session_id: "session-list-1", href: "/swarm-workspace/session-list-1" } }],
    }),
  });
  assert(Boolean(message), "expected list tool message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes("Your sessions") && markup.includes("Release readiness"), "expected list card and durable title");
  assert(markup.includes("More available"), "expected bounded pagination affordance");
  assert(!markup.includes("&quot;items&quot;"), "raw JSON preview must be hidden behind the card");
}

function testTaskHeaderDoesNotShiftBetweenLaunchAssignments(): void {
  const description = "Map backend and UI task stream behavior";
  const firstMessage = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_stable_title",
    outputText: JSON.stringify({
      tool: "task",
      description,
      launch_count: 3,
      status: "running",
      assignment_label: "Backend explorer title",
      launches: [{
        launch_index: 1,
        status: "running",
        resolved_agent_name: "explorer",
        assignment_label: "Backend explorer title",
        current_tool: "search",
      }],
    }),
    state: "running",
  });
  const secondMessage = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_stable_title",
    outputText: JSON.stringify({
      tool: "task",
      description,
      launch_count: 3,
      status: "running",
      assignment_label: "Frontend explorer title",
      launches: [{
        launch_index: 2,
        status: "running",
        resolved_agent_name: "parallel",
        assignment_label: "Frontend explorer title",
        current_tool: "read",
      }],
    }),
    state: "running",
  });

  assert(Boolean(firstMessage), "expected first task message");
  assert(Boolean(secondMessage), "expected second task message");
  assert(firstMessage!.summary === secondMessage!.summary, `task header shifted: ${firstMessage!.summary} -> ${secondMessage!.summary}`);
  assert(firstMessage!.summary === `task ${description} (3 launches) (running)`, "expected task summary to use stable parent description only");

  const firstMarkup = renderToolMarkup(firstMessage!);
  const secondMarkup = renderToolMarkup(secondMessage!);
  assert(firstMarkup.includes(description), "expected stable description in first header");
  assert(secondMarkup.includes(description), "expected stable description in second header");
  assert(!firstMarkup.includes("Backend explorer title</span><svg"), "first header should not end with launch assignment title");
  assert(!secondMarkup.includes("Frontend explorer title</span><svg"), "second header should not end with launch assignment title");
}

function main(): void {
  testDeniedExitPlanPermissionUsesFlatPreview();
  testPlanManageUsesFlatPreview();
  testTaskSwarmUsesCompactPreview();
  testTaskRunningTimerUsesStartTimestamp();
  testTaskTerminalTimerUsesFinalElapsed();
  testBashToolUsesDedicatedFullWidthCard();
  testManageSessionsUsesRelativeDesktopNavigation();
  testManageSessionsDeployRendersNavigableResultsAndHonestFailures();
  testManageSessionsListRendersCardsWithoutRawJson();
  testTaskHeaderDoesNotShiftBetweenLaunchAssignments();
  console.log("chat-markdown preview tests passed");
}

main();
