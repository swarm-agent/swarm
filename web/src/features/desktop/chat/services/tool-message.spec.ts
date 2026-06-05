import { buildStructuredToolMessage, parseStructuredToolMessage } from "./tool-message";

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

function testExitPlanApprovedShowsMetadata(): void {
  const message = buildStructuredToolMessage({
    tool: "exit_plan_mode",
    callId: "call_1",
    outputText: JSON.stringify({
      tool: "exit_plan_mode",
      status: "approved",
      title: "Implementation Plan",
      plan_id: "plan_123",
      target_mode: "auto",
      user_message: "Ship it",
    }),
  });
  assert(Boolean(message), "expected structured tool message");
  assert(
    message?.summary === "plan approved · Implementation Plan",
    `unexpected summary: ${message?.summary}`,
  );
  assert(
    message?.previewLines.includes("action: approved") === true &&
      message?.previewLines.includes("plan: plan_123") === true,
    `missing action/plan id preview: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.includes("next mode: auto") === true,
    `missing target mode preview: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.includes("feedback: Ship it") === true,
    `missing feedback preview: ${message?.previewLines.join(" | ")}`,
  );
}

function testExitPlanDeniedPermissionShowsFeedbackAndPlan(): void {
  const message = buildStructuredToolMessage({
    tool: "permission",
    callId: "call_2",
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
  assert(Boolean(message), "expected structured permission message");
  assert(
    message?.summary === "plan denied · Deployment Plan",
    `unexpected permission summary: ${message?.summary}`,
  );
  assert(
    message?.previewLines.includes("plan: plan_456") === true,
    `missing denied plan id preview: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.includes("feedback: Need rollout notes") === true,
    `missing denied feedback preview: ${message?.previewLines.join(" | ")}`,
  );
}

function testExitPlanDefaultApprovalFeedbackIsHidden(): void {
  const message = buildStructuredToolMessage({
    tool: "exit_plan_mode",
    callId: "call_3",
    outputText: JSON.stringify({
      tool: "exit_plan_mode",
      status: "approved",
      title: "Implementation Plan",
      user_message: "approved by user",
    }),
  });
  assert(
    Boolean(message),
    "expected structured tool message for default approval",
  );
  assert(
    message?.previewLines.some((line) => line.includes("feedback:")) === false,
    `default approval feedback should be hidden: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageTodosListShowsItems(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_todos",
    callId: "call_todos_1",
    outputText: JSON.stringify({
      tool: "manage_todos",
      action: "list",
      owner_kind: "user",
      items: [
        {
          id: "todo_1",
          text: "Ship desktop todo rendering",
          done: false,
          in_progress: true,
          priority: "high",
          group: "ux",
          tags: ["desktop"],
        },
        {
          id: "todo_2",
          text: "Ship tui todo rendering",
          done: true,
          priority: "medium",
          tags: ["tui"],
        },
      ],
    }),
  });
  assert(Boolean(message), "expected structured manage_todos message");
  assert(
    message?.summary === "todo [user] list",
    `unexpected manage_todos summary: ${message?.summary}`,
  );
  assert(
    message?.previewLines[0] === "ux · \#desktop",
    `unexpected first todo metadata preview: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines[1] === "> [ ] Ship desktop todo rendering · high",
    `unexpected first todo body preview: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines[3] === "[x] Ship tui todo rendering · medium",
    `unexpected second todo body preview: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageTodosSummaryShowsCounts(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_todos",
    callId: "call_todos_2",
    outputText: JSON.stringify({
      tool: "manage_todos",
      action: "summary",
      summary: {
        task_count: 5,
        open_count: 3,
        in_progress_count: 1,
        user: { task_count: 2, open_count: 1, in_progress_count: 0 },
        agent: { task_count: 3, open_count: 2, in_progress_count: 1 },
      },
    }),
  });
  assert(Boolean(message), "expected structured manage_todos summary message");
  assert(
    message?.summary === "todo summary (3 open · 5 total, 1 in progress)",
    `unexpected manage_todos counts summary: ${message?.summary}`,
  );
  assert(
    message?.todoData?.summary?.openCount === 3 &&
      message.todoData.summary.taskCount === 5 &&
      message.todoData.summary.inProgressCount === 1,
    `unexpected todoData counts: ${JSON.stringify(message?.todoData)}`,
  );
  assert(
    message?.previewLines.includes(
      "All Todos: 3 open · 5 total · 1 in progress",
    ) === true,
    `missing overall todo counts: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.includes(
      "Agent Checklist: 2 open · 3 total · 1 in progress",
    ) === true,
    `missing agent counts: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageTodosBatchShowsOnlyChangedItems(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_todos",
    callId: "call_todos_3",
    outputText: JSON.stringify({
      tool: "manage_todos",
      action: "batch",
      operation_count: 2,
      operations: [
        { action: "update", id: "todo_focus" },
        { action: "update", id: "todo_done" },
      ],
      summary: {
        task_count: 6,
        open_count: 4,
        in_progress_count: 2,
      },
      results: [
        {
          index: 0,
          action: "update",
          id: "todo_focus",
          item: {
            id: "todo_focus",
            text: "Focused changed item",
            done: false,
            in_progress: true,
            priority: "high",
            group: "flow",
            tags: ["focus"],
          },
        },
        {
          index: 1,
          action: "update",
          id: "todo_done",
          item: {
            id: "todo_done",
            text: "Completed changed item",
            done: true,
            priority: "low",
            tags: ["done"],
          },
        },
      ],
      items: [
        { id: "todo_old", text: "Old top item", done: false, priority: "urgent" },
        {
          id: "todo_focus",
          text: "Focused changed item",
          done: false,
          in_progress: true,
          priority: "high",
          group: "flow",
          tags: ["focus"],
        },
        { id: "todo_done", text: "Completed changed item", done: true, priority: "low", tags: ["done"] },
      ],
    }),
  });
  assert(Boolean(message), "expected structured manage_todos batch message");
  assert(
    message?.summary === "todo batch (2 ops, 4 open · 6 total, 2 in progress)",
    `unexpected manage_todos batch summary: ${message?.summary}`,
  );
  assert(
    message?.todoData?.operationCount === 2 &&
      message.todoData.summary?.openCount === 4 &&
      message.todoData.summary.taskCount === 6 &&
      message.todoData.summary.inProgressCount === 2,
    `unexpected todoData batch counts: ${JSON.stringify(message?.todoData)}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("Focused changed item")) === true,
    `missing changed focus item: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("[x] Completed changed item · low")) === true,
    `missing completed changed item: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("Old top item")) === false,
    `should not show unrelated todos: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("All Todos: 4 open · 6 total · 2 in progress")) === false,
    `should not show global summary lines for batch previews: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageImageGenerateShowsSessionRefs(): void {
  const message = buildStructuredToolMessage({
    tool: "manage-image",
    callId: "call_image_1",
    outputText: JSON.stringify({
      status: "completed",
      tool: "manage-image",
      thread_id: "thread_1",
      open_url: "swarm://tools/image/sessions/thread_1",
      provider: "google_gemini",
      model: "gemini-test",
      requested_count: 2,
      saved_count: 2,
      assets: [
        { asset_id: "asset_1", url: "/v1/image/assets?thread_id=thread_1&asset_id=asset_1" },
      ],
    }),
  });
  assert(Boolean(message), "expected structured manage-image message");
  assert(
    message?.summary === "manage-image · completed · 2 images · google_gemini/gemini-test · session thread_1",
    `unexpected manage-image summary: ${message?.summary}`,
  );
  assert(message?.target === "swarm://tools/image/sessions/thread_1", `unexpected image target: ${message?.target}`);
  assert(
    message?.previewLines.includes("open: swarm://tools/image/sessions/thread_1") === true,
    `missing image open url: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageTodosAgentListShowsOnlyCurrentSession(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_todos",
    callId: "call_todos_4",
    outputText: JSON.stringify({
      tool: "manage_todos",
      action: "list",
      owner_kind: "agent",
      session_id: "session-1",
      items: [
        { id: "todo_local", text: "Local agent item", done: false, session_id: "session-1" },
        { id: "todo_other", text: "Other session item", done: false, session_id: "session-2" },
      ],
    }),
  });
  assert(Boolean(message), "expected structured manage_todos agent list message");
  assert(
    message?.previewLines.some((line) => line.includes("Local agent item")) === true,
    `missing current-session agent todo: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("Other session item")) === false,
    `should not show other-session agent todos: ${message?.previewLines.join(" | ")}`,
  );
}

function testManageTodosAgentListPrioritizesActiveOpenItems(): void {
  const message = buildStructuredToolMessage({
    tool: "manage_todos",
    callId: "call_todos_5",
    outputText: JSON.stringify({
      tool: "manage_todos",
      action: "list",
      owner_kind: "agent",
      session_id: "session-1",
      items: [
        { id: "todo_done_1", text: "Completed one", done: true, session_id: "session-1" },
        { id: "todo_done_2", text: "Completed two", done: true, session_id: "session-1" },
        { id: "todo_done_3", text: "Completed three", done: true, session_id: "session-1" },
        { id: "todo_active", text: "Actually active item", done: false, in_progress: true, session_id: "session-1" },
        { id: "todo_open", text: "Open next item", done: false, session_id: "session-1" },
      ],
    }),
  });
  assert(Boolean(message), "expected structured manage_todos agent list message");
  assert(
    message?.previewLines.some((line) => line.includes("> [ ] Actually active item")) === true,
    `missing active agent todo: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("[ ] Open next item")) === true,
    `missing open agent todo: ${message?.previewLines.join(" | ")}`,
  );
  assert(
    message?.previewLines.some((line) => line.includes("Completed one") || line.includes("Completed two") || line.includes("Completed three")) === false,
    `completed todos should not crowd out active todos: ${message?.previewLines.join(" | ")}`,
  );
}

function testEditToolPreservesFullExpandedDiff(): void {
  const longOld = 'A'.repeat(50)
  const longNew = 'B'.repeat(50)
  const message = buildStructuredToolMessage({
    tool: 'edit',
    callId: 'call_edit_1',
    outputText: JSON.stringify({
      tool: 'edit',
      path: '/tmp/demo.txt',
      old_string_preview: `before\n${longOld}`,
      new_string_preview: `after\n${longNew}`,
      old_string_truncated: false,
      new_string_truncated: false,
    }),
  })
  assert(Boolean(message), 'expected structured edit tool message')
  assert(message?.editDiff?.oldLines.length === 2, `unexpected old diff lines: ${message?.editDiff?.oldLines.join(' | ')}`)
  assert(message?.editDiff?.newLines.length === 2, `unexpected new diff lines: ${message?.editDiff?.newLines.join(' | ')}`)
  assert(message?.editDiff?.oldLines[1] === longOld, 'expected full old diff line')
  assert(message?.editDiff?.newLines[1] === longNew, 'expected full new diff line')
  assert(message?.editDiff?.hunks.length === 1, `expected one edit hunk, got ${message?.editDiff?.hunks.length}`)
}

function testEditToolShowsMultiEditHunks(): void {
  const message = buildStructuredToolMessage({
    tool: 'edit',
    callId: 'call_edit_2',
    outputText: JSON.stringify({
      tool: 'edit',
      path: '/tmp/demo.txt',
      edit_count: 2,
      edits: [
        {
          index: 1,
          old_string_preview: 'first old',
          new_string_preview: 'first new',
          old_string_truncated: false,
          new_string_truncated: false,
        },
        {
          index: 2,
          old_string_preview: 'second old\\nline',
          new_string_preview: 'second new',
          old_string_truncated: true,
          new_string_truncated: false,
        },
      ],
    }),
  })
  assert(Boolean(message), 'expected structured multi-edit tool message')
  assert(message?.editDiff?.hunks.length === 2, `expected two edit hunks, got ${message?.editDiff?.hunks.length}`)
  assert(message?.editDiff?.hunks[0]?.oldLines[0] === 'first old', 'expected first old hunk line')
  assert(message?.editDiff?.hunks[0]?.newLines[0] === 'first new', 'expected first new hunk line')
  assert(message?.editDiff?.hunks[1]?.oldLines.join(' | ') === 'second old | line ...', `unexpected second old hunk: ${message?.editDiff?.hunks[1]?.oldLines.join(' | ')}`)
  assert(message?.editDiff?.hunks[1]?.newLines[0] === 'second new', 'expected second new hunk line')
  assert(message?.editDiff?.oldLines[0] === 'first old', 'expected legacy oldLines to mirror first hunk')
  assert(message?.editDiff?.oldTruncated === true, 'expected aggregate old truncation flag')
}

function testTaskRowsMapReasoningToThinkingWithoutPreviewLeak(): void {
  const message = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_1',
    outputText: JSON.stringify({
      tool: 'task',
      status: 'running',
      launches: [
        {
          launch_index: 1,
          subagent: 'explorer',
          status: 'running',
          current_preview_kind: 'reasoning',
          current_preview_text: '<reasoning>Inspecting files</reasoning>',
          reasoning_summary: 'Inspecting files before edit',
          current_tool: '',
        },
      ],
    }),
  })
  assert(Boolean(message), 'expected structured task tool message')
  assert(message?.taskRows.length === 1, `unexpected task rows: ${message?.taskRows.length}`)
  assert(message?.taskRows[0]?.tool === 'thinking', `unexpected task tool label: ${message?.taskRows[0]?.tool}`)
  assert(message?.taskRows[0]?.previewKind === 'thinking', `unexpected task preview kind: ${message?.taskRows[0]?.previewKind}`)
  assert(message?.taskRows[0]?.previewText === '', `reasoning preview should be hidden: ${message?.taskRows[0]?.previewText}`)
}

function testTaskRowsHideAssistantPreviewText(): void {
  const message = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_2',
    outputText: JSON.stringify({
      tool: 'task',
      status: 'running',
      launches: [
        {
          launch_index: 1,
          subagent: 'clone',
          status: 'running',
          current_preview_kind: 'assistant',
          current_preview_text: 'No Shore Between',
          current_tool: '',
        },
      ],
    }),
  })
  assert(Boolean(message), 'expected structured task tool message')
  assert(message?.taskRows.length === 1, `unexpected task rows: ${message?.taskRows.length}`)
  assert(message?.taskRows[0]?.previewKind === 'assistant', `unexpected task preview kind: ${message?.taskRows[0]?.previewKind}`)
  assert(message?.taskRows[0]?.previewText === '', `assistant preview should be hidden: ${message?.taskRows[0]?.previewText}`)
}

function testBashToolMessageShowsCommandAsMetadata(): void {
  const command = './scripts/check.sh --fast'
  const message = buildStructuredToolMessage({
    tool: 'bash',
    callId: 'call_bash_script_1',
    argumentsText: JSON.stringify({ command }),
    outputText: JSON.stringify({ command, exit_code: 0, output: 'ok' }),
  })

  assert(Boolean(message), 'expected structured bash tool message')
  assert(message?.summary === 'bash', `unexpected bash summary: ${message?.summary}`)
  assert(message?.commandText === command, `missing bash command metadata: ${message?.commandText}`)
  assert(
    message?.previewLines.includes(`$ ${command}`) === false,
    `command should not be quoted in preview lines: ${message?.previewLines.join(' | ')}`,
  )
  assert(
    message?.previewLines.includes('ok') === true,
    `missing bash output preview: ${message?.previewLines.join(' | ')}`,
  )
}

function testSearchToolPreservesContentMatchText(): void {
  const message = buildStructuredToolMessage({
    tool: 'search',
    callId: 'call_search_1',
    argumentsText: JSON.stringify({ query: 'SearchToolView', path: 'web/src' }),
    outputText: JSON.stringify({
      tool: 'search',
      search_mode: 'content',
      path: 'web/src',
      count: 1,
      total_matched: 1,
      matches: [
        {
          path: 'web/src/features/desktop/chat/components/chat-markdown.tsx',
          query: 'SearchToolView',
          line: 307,
          text: 'function SearchToolView({ toolMessage }: { toolMessage: StructuredToolMessage }) {',
        },
      ],
    }),
  })

  assert(Boolean(message), 'expected structured search tool message')
  const match = message?.searchData?.files[0]?.queryGroups[0]?.matches[0]
  assert(match?.line === 307, `unexpected search match line: ${match?.line}`)
  assert(
    match?.text.includes('function SearchToolView') === true,
    `missing search match text: ${match?.text}`,
  )
  assert(
    message?.previewLines.length === 0,
    `search tool should use rich search data, got preview lines: ${message?.previewLines.join(' | ')}`,
  )
}

function testTaskRowsPreserveCompletedLaunchesAcrossDeltaAndFinalPayloads(): void {
  const deltaPayload = JSON.stringify({
    tool: 'task',
    path_id: 'tool.task.stream.v1',
    status: 'running',
    launches: [
      {
        launch_index: 1,
        child_session_id: 'child-1',
        subagent: 'explorer',
        status: 'ok',
        current_tool: '',
        elapsed_ms: 1200,
      },
      {
        launch_index: 2,
        child_session_id: 'child-2',
        subagent: 'clone',
        status: 'running',
        current_tool: 'read',
        elapsed_ms: 800,
      },
    ],
  })
  const finalPayload = JSON.stringify({
    tool: 'task',
    path_id: 'tool.task.v1',
    status: 'ok',
    launches: [
      {
        launch_index: 1,
        child_session_id: 'child-1',
        subagent: 'explorer',
        status: 'ok',
        elapsed_ms: 1400,
      },
      {
        launch_index: 2,
        child_session_id: 'child-2',
        subagent: 'clone',
        status: 'ok',
        elapsed_ms: 1300,
      },
    ],
  })

  const deltaMessage = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_rows_1',
    outputText: deltaPayload,
  })
  const finalMessage = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_rows_1',
    outputText: finalPayload,
  })

  assert(Boolean(deltaMessage), 'expected delta task tool message')
  assert(Boolean(finalMessage), 'expected final task tool message')
  assert(deltaMessage?.taskRows.length === 2, `expected 2 delta task rows, got ${deltaMessage?.taskRows.length}`)
  assert(finalMessage?.taskRows.length === 2, `expected 2 final task rows, got ${finalMessage?.taskRows.length}`)
  assert(finalMessage?.taskRows[0]?.childSessionId === 'child-1', 'expected first child session id to persist')
  assert(finalMessage?.taskRows[1]?.childSessionId === 'child-2', 'expected second child session id to persist')
}

function testRunningTaskRowDoesNotUseStreamDurationAsDisplayTime(): void {
  const message = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_running_timer_contract',
    outputText: JSON.stringify({
      tool: 'task',
      path_id: 'tool.task.stream.v1',
      status: 'running',
      launches: [{
        launch_index: 1,
        child_session_id: 'child-1',
        subagent: 'explorer',
        status: 'running',
        current_tool: 'search',
        launch_started_at_ms: 123000,
        current_tool_ms: 5000,
        elapsed_ms: 9000,
      }],
    }),
  })

  assert(Boolean(message), 'expected running task message')
  const row = message!.taskRows[0]
  assert(row?.terminal === false, 'running task row should not be terminal')
  assert(row?.time === '', `running task row should not format stream duration as display time, got ${row?.time}`)
  assert(row?.launchStartedAtMs === 123000, `expected launch start timestamp, got ${row?.launchStartedAtMs}`)
  assert(row?.elapsedMs === 9000, `expected raw elapsed ms to be retained, got ${row?.elapsedMs}`)
}

function testTerminalTaskRowUsesFinalElapsedAsDisplayTime(): void {
  const message = buildStructuredToolMessage({
    tool: 'task',
    callId: 'call_task_terminal_timer_contract',
    outputText: JSON.stringify({
      tool: 'task',
      path_id: 'tool.task.v1',
      status: 'ok',
      launches: [{
        launch_index: 1,
        child_session_id: 'child-1',
        subagent: 'explorer',
        status: 'ok',
        elapsed_ms: 3400,
      }],
    }),
  })

  assert(Boolean(message), 'expected terminal task message')
  const row = message!.taskRows[0]
  assert(row?.terminal === true, 'terminal task row should be terminal')
  assert(row?.time === '3.4s', `terminal task row should format final elapsed time, got ${row?.time}`)
}

function testPlanManageShowsPlanLabelAndAction(): void {
  const message = buildStructuredToolMessage({
    tool: "plan_manage",
    callId: "call_plan_1",
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
  assert(Boolean(message), "expected structured plan message");
  assert(message?.summary === "plan save (Implementation Plan)", `unexpected plan summary: ${message?.summary}`);
  assert(message?.previewLines.includes("action: save") === true, `missing plan action: ${message?.previewLines.join(" | ")}`);
  assert(message?.previewLines.includes("title: Implementation Plan") === true, `missing plan title: ${message?.previewLines.join(" | ")}`);
  assert(message?.previewLines.includes("update: polish tool stream") === true, `missing plan update: ${message?.previewLines.join(" | ")}`);
}

function testToolMessagePreservesToolInstanceID(): void {
  const built = buildStructuredToolMessage({
    tool: "read",
    callId: "call-reused",
    toolInstanceId: "step-1:call-reused",
    argumentsText: JSON.stringify({ path: "one.txt" }),
    outputText: "first output",
  });
  assert(Boolean(built), "expected structured tool message");
  assert(built?.toolInstanceId === "step-1:call-reused", `unexpected built tool instance id: ${built?.toolInstanceId}`);

  const parsed = parseStructuredToolMessage(JSON.stringify({
    path_id: "run.tool-history.v2",
    tool: "read",
    call_id: "call-reused",
    tool_instance_id: "step-2:call-reused",
    arguments: JSON.stringify({ path: "two.txt" }),
    output: "second output",
  }));
  assert(Boolean(parsed), "expected parsed structured tool message");
  assert(parsed?.toolInstanceId === "step-2:call-reused", `unexpected parsed tool instance id: ${parsed?.toolInstanceId}`);
}

function testParsesV3ProviderToolResultEnvelope(): void {
  const parsed = parseStructuredToolMessage(JSON.stringify({
    path_id: "run.v3.provider-tool-result.v1",
    type: "v3_provider_tool_result",
    run_id: "v3run_123",
    step: 3,
    step_id: "step-3",
    tool_name: "read",
    call_id: "call-read-go-mod",
    tool_instance_id: "step-3:call-read-go-mod",
    arguments: JSON.stringify({ path: "go.mod", line_start: 1, max_lines: 20 }),
    output: JSON.stringify({ path: "/workspace/go.mod", line_start: 1, count: 20, truncated: true }),
    completed_output: "read /workspace/go.mod (lines 1-20, partial)",
  }));
  assert(Boolean(parsed), "expected parsed V3 provider tool result");
  assert(parsed?.pathId === "run.v3.provider-tool-result.v1", `unexpected path id: ${parsed?.pathId}`);
  assert(parsed?.tool === "read", `unexpected tool: ${parsed?.tool}`);
  assert(parsed?.toolInstanceId === "step-3:call-read-go-mod", `unexpected tool instance id: ${parsed?.toolInstanceId}`);
  assert(parsed?.summary.includes("go.mod"), `expected summarized read output, got: ${parsed?.summary}`);
  assert(!parsed?.previewLines.some((line) => line.includes("run.v3.provider-tool-result.v1")), "should not leak raw V3 envelope path_id into preview");
}

function main(): void {
  testExitPlanApprovedShowsMetadata();
  testExitPlanDeniedPermissionShowsFeedbackAndPlan();
  testExitPlanDefaultApprovalFeedbackIsHidden();
  testManageTodosListShowsItems();
  testManageTodosSummaryShowsCounts();
  testManageTodosBatchShowsOnlyChangedItems();
  testManageImageGenerateShowsSessionRefs();
  testManageTodosAgentListShowsOnlyCurrentSession();
  testManageTodosAgentListPrioritizesActiveOpenItems();
  testEditToolPreservesFullExpandedDiff();
  testEditToolShowsMultiEditHunks();
  testTaskRowsMapReasoningToThinkingWithoutPreviewLeak();
  testTaskRowsHideAssistantPreviewText();
  testBashToolMessageShowsCommandAsMetadata();
  testSearchToolPreservesContentMatchText();
  testTaskRowsPreserveCompletedLaunchesAcrossDeltaAndFinalPayloads();
  testRunningTaskRowDoesNotUseStreamDurationAsDisplayTime();
  testTerminalTaskRowUsesFinalElapsedAsDisplayTime();
  testPlanManageShowsPlanLabelAndAction();
  testToolMessagePreservesToolInstanceID();
  testParsesV3ProviderToolResultEnvelope();
  console.log("tool-message tests passed");
}

main();

