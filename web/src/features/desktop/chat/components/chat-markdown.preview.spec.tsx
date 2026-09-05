import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { TASK_ELAPSED_TICK_MS, SearchReadToolGroupView, ToolMessageView, bashCopyText, indexBashOutput, taskActivityLabel, taskSwarmLayout } from "./chat-markdown";
import { buildStructuredToolMessage } from "../services/tool-message";

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

function renderToolMarkup(toolMessage: NonNullable<ReturnType<typeof buildStructuredToolMessage>>): string {
  return renderToStaticMarkup(<ToolMessageView toolMessage={toolMessage} />);
}

function testAcceptedAskUserResponseRendersOnTimeline(): void {
  const choice = buildStructuredToolMessage({
    tool: "ask_user",
    callId: "call_ask_choice",
    argumentsText: JSON.stringify({ question: "Which environment?", options: [{ label: "Staging", value: "staging" }, { label: "Production", value: "production" }] }),
    outputText: JSON.stringify({ tool: "ask_user", status: "answered", question: "Which environment?", answer: "production" }),
  });
  assert(Boolean(choice), "expected structured ask-user choice message");
  const choiceMarkup = renderToolMarkup(choice!);
  assert(choiceMarkup.includes("Selected: Production"), "timeline should show the accepted predefined choice");

  const custom = buildStructuredToolMessage({
    tool: "ask_user",
    callId: "call_ask_custom",
    argumentsText: JSON.stringify({ question: "What should it say?", options: ["Short", "Detailed"] }),
    outputText: JSON.stringify({ tool: "ask_user", status: "answered", question: "What should it say?", answer: "Use the customer-facing name" }),
  });
  assert(Boolean(custom), "expected structured ask-user custom response message");
  const customMarkup = renderToolMarkup(custom!);
  assert(customMarkup.includes("Custom response: Use the customer-facing name"), "timeline should show the accepted custom response");
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

function testPlanManageUsesMinimalTransitionView(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_manage_start",
    argumentsText: JSON.stringify({ action: "start_checkpoint", checkpoint_id: "cp-2" }),
    outputText: JSON.stringify({
      tool: "plan_manage",
      action: "start_checkpoint",
      status: "ok",
      execution_summary: {
        active_checkpoint_id: "cp-2",
        next_checkpoint_status: "in_progress",
      },
      plan: {
        id: "plan_123",
        title: "Implementation Plan",
      },
    }),
  });
  assert(Boolean(message), "expected structured plan_manage message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("data-plan-tool-transition"), "expected dedicated plan transition treatment");
  assert(markup.includes("Checkpoint started"), "expected lifecycle action label");
  assert(markup.includes("Checkpoint 2"), "expected friendly checkpoint identity");
  assert(!markup.includes("cp-2"), "raw checkpoint id must not render");
  assert(markup.includes("in progress"), "expected transition status");
  assert(!markup.includes("action: start checkpoint"), "raw preview rows should not render");
  assert(markup.includes("rounded-xl"), "plan transition should render as a minimal card");
  assert(markup.includes("border border-[var(--app-border)]"), "plan transition card should keep a quiet theme border");
  assert(!markup.includes("border-l-2"), "plan transition should not return to the left-rail treatment");
}

function testAcceptedPlanShowsStartedPlanMetadata(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_manage_accepted",
    argumentsText: JSON.stringify({ action: "request_new_plan", title: "First Test Plan" }),
    outputText: JSON.stringify({
      tool: "plan_manage",
      action: "request_new_plan",
      status: "ok",
      execution_summary: { active_checkpoint_id: "cp-1", next_checkpoint_status: "in_progress" },
      plan: {
        id: "plan-1",
        title: "First Test Plan",
        document: {
          title: "First Test Plan",
          active_checkpoint_id: "cp-1",
          checkpoints: [{ id: "cp-1", order: 1, title: "Run First Test Checkpoint", status: "pending" }],
        },
      },
    }),
  });
  assert(Boolean(message), "expected accepted plan_manage message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Plan started"), "accepted plan should show the started lifecycle");
  assert(markup.includes("First Test Plan"), "accepted plan should show the plan title");
  assert(markup.includes("Checkpoint 1"), "accepted plan should show a friendly checkpoint number");
  assert(markup.includes("Run First Test Checkpoint"), "accepted plan should show checkpoint metadata");
  assert(markup.includes("1 checkpoint"), "accepted plan should show checkpoint count metadata");
  assert(markup.includes("in progress"), "started checkpoint should use the execution status instead of stale pending document state");
  assert(!markup.includes("request new plan") && !markup.includes("request_new_plan"), "accepted plan must not show the request action");
  assert(!markup.includes("cp-1") && !markup.includes("CP-1"), "accepted plan must not show raw checkpoint ids");
}

function testPlanManageFollowupUsesCanonicalCheckpointMetadata(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_manage_followup",
    argumentsText: JSON.stringify({ action: "request_followup_checkpoint" }),
    outputText: JSON.stringify({
      tool: "plan_manage",
      action: "request_followup_checkpoint",
      status: "ok",
      checkpoint_id: "followup-2",
      next_checkpoint_id: "followup-2",
      execution_summary: {
        active_checkpoint_id: "cp-stale",
        next_checkpoint_id: "followup-2",
        next_checkpoint_status: "in_progress",
      },
      plan: {
        id: "plan-1",
        title: "Stale plan title",
        document: {
          active_checkpoint_id: "cp-stale",
          checkpoints: [
            { id: "cp-stale", title: "Prior checkpoint", status: "completed" },
            { id: "followup-2", title: "Fresh follow-up checkpoint", status: "pending" },
          ],
        },
      },
    }),
  });
  assert(Boolean(message), "expected follow-up plan_manage message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Checkpoint added"), "expected follow-up transition label");
  assert(markup.includes("Checkpoint 2"), "expected returned checkpoint number");
  assert(!markup.includes("followup-2"), "raw checkpoint id must not render");
  assert(markup.includes("Fresh follow-up checkpoint"), "expected canonical checkpoint title");
  assert(markup.includes("pending"), "expected canonical pending status");
  assert(!markup.includes("Prior checkpoint") && !markup.includes("Stale plan title"), "stale active checkpoint and plan metadata must not render");
  assert(!markup.includes("in progress"), "summary-wide status must not override the returned checkpoint status");
}

function testPlanManageSubtaskUsesCanonicalUpdatedMetadata(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_manage_subtask",
    argumentsText: JSON.stringify({ action: "complete_subtask", checkpoint_id: "cp-2", subtask_id: "task-2" }),
    outputText: JSON.stringify({
      tool: "plan_manage",
      action: "complete_subtask",
      status: "ok",
      checkpoint_id: "cp-2",
      execution_summary: {
        active_checkpoint_id: "cp-1",
        next_checkpoint_status: "in_progress",
      },
      plan: {
        id: "plan-1",
        title: "Plan title",
        document: {
          active_checkpoint_id: "cp-1",
          checkpoints: [
            { id: "cp-1", title: "Stale active checkpoint", status: "in_progress" },
            {
              id: "cp-2",
              title: "Canonical checkpoint",
              status: "in_progress",
              subtasks: [
                { id: "task-1", title: "Unrelated task", status: "pending" },
                { id: "task-2", title: "Canonical updated task", status: "completed" },
              ],
            },
          ],
        },
      },
    }),
  });
  assert(Boolean(message), "expected subtask plan_manage message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Task completed"), "expected subtask lifecycle label");
  assert(markup.includes("Checkpoint 2") && markup.includes("Canonical checkpoint"), "expected affected checkpoint metadata");
  assert(!markup.includes("cp-2"), "raw checkpoint id must not render");
  assert(markup.includes("Canonical updated task") && markup.includes("completed"), "expected canonical updated subtask title and status");
  assert(!markup.includes("Unrelated task") && !markup.includes("Stale active checkpoint"), "unaffected stale metadata must not render");
}

function testTaskSwarmLayoutProgressivelyCompacts(): void {
  const eleven = taskSwarmLayout(11, 420, 720);
  const twelve = taskSwarmLayout(12, 420, 720);
  const twentyFive = taskSwarmLayout(25, 420, 720);
  const twentySix = taskSwarmLayout(26, 420, 720);
  const fifty = taskSwarmLayout(50, 420, 720);
  const fiftyOne = taskSwarmLayout(51, 420, 720);
  const seventyFive = taskSwarmLayout(75, 420, 720);
  const seventySix = taskSwarmLayout(76, 420, 720);
  const hundred = taskSwarmLayout(100, 420, 720);
  const hundredOne = taskSwarmLayout(101, 420, 720);

  assert(eleven.stage === 11 && eleven.density === "detailed", "eleven agents should preserve the spacious box treatment");
  assert(twelve.stage === 25 && twentyFive.stage === 25 && twelve.density === "compact", "12–25 agents should share one stable compact stage");
  assert(twentySix.stage === 50 && fifty.stage === 50 && twentySix.density === "micro", "26–50 agents should share the next stacking stage");
  assert(fiftyOne.stage === 75 && seventyFive.stage === 75 && fiftyOne.density === "dense", "51–75 agents should share the dense stacking stage");
  assert(seventySix.stage === 100 && hundred.stage === 100 && seventySix.density === "signal", "76–100 agents should share the final bounded stage");
  assert(eleven.columns < twelve.columns && twelve.columns < twentySix.columns && twentySix.columns < fiftyOne.columns && fiftyOne.columns < seventySix.columns, "each boundary should add columns progressively rather than flip layouts");
  assert(hundredOne.stage === 101 && hundredOne.maxHeight !== undefined, "only swarms above one hundred should become vertically bounded and scrollable");
}

function testIdeaSwarmUsesSharedModelHeaderAndGenericAgentLabels(): void {
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_idea_swarm",
    argumentsText: JSON.stringify({ mode: "swarm", agent_type: "idea", count: 2, prompt: "Name this feature" }),
    outputText: JSON.stringify({
      tool: "task",
      task_mode: "swarm",
      launch_count: 2,
      launches: [1, 2].map((index) => ({
        launch_index: index,
        subagent: "idea",
        assignment_label: `Idea swarm ${index}`,
        subagent_provider: "codex",
        subagent_model: "gpt-5.6-sol",
        status: "done",
      })),
    }),
  });
  assert(Boolean(message), "expected Idea swarm message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("IDEA SWARM"), "Idea swarm should identify its mode in the shared header");
  assert(markup.includes("codex/gpt-5.6-sol"), "Idea swarm should show the shared provider/model in the header");
  assert(markup.includes("Agent #1") && markup.includes("Agent #2"), "Idea swarm rows should use generic Agent labels");
  assert(!markup.includes("Idea swarm 1") && !markup.includes("Idea swarm 2"), "Idea swarm implementation labels should not leak into rows");
}

function testTaskSwarmUsesCompactPreview(): void {
  const longAssignment = "Coordinate an extremely detailed research and implementation assignment that would normally push the desktop task header sideways";
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_swarm",
    outputText: JSON.stringify({
      tool: "task",
      task_mode: "swarm",
      description: longAssignment,
      launch_count: 12,
      launches: Array.from({ length: 12 }, (_, index) => ({
        launch_index: index + 1,
        child_session_id: `child-session-${index + 1}`,
        status: index % 3 === 0 ? "running" : "done",
        resolved_agent_name: index % 2 === 0 ? "finder" : "parallel",
        assignment_label: `${longAssignment} ${index + 1}`,
        current_tool: index % 3 === 0 ? "search" : "read",
        current_tool_ms: 1200 + index,
      })),
    }),
  });
  assert(Boolean(message), "expected structured task swarm message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("ITERATION SWARM"), "legacy explore payload should render as Iteration Swarm");
  assert((markup.match(/ITERATION SWARM/g) ?? []).length === 1, "Iteration Swarm heading should render once");
  assert(markup.includes("Fast parallel iterations"), "Iteration Swarm should describe fast parallel iteration");
  assert(!markup.includes("12 AI"), "swarm mode should omit the redundant AI population badge");
  assert(!markup.includes("finder"), "swarm rows should not show provider or agent metadata");
  assert(markup.includes("search"), "12–25 agent rows should retain the current tool");
  assert(markup.includes("RUN"), "expected compact row status");
  assert(!markup.includes("Subagent stream"), "swarm mode should replace the regular stream heading");
  assert(!markup.includes("Current"), "swarm mode should not render detailed current column header");
  assert(!markup.includes("child child-session"), "swarm mode should not render child session ids");
  assert(!markup.includes(`task ${longAssignment}`), "swarm mode should suppress long task header summary");

  const regularMessage = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_regular_many",
    outputText: JSON.stringify({
      tool: "task",
      task_mode: "regular",
      launch_count: 12,
      launches: Array.from({ length: 12 }, (_, index) => ({
        launch_index: index + 1,
        child_session_id: `regular-child-${index + 1}`,
        status: "done",
        resolved_agent_name: "finder",
        assignment_label: `Regular task ${index + 1}`,
      })),
    }),
  });
  assert(Boolean(regularMessage), "expected regular task message");
  assert(!renderToolMarkup(regularMessage!).includes("ITERATION SWARM"), "large regular waves must not become swarm mode by count");
}

function testAssemblySwarmShowsPartsAndParentIntegrationRequirement(): void {
  const message = buildStructuredToolMessage({
    tool: "task",
    callId: "call_assembly_swarm",
    outputText: JSON.stringify({
      tool: "task",
      path_id: "tool.task.v1",
      task_mode: "swarm",
      swarm_strategy: "assembly",
      integration_contract: "Combine committed parts into the parent deliverable.",
      integration_required: true,
      integration_status: "pending_parent_assembly",
      ready_for_dependent_work: false,
      launches: [{
        launch_index: 1,
        child_session_id: "part-1",
        swarm_mode: true,
        swarm_strategy: "assembly",
        assembly_part: { name: "Backend API", owned_scope: ["swarmd/internal/api/**"] },
        integration_contract: "Combine committed parts into the parent deliverable.",
        integration_required: true,
        subagent: "coder",
        status: "done",
      }],
    }),
  });
  assert(Boolean(message), "expected Assembly swarm message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes("ASSEMBLY SWARM"), "Assembly swarm should use the explicit Assembly label");
  assert(markup.includes("Complementary parts"), "Assembly swarm should describe workers as parts");
  assert(markup.includes("parent integration required"), "completed Assembly children must retain the parent integration obligation");
  assert(markup.includes("Contract: Combine committed parts into the parent deliverable."), "Assembly contract should be visible");
  assert(markup.includes("Backend API"), "Assembly part identity should label the worker row");
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
        resolved_agent_name: "finder",
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
        resolved_agent_name: "finder",
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

function testBashCopyUsesOutputOnly(): void {
  const output = "public-result\nsecond-line";

  const copyPayload = bashCopyText(output);
  assert(copyPayload === output, "bash copy should preserve only the exact command output");
}

function testBashOutputIndexBoundsPreviewWithoutChangingCanonicalOutput(): void {
  const output = Array.from({ length: 500 }, (_, index) => `line-${index + 1}-${"x".repeat(300)}`).join("\n");
  const index = indexBashOutput(output);

  assert(index.lineStarts.length === 500, "expected every exact output line to remain addressable");
  assert(index.preview.length <= 32 * 1024, "collapsed preview must remain bounded independently of output size");
  assert(index.preview !== output, "large collapsed output must not mount the complete canonical output");
  assert(output.endsWith(index.preview), "collapsed preview must be an exact suffix of canonical output");
  assert(index.canExpand, "large output must expose the exact full-output viewer");
  assert(bashCopyText(output) === output, "copy-all must preserve canonical output byte-for-byte");
}

function testRunningBashUsesDedicatedStreamingCard(): void {
  const command = "for i in 1 2 3; do echo line-$i; sleep 1; done";
  const startedMessage = buildStructuredToolMessage({
    tool: "bash",
    callId: "call_bash_started",
    argumentsText: JSON.stringify({ command }),
    state: "running",
    lifecycleStatus: "started",
  });
  const streamingMessage = buildStructuredToolMessage({
    tool: "bash",
    callId: "call_bash_streaming",
    argumentsText: JSON.stringify({ command }),
    outputText: "line-1",
    state: "running",
    lifecycleStatus: "running",
  });
  assert(Boolean(startedMessage && streamingMessage), "expected running bash tool messages");

  const startedMarkup = renderToolMarkup(startedMessage!);
  const streamingMarkup = renderToolMarkup(streamingMessage!);
  assert(startedMarkup.includes("Waiting for output…"), "tool.start should render the bash card before output arrives");
  assert(startedMarkup.includes("running"), "tool.start should identify bash as running");
  assert(streamingMarkup.includes("line-1"), "streaming bash output should render immediately");
  assert(streamingMarkup.includes("streaming"), "bash should visibly identify active streamed output");
  assert(streamingMarkup.includes('aria-label="Copy Bash output"'), "streaming bash should keep its output controls");
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
  assert(markup.includes('aria-label="Copy Bash output"'), "bash copy control should identify output as its only payload");
  assert(markup.includes('aria-label="Download exact Bash output"'), "bash card should expose exact output download");
  assert(markup.includes('data-bash-output="bounded-preview"'), "collapsed bash output should use the bounded preview path");
  assert(markup.includes("View all"), "large bash output should expose the exact full-output viewer");
  assert(markup.includes("max-height"), "bash output should render in a bounded scroll container");
  assert(markup.includes("50vh") || markup.includes("max-height:"), "bash output should expose a bounded viewport height");
  assert(markup.includes("overflow-y-auto"), "bash output should be vertically scrollable");
  assert(markup.includes("overflow-x-hidden"), "bash output should not overflow horizontally");
  assert(markup.includes("line-80"), "bash card should render final output line without data truncation");
  assert(!markup.includes("odd:bg"), "bash output should not use striped preview rows");
}

function testManageThemeBatchShowsGeneratedMetadata(): void {
  const message = buildStructuredToolMessage({
    tool: "manage-theme",
    callId: "call_manage_theme_batch",
    argumentsText: JSON.stringify({ action: "create_batch" }),
    outputText: JSON.stringify({
      status: "ok",
      action: "create_batch",
      generated_count: 3,
      generated_names: ["Dawn", "Dusk", "Aurora"],
      summary: "generated 3 themes: Dawn, Dusk, Aurora",
    }),
  });
  assert(Boolean(message), "expected structured manage-theme message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("theme") && markup.includes("generated 3 themes: Dawn, Dusk, Aurora"), "expected generated count and names in the visible tool header");
  assert(markup.includes("Generated 3 themes.") && markup.includes("Dawn") && markup.includes("Dusk") && markup.includes("Aurora"), "expected concise result metadata lines");
  assert(!markup.includes("&quot;generated_count&quot;"), "manage-theme should not expose raw result JSON");
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
  assert(!markup.includes('href="http://') && !markup.includes('href="https://'), "must not synthesize an absolute navigation origin");
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

function testManageSessionsReviewWorktreesHydratesCandidates(): void {
  const longSubject = `Compact a very long worktree commit subject ${"x".repeat(180)}`;
  const message = buildStructuredToolMessage({
    tool: "manage-sessions",
    callId: "call_manage_sessions_review_worktrees",
    argumentsText: JSON.stringify({ action: "review_worktrees" }),
    outputText: JSON.stringify({
      action: "review_worktrees",
      needs_review_count: 3,
      worktree_session_count: 2,
      follow_up_candidate_count: 1,
      archive_candidate_count: 1,
      inspection_error_count: 0,
      follow_up_candidates: [{
        session_id: "session-follow-up",
        title: "Search rendering worktree",
        worktree_branch: "agent/search-rendering",
        classification: "follow_up",
        clean: false,
        dirty_count: 2,
        missing_commit_count: 2,
        missing_commits_truncated: true,
        missing_commits: [{ subject: "Group results by file" }, { subject: longSubject }],
        navigation: { session_id: "session-follow-up", href: "/swarm/session-follow-up" },
      }],
      archive_candidates: [{
        session_id: "session-archive",
        title: "Integrated tool card",
        worktree_branch: "agent/integrated-tool-card",
        classification: "archive_candidate",
        clean: true,
        dirty_count: 0,
        missing_commit_count: 0,
      }],
    }),
  });
  assert(Boolean(message), "expected review_worktrees tool message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Worktrees needing review"), "expected review-worktrees title");
  assert(markup.includes("3 total · 2 worktrees · 1 follow up · 1 archive ready"), "expected review count summary");
  assert(markup.includes("Search rendering worktree") && markup.includes("Integrated tool card"), "expected both candidate groups to hydrate");
  assert(markup.includes("agent/search-rendering") && markup.includes("Dirty · 2 changes") && markup.includes("2 missing commits"), "expected compact worktree metadata");
  assert(markup.includes("Group results by file") && markup.includes("line-clamp-2"), "expected concise commit subjects");
  assert(markup.includes('href="/swarm/session-follow-up"') && markup.includes("Open session"), "expected safe internal session link");
  assert(!markup.includes("Session details"), "review_worktrees must not fall back to the generic empty card");
}

function testSearchToolRendersSimpleSummary(): void {
  const message = buildStructuredToolMessage({
    tool: "search",
    callId: "call_simple_search_render",
    argumentsText: JSON.stringify({ query: "compactSearchResult", path: "web/src" }),
    outputText: JSON.stringify({
      tool: "search",
      search_mode: "content",
      path: "web/src",
      count: 2,
      total_matched: 8,
      truncated: true,
      query_results: [{ query: "compactSearchResult" }],
      results: [{
        path: "web/src/features/desktop/chat-markdown.tsx",
        items: [
          { line: 88, column: 4, text: "const compactSearchResult = true" },
          { line: 93, column: 7, text: "return compactSearchResult" },
        ],
      }],
    }),
  });
  assert(Boolean(message), "expected simple search tool message");

  const markup = renderToolMarkup(message!);
  assert(markup.includes("Searched “compactSearchResult” · Found 8 matches · Partial results"), "expected a plain-language searched/found summary");
  assert(markup.includes("web/src/features/desktop/chat-markdown.tsx") && markup.includes("2 matches"), "search cards should expose matched files and counts");
  assert(!markup.includes("88:4") && !markup.includes("93:7"), "line-level results should stay out of the Desktop card");
  assert(!markup.includes("return compactSearchResult"), "match snippets should remain omitted from the compact card");

  const empty = buildStructuredToolMessage({
    tool: "search",
    argumentsText: JSON.stringify({ queries: ["first query", "second query"] }),
    outputText: JSON.stringify({ search_mode: "files", query_count: 2, count: 0, timed_out: true, path: "web/src" }),
  });
  assert(Boolean(empty), "expected zero-result search tool message");
  const emptyMarkup = renderToolMarkup(empty!);
  assert(emptyMarkup.includes("Searched 2 queries · Found 0 files · Timed out"), "expected understandable multi-query, zero-result, and timeout copy");
}

function testSearchActivityKeepsInvestigationLabelAcrossSingleAndGroupedCalls(): void {
  const search = buildStructuredToolMessage({
    tool: "search",
    callId: "call-single-search",
    argumentsText: JSON.stringify({ query: "needle", path: "web/src" }),
    outputText: JSON.stringify({ search_mode: "content", count: 1, total_matched: 1, results: [{ path: "web/src/file.tsx", items: [{ line: 10, column: 2, text: "needle" }] }] }),
  });
  assert(Boolean(search), "expected single search message");
  if (!search) return;
  const markup = renderToolMarkup(search);
  assert(markup.includes("Investigation") && !markup.includes(">Search<"), "single search should use the stable investigation label");
}

function testSearchReadGroupRendersCompactFileAggregation(): void {
  const tools = Array.from({ length: 16 }, (_, index) => buildStructuredToolMessage({
    tool: index % 3 === 0 ? "search" : "read",
    callId: `call-group-${index}`,
    argumentsText: JSON.stringify({ path: `web/src/file-${index}.tsx`, query: "needle" }),
    outputText: index % 3 === 0 ? JSON.stringify({
      search_mode: "content",
      count: 1,
      total_matched: 2,
      results: [{ path: `web/src/file-${index}.tsx`, items: [{ line: 10, column: 2, text: "needle" }, { line: 20, column: 1, text: "needle again" }] }],
    }) : JSON.stringify({ path: `web/src/file-${index}.tsx`, count: 10 }),
  })).filter((message): message is NonNullable<ReturnType<typeof buildStructuredToolMessage>> => Boolean(message));
  const markup = renderToStaticMarkup(<SearchReadToolGroupView toolMessages={tools} />);
  assert(markup.includes('data-search-read-group="true"') && markup.includes("Investigation") && markup.includes("6 searches · 10 reads · 16 files · 12 matches"), "group should keep the investigation label while summarizing consecutive search/read calls");
  assert(markup.includes("web/src/file-0.tsx") && markup.includes("2 matches") && markup.includes("web/src/file-1.tsx") && markup.includes("1 read"), "group should preserve search files and read paths");
  assert(markup.includes("Show 4 more files"), "large groups should cap the initial file list but preserve expandable detail");
}

function testManageSessionsDurableLogRendersTechnicalEvents(): void {
  const message = buildStructuredToolMessage({
    tool: "manage-sessions",
    callId: "call_manage_sessions_durable_log",
    argumentsText: JSON.stringify({ action: "search", search_mode: "durable_log", session_id: "session-1", query: "diagnostic" }),
    outputText: JSON.stringify({
      action: "search", search_mode: "durable_log", source: "durable_v3_session_events", session_id: "session-1",
      events: [{ id: "event-8", session_id: "session-1", seq: 8, event_type: "session.diagnostic.recorded", payload: { message: "API diagnostic" } }],
      scan_truncated: true, character_truncated: false, result_truncated: false,
    }),
  });
  assert(Boolean(message), "expected durable-log tool message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes("Durable event-log search") && markup.includes("durable V3 log"), "expected distinct technical search title");
  assert(markup.includes("session.diagnostic.recorded") && markup.includes("#8") && markup.includes("API diagnostic"), "expected event metadata and payload");
  assert(markup.includes("Source: durable V3 session events") && markup.includes("scan truncated"), "expected source and honest bounds");
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

function testWebSearchUsesDedicatedResponsiveCard(): void {
  const longQuery = `desktop web search ${"very-long-query ".repeat(20)}`;
  const longText = `Complete searchable content ${"result body ".repeat(90)}`;
  const message = buildStructuredToolMessage({
    tool: "websearch",
    argumentsText: JSON.stringify({ queries: [longQuery, "failing query"] }),
    outputText: JSON.stringify({
      queries: [longQuery, "failing query"],
      query_count: 2,
      total_results: 2,
      failed_queries: 1,
      details_truncated: true,
      results: [
        { query: longQuery, count: 2, results: [
          { url: "https://example.com/article", title: "An attractive result", highlights: ["Useful highlight"], text: longText },
          { url: "javascript:alert(1)", title: "Unsafe URL remains text", summary: "No invalid link is created." },
        ] },
        { query: "failing query", count: 0, error: "search backend failed", results: [] },
      ],
    }),
    durationMs: 1320,
  });
  assert(Boolean(message), "expected websearch message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes('data-web-tool-card="websearch"') && markup.includes("Web Search"), "expected dedicated websearch card");
  assert(markup.includes("2 queries") && markup.includes("2 results") && markup.includes("1 failed") && markup.includes("1.3s"), "expected header metadata");
  assert(markup.includes("example.com") && markup.includes("Useful highlight") && markup.includes("Read full preview"), "expected structured result row and expansion affordance");
  assert(markup.includes('href="https://example.com/article"') && markup.includes('target="_blank"') && markup.includes('rel="noopener noreferrer"'), "expected safe external link behavior");
  assert(!markup.includes('href="javascript:') && markup.includes("Unsafe URL remains text"), "unsafe URL must not become a link");
  assert(markup.includes("max-h-[50vh]") && markup.includes("overflow-y-auto") && markup.includes("overflow-x-hidden"), "websearch body must be bounded and overflow safe");
  assert(markup.includes("line-clamp-3") && markup.includes(longText), "visual clamping must preserve full underlying result content");
  assert(!markup.includes("&quot;total_results&quot;"), "websearch card should not expose raw result JSON");
}

function testWebFetchUsesDedicatedFailureAwareCard(): void {
  const longText = `Full fetched text ${"paragraph ".repeat(120)}`;
  const message = buildStructuredToolMessage({
    tool: "webfetch",
    outputText: JSON.stringify({
      urls: ["https://docs.example.com/guide", "bad-url"],
      count: 2,
      success_count: 1,
      timed_out: true,
      results: [
        { url: "https://docs.example.com/guide", title: "Fetched guide", summary: "Readable summary", text: longText },
        { url: "bad-url", title: "Failed page", error: "crawl failed" },
      ],
      statuses: [{ id: "broken", source: "bad-url", status: "error", error: { tag: "CRAWL_FAILED" } }],
    }),
    error: "webfetch returned partial content",
    state: "error",
    durationMs: 900,
  });
  assert(Boolean(message), "expected webfetch message");
  const markup = renderToolMarkup(message!);
  assert(markup.includes('data-web-tool-card="webfetch"') && markup.includes("Web Fetch"), "expected dedicated webfetch card");
  assert(markup.includes("2 URLs") && markup.includes("1 fetched") && markup.includes("1 failed") && markup.includes("timed out"), "expected fetch counts and status");
  assert(markup.includes("Fetched guide") && markup.includes("docs.example.com") && markup.includes("crawl failed") && markup.includes("CRAWL_FAILED"), "expected successful and failed records");
  assert(markup.includes("max-h-[50vh]") && markup.includes("overflow-y-auto") && markup.includes("overflow-x-hidden"), "webfetch body must be bounded and overflow safe");
  assert(markup.includes("Read full preview") && markup.includes(longText), "long fetched content should remain expandable and durable");
  assert(!markup.includes('href="bad-url"'), "invalid fetch URL must not become a link");
}

function testRunningWebCardsShowProgressWithoutRawJson(): void {
  const search = buildStructuredToolMessage({ tool: "websearch", argumentsText: JSON.stringify({ queries: ["one", "two"] }), state: "running" });
  const fetch = buildStructuredToolMessage({ tool: "webfetch", argumentsText: JSON.stringify({ urls: ["https://example.com"] }), state: "running" });
  assert(Boolean(search && fetch), "expected running web tool messages");
  const searchMarkup = renderToolMarkup(search!);
  const fetchMarkup = renderToolMarkup(fetch!);
  assert(searchMarkup.includes("running") && searchMarkup.includes("Searching the web…") && !searchMarkup.includes("&quot;queries&quot;"), "running search should use a structured progress state");
  assert(fetchMarkup.includes("running") && fetchMarkup.includes("Fetching page content…") && !fetchMarkup.includes("&quot;urls&quot;"), "running fetch should use a structured progress state");
}

function testFileActionsUseThemeAwareCards(): void {
  const readMessage = buildStructuredToolMessage({
    tool: "read",
    outputText: JSON.stringify({ path: "web/src/app.tsx", count: 18, line_start: 1 }),
  });
  const listMessage = buildStructuredToolMessage({
    tool: "list",
    outputText: JSON.stringify({
      path: "web/src",
      count: 2,
      total_found: 2,
      entries: [
        { path: "web/src/app.tsx", type: "file" },
        { path: "web/src/features", type: "dir" },
      ],
    }),
  });
  const editMessage = buildStructuredToolMessage({
    tool: "edit",
    argumentsText: JSON.stringify({ path: "web/src/app.tsx" }),
    outputText: JSON.stringify({
      path: "web/src/app.tsx",
      old_string_preview: "const oldValue = true",
      new_string_preview: "const newValue = true",
    }),
  });
  assert(Boolean(readMessage && listMessage && editMessage), "expected file action messages");

  const readMarkup = renderToolMarkup(readMessage!);
  const listMarkup = renderToolMarkup(listMessage!);
  const editMarkup = renderToolMarkup(editMessage!);
  assert(readMarkup.includes("rounded-xl") && readMarkup.includes("web/src/app.tsx"), "read should render a themed file card");
  assert(listMarkup.includes("web/src/features/") && listMarkup.includes("border-t"), "list should place files in a vertical list");
  assert(editMarkup.includes("Changes") && editMarkup.includes("−1") && editMarkup.includes("+1"), "edit should render a restrained diff card");
}

function testTaskCardUsesOneStableStreamHeader(): void {
  const description = "Map backend and UI task stream behavior";
  const firstMessage = buildStructuredToolMessage({
    tool: "task",
    callId: "call_task_stable_title",
    outputText: JSON.stringify({
      tool: "task",
      description,
      launch_count: 3,
      status: "running",
      assignment_label: "Backend finder title",
      launches: [{
        launch_index: 1,
        status: "running",
        resolved_agent_name: "finder",
        assignment_label: "Backend finder title",
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
      assignment_label: "Frontend finder title",
      launches: [{
        launch_index: 2,
        status: "running",
        resolved_agent_name: "parallel",
        assignment_label: "Frontend finder title",
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
  assert(!firstMarkup.includes(description), "task description should not create a repetitive heading above the stream");
  assert(!secondMarkup.includes(description), "updated assignments should not create a repetitive heading above the stream");
  assert((firstMarkup.match(/Subagent stream/g) ?? []).length === 1, "task card should render one subagent stream heading");
  assert(firstMarkup.includes('data-task-tool-card="true"') && firstMarkup.includes('data-task-rows="true"'), "subagent stream rows should remain in the task card");
  assert(firstMarkup.includes("lucide-bot"), "stream header should use the Lucide bot icon");
  assert(firstMarkup.includes("rounded-full"), "stream header bot icon should have a circular treatment");
  assert(!firstMarkup.includes("starting…") && !firstMarkup.includes("animate-spin"), "running task cards should not show the starting spinner treatment");
  assert(!firstMarkup.includes("Backend finder title</span><svg"), "first header should not end with launch assignment title");
  assert(!secondMarkup.includes("Frontend finder title</span><svg"), "second header should not end with launch assignment title");
}

function testTaskActivityPrefersSummaryOnlyForActiveRows(): void {
  const row = buildStructuredToolMessage({
    tool: "task",
    outputText: JSON.stringify({
      tool: "task",
      launches: [{ launch_index: 1, status: "running", current_tool: "search" }],
    }),
    state: "running",
  })!.taskRows[0]!;

  assert(taskActivityLabel({ ...row, toolActivitySummary: "read ×5 · search ×2" }) === "read ×5 · search ×2", "active row should prefer activity summary");
  assert(taskActivityLabel({ ...row, status: "pending", phase: "spawned", tool: "-", toolActivitySummary: "read ×5" }) === "queued", "pending row should retain status fallback");
  assert(taskActivityLabel({ ...row, status: "completed", terminal: true, tool: "-", toolActivitySummary: "read ×5" }) === "done", "terminal row should retain status fallback");
}

function testTaskElapsedClockUsesDisplayCadence(): void {
  assert(TASK_ELAPSED_TICK_MS === 1_000, `expected one-second elapsed cadence, got ${TASK_ELAPSED_TICK_MS}`);
}

function testManageArtifactRendersTypedArtifactCard(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_artifact",
    callId: "call_artifact_test",
    outputText: JSON.stringify({
      tool: "manage_artifact",
      action: "create",
      status: "ok",
      artifact: {
        id: "var-123",
        collection_id: "col-abc",
        session_id: "sess-xyz",
        event_seq: 4,
        filename: "landing.html",
        media_type: "text/html",
        label: "Landing Page Mockup",
        description: "Interactive landing page prototype",
        status: "ready",
        category: "visual",
      },
      reference: {
        session_id: "sess-xyz",
        collection_id: "col-abc",
        variant_id: "var-123",
        event_seq: 4,
      },
    }),
  });
  assert(Boolean(message), "expected manage_artifact message");
  message!.artifactData!.artifact!.localRevealAvailable = true;
  const markup = renderToolMarkup(message!);
  assert(markup.includes('data-testid="desktop-artifact-tool-card"'), "expected artifact tool card testid");
  assert(markup.includes("Landing Page Mockup"), "expected artifact label in markup");
  assert(markup.includes("Interactive landing page prototype"), "expected description in markup");
  assert(markup.includes("text/html"), "expected media type badge");
  assert(markup.includes("Ready"), "expected Ready status badge");
  assert(markup.includes("Open in viewer"), "expected Open in viewer button or link");
  assert(markup.includes("Show in folder"), "local artifact card should expose the recovery working-copy action");
  assert(markup.includes("Download"), "artifact card should retain the remote recovery download action");
}

function main(): void {
  testAcceptedAskUserResponseRendersOnTimeline();
  testDeniedExitPlanPermissionUsesFlatPreview();
  testPlanManageUsesMinimalTransitionView();
  testAcceptedPlanShowsStartedPlanMetadata();
  testPlanManageFollowupUsesCanonicalCheckpointMetadata();
  testPlanManageSubtaskUsesCanonicalUpdatedMetadata();
  testTaskSwarmLayoutProgressivelyCompacts();
  testIdeaSwarmUsesSharedModelHeaderAndGenericAgentLabels();
  testTaskSwarmUsesCompactPreview();
  testAssemblySwarmShowsPartsAndParentIntegrationRequirement();
  testTaskRunningTimerUsesStartTimestamp();
  testTaskTerminalTimerUsesFinalElapsed();
  testTaskElapsedClockUsesDisplayCadence();
  testTaskActivityPrefersSummaryOnlyForActiveRows();
  testBashCopyUsesOutputOnly();
  testBashOutputIndexBoundsPreviewWithoutChangingCanonicalOutput();
  testRunningBashUsesDedicatedStreamingCard();
  testBashToolUsesDedicatedFullWidthCard();
  testManageThemeBatchShowsGeneratedMetadata();
  testManageSessionsUsesRelativeDesktopNavigation();
  testManageSessionsDeployRendersNavigableResultsAndHonestFailures();
  testManageSessionsReviewWorktreesHydratesCandidates();
  testSearchToolRendersSimpleSummary();
  testManageArtifactRendersTypedArtifactCard();
  testSearchActivityKeepsInvestigationLabelAcrossSingleAndGroupedCalls();
  testSearchReadGroupRendersCompactFileAggregation();
  testManageSessionsListRendersCardsWithoutRawJson();
  testManageSessionsDurableLogRendersTechnicalEvents();
  testWebSearchUsesDedicatedResponsiveCard();
  testWebFetchUsesDedicatedFailureAwareCard();
  testRunningWebCardsShowProgressWithoutRawJson();
  testFileActionsUseThemeAwareCards();
  testTaskCardUsesOneStableStreamHeader();
  console.log("chat-markdown preview tests passed");
}

main();
