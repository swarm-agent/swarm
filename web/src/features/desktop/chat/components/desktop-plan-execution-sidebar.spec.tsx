import React from "react";
import type { ReactElement, ReactNode } from "react";
import test from "node:test";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { readFileSync } from "node:fs";

import { DesktopPlanExecutionSidebar } from "./desktop-plan-execution-sidebar";
import type { DesktopPlanExecutionSidebarActionInput } from "./desktop-plan-execution-sidebar";
import {
  DesktopPlanModal,
  type DesktopPlanRecoveryInput,
} from "./desktop-plan-modal";
import type {
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
} from "../types/chat";
import type { DesktopPlanExecutionView } from "../../state/desktop-v3-cache-selectors";

type HostElement = ReactElement<
  {
    "aria-label"?: string;
    "aria-pressed"?: boolean;
    children?: ReactNode;
    disabled?: boolean;
    onClick?: (event?: unknown) => void;
  },
  string
>;

function textContent(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === "boolean")
    return "";
  if (
    typeof node === "string" ||
    typeof node === "number" ||
    typeof node === "bigint"
  )
    return String(node);
  if (Array.isArray(node)) return node.map(textContent).join("");
  if (React.isValidElement(node))
    return textContent((node.props as { children?: ReactNode }).children);
  return "";
}

function collectHostElements(
  node: ReactNode,
  elements: HostElement[] = [],
): HostElement[] {
  if (node === null || node === undefined || typeof node === "boolean")
    return elements;
  if (Array.isArray(node)) {
    for (const child of node) collectHostElements(child, elements);
    return elements;
  }
  if (!React.isValidElement(node)) return elements;

  const element = node as ReactElement<{ children?: ReactNode }>;
  const elementType = element.type as unknown;
  if (typeof elementType === "function")
    return collectHostElements(elementType(element.props), elements);
  if (
    typeof elementType === "object" &&
    elementType &&
    "$$typeof" in elementType &&
    (elementType as { $$typeof?: symbol }).$$typeof === Symbol.for("react.memo")
  ) {
    const memoType = (elementType as { type?: unknown }).type;
    if (typeof memoType === "function")
      return collectHostElements(memoType(element.props), elements);
  }

  if (typeof element.type === "string") elements.push(element as HostElement);
  collectHostElements(element.props.children, elements);
  return elements;
}

function findSidebarButton(element: ReactElement, label: string): HostElement {
  const button = collectHostElements(element).find(
    (candidate) =>
      candidate.type === "button" &&
      textContent(candidate.props.children).replace(/\s+/g, " ").trim() ===
        label,
  );
  assert.ok(button, `expected ${label} button`);
  return button;
}

function planRecord(): DesktopSessionPlanRecord {
  const checkpoint = {
    id: "cp-1",
    title: "Build UI",
    status: "pending",
    objective: "",
    tasks: [],
    acceptanceCriteria: [],
    notes: "",
    report: "",
    result: "",
    changedFiles: [],
    validation: [],
    attemptId: "",
    runId: "",
    sessionId: "",
    startedAt: 0,
    completedAt: 0,
    review: null,
    attempts: [],
    order: 1,
  };
  return {
    id: "plan-1",
    title: "Plan",
    plan: "# Plan",
    status: "draft",
    approvalState: "pending",
    updatedAt: 1,
    document: {
      id: "plan-1",
      title: "Plan",
      status: "draft",
      schemaVersion: "",
      revisionId: "",
      info: {
        goal: "Ship plan modal",
        scope: "",
        context: "",
        decisions: [],
        constraints: [],
        assumptions: [],
        openQuestions: [],
        relevantFiles: [],
        successCriteria: [],
        validationStrategy: "",
      },
      executionPolicy: {
        mode: "automatic",
        shape: "checkpointed",
        followupCheckpointPolicy: "",
      },
      executionState: null,
      checkpoints: [checkpoint],
      originalCheckpoints: [],
      activeCheckpointId: "cp-1",
      renderedText: "",
      displayText: "",
    },
  };
}

function planRevision(
  overrides: Partial<DesktopSessionPlanRevisionRecord> = {},
): DesktopSessionPlanRevisionRecord {
  const base = planRecord();
  return {
    ...base,
    key: "revision-1",
    createdAt: 2,
    priorTitle: "",
    priorPlan: "",
    diffLines: [],
    updateSummary: "Initial plan",
    updateScope: "",
    updateKind: "definition",
    revisionKind: "definition",
    restoredFromVersion: 0,
    version: 1,
    parentRevision: 0,
    checkpoint: false,
    ...overrides,
  };
}

function renderPlanModal(
  props: Partial<React.ComponentProps<typeof DesktopPlanModal>> = {},
): string {
  return renderToStaticMarkup(
    <DesktopPlanModal
      open
      plan={planRecord()}
      revisions={[]}
      historyLoading={false}
      saving={false}
      executing={false}
      error={null}
      onOpenChange={() => undefined}
      onCopy={async () => true}
      onRestoreRevision={async () => undefined}
      onApproveStart={async () => undefined}
      {...props}
    />,
  );
}

function view(
  overrides: Partial<DesktopPlanExecutionView> = {},
): DesktopPlanExecutionView {
  const checkpoint = {
    id: "cp-1",
    title: "Build UI",
    status: "in_progress",
    objective: "",
    tasks: [],
    acceptanceCriteria: [],
    notes: "",
    report: "",
    result: "",
    changedFiles: [],
    validation: [],
    attemptId: "attempt-1",
    runId: "run-1",
    sessionId: "session-1",
    startedAt: 1,
    completedAt: 0,
    review: null,
    attempts: [],
    order: 1,
  };
  const checkpoints = [checkpoint];
  return {
    plan: {
      id: "plan-1",
      title: "Plan",
      plan: "# Plan",
      status: "approved",
      approvalState: "approved",
      updatedAt: 1,
      document: {
        id: "plan-1",
        title: "Plan",
        status: "approved",
        schemaVersion: "",
        revisionId: "",
        info: {
          goal: "",
          scope: "",
          context: "",
          decisions: [],
          constraints: [],
          assumptions: [],
          openQuestions: [],
          relevantFiles: [],
          successCriteria: [],
          validationStrategy: "",
        },
        executionPolicy: {
          mode: "automatic",
          shape: "checkpointed",
          followupCheckpointPolicy: "",
        },
        executionState: {
          status: "in_progress",
          activeAttemptId: "attempt-1",
          parentSessionId: "session-1",
          currentSessionId: "session-1",
          currentRunId: "run-1",
          lastCheckpointId: "",
          lastAttemptId: "",
          lastOutcome: "",
          startedAt: 1,
          updatedAt: 1,
          completedAt: 0,
        },
        checkpoints,
        activeCheckpointId: "cp-1",
        renderedText: "",
        displayText: "",
      },
    },
    activeCheckpoint: checkpoint,
    activeCheckpointId: "cp-1",
    status: "in_progress",
    policyMode: "automatic",
    policyShape: "checkpointed",
    currentRunId: "run-1",
    currentSessionId: "session-1",
    freshContext: true,
    reviewRequired: false,
    paused: false,
    blocked: false,
    failed: false,
    completed: false,
    attemptCount: 0,
    ...overrides,
  };
}

test("plan sidebar renders session content without a passive run-state section", () => {
  const html = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={() => undefined}
      belowActions={<section data-testid="session-git">Scoped Git changes</section>}
    />,
  );

  assert.match(html, /data-plan-section="checkpoint"/);
  assert.doesNotMatch(html, /data-plan-section="actions"/);
  assert.match(html, /data-plan-section="session"/);
  assert.match(html, /data-plan-scroll-region=""/);
  assert.match(
    html,
    /class="border-t[^\"]*flex min-h-\[160px\] flex-1 flex-col overflow-hidden" data-plan-section="session"/,
  );
  assert.match(
    html,
    /data-plan-scroll-region=""/,
  );
  assert.match(html, /shrink basis-auto gap-5 overflow-hidden/);
  assert.doesNotMatch(html, /data-plan-scroll-region=""[^>]*overflow-y-auto/);
  assert.doesNotMatch(html, /max-h-\[40%\]/);
  assert.doesNotMatch(html, /shadow-\[0_12px_34px/);
  assert.doesNotMatch(html, /Run state/);
  assert.doesNotMatch(html, /Running automatically/);
});

test("active checkpoint heading has balanced spacing before Progress", () => {
  const base = view();
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    title: "Reproduce the paused-stream resend inversion",
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={base} onAction={() => undefined} />,
  );

  assert.match(markup, /class="pt-2" data-plan-checkpoint-box-wrapper/);
  assert.match(markup, /class="mt-2" data-plan-progress/);
});

test("checkpoint sidebar bounds overflow while prioritizing active work and preserving full task text", () => {
  const base = view();
  const longTask =
    "Render the complete task text even when it is long enough that the old sidebar would have truncated it";
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    activeSubtaskId: "task-2",
    subtasks: [
      { id: "task-1", title: "Persist task changes", status: "completed", notes: "", result: "", startedAt: 1, completedAt: 2, order: 1 },
      { id: "task-2", title: "Render sidebar state", status: "in_progress", notes: "", result: "", startedAt: 2, completedAt: 0, order: 2 },
      { id: "task-3", title: "Keep the layout compact", status: "pending", notes: "", result: "", startedAt: 0, completedAt: 0, order: 3 },
      { id: "task-4", title: longTask, status: "pending", notes: "", result: "", startedAt: 0, completedAt: 0, order: 4 },
      { id: "task-5", title: "Reveal the fifth task", status: "pending", notes: "", result: "", startedAt: 0, completedAt: 0, order: 5 },
      { id: "task-6", title: "Reveal the sixth task", status: "pending", notes: "", result: "", startedAt: 0, completedAt: 0, order: 6 },
    ],
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );
  const source = readFileSync(
    new URL("./desktop-plan-execution-sidebar.tsx", import.meta.url),
    "utf8",
  );

  assert.match(markup, />Tasks</);
  assert.match(markup, /Render sidebar state/);
  assert.match(markup, new RegExp(longTask));
  assert.match(markup, /data-plan-task-mode="bounded"/);
  assert.match(markup, /data-plan-task-viewport=""/);
  assert.match(markup, /style="max-height:88px"/);
  assert.match(markup, /data-plan-visible-tasks=""/);
  assert.match(markup, /data-plan-task-active="true"/);
  assert.match(markup, /data-plan-task-expansion="" data-plan-completed-tasks=""/);
  assert.match(markup, /data-plan-task-chevron=""/);
  assert.match(markup, /aria-expanded="false"/);
  assert.match(markup, /aria-controls="desktop-plan-overflow-tasks"/);
  assert.match(markup, /Show 3 more tasks and 1 completed/);
  assert.ok(
    markup.indexOf("Render sidebar state") < markup.indexOf("Keep the layout compact"),
    "expected active work before pending work",
  );
  assert.match(markup, /overflow-y-auto/);
  assert.match(markup, /break-words \[overflow-wrap:anywhere\]/);
  assert.match(source, /\.\.\.\(expanded \? \{ height: viewportHeight \} : \{\}\)/);
  assert.match(source, /Math\.floor\(regionBottom - sectionTop - 60\)/);
  assert.doesNotMatch(source, /setExpanded\(false\)/);
  assert.match(source, /expanded \? "flex-1 overflow-hidden" : "shrink-0"/);
  assert.match(source, /className="min-h-0 flex-1 overflow-y-auto/);
  assert.match(source, /data-plan-task-overflow-scroll="conditional"/);
  assert.match(source, /data-plan-scroll-region[\s\S]*?overflow-hidden/);
  assert.doesNotMatch(source, /data-plan-scroll-region[\s\S]{0,160}?overflow-y-auto/);
  assert.doesNotMatch(markup, /\[x\] Persist task changes/);
  assert.doesNotMatch(markup, /\[ \] Render sidebar state/);
  assert.match(markup, /Open full plan/);
});

test("pending-only overflow always exposes a keyboard-accessible chevron disclosure", () => {
  const base = view();
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    activeSubtaskId: "task-3",
    subtasks: [1, 2, 3, 4, 5, 6].map((order) => ({
      id: `task-${order}`,
      title: order === 3 ? "Active task must stay visible" : `Pending task ${order}`,
      status: order === 3 ? "in_progress" : "pending",
      notes: "",
      result: "",
      startedAt: order === 3 ? 1 : 0,
      completedAt: 0,
      order,
    })),
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={base} onAction={() => undefined} />,
  );
  const source = readFileSync(
    new URL("./desktop-plan-execution-sidebar.tsx", import.meta.url),
    "utf8",
  );

  assert.match(markup, /Active task must stay visible/);
  assert.match(markup, /data-plan-task-active="true"/);
  assert.match(markup, /data-plan-task-expansion=""/);
  assert.doesNotMatch(markup, /data-plan-completed-tasks/);
  assert.match(markup, /<button[^>]*type="button"[^>]*aria-expanded="false"/);
  assert.match(markup, /data-plan-task-chevron=""/);
  assert.match(markup, /Show 4 more tasks/);
  assert.match(source, /Math\.floor\(sidebarHeight \* 0\.28\)/);
  assert.match(source, /pendingTasks\.length - 1/);
});

test("Git integration reserves a visible bounded region below the plan", () => {
  const sidebarSource = readFileSync(
    new URL("./desktop-v3-existing-conversation-pane.tsx", import.meta.url),
    "utf8",
  );
  const appPageSource = readFileSync(
    new URL("../../layout/desktop-app-page.tsx", import.meta.url),
    "utf8",
  );

  assert.match(sidebarSource, /data-plan-sidebar-column/);
  assert.match(sidebarSource, /hidden min-h-0 min-w-0 overflow-hidden min-\[1300px\]:flex min-\[1300px\]:flex-col/);
  assert.match(appPageSource, /data-plan-git-layout="protected"/);
  assert.match(appPageSource, /min-h-\[160px\][^'\"]*flex-1[^'\"]*overflow-hidden/);
});

test("automatic plan sidebar omits passive run state and execution mode controls", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.doesNotMatch(markup, /Run state/);
  assert.doesNotMatch(markup, /Running automatically/);
  assert.match(markup, /In Progress/);
  assert.doesNotMatch(markup, /data-plan-system-message=""/);
  assert.doesNotMatch(markup, /border-l-2/);
  assert.doesNotMatch(markup, /Pause for review after each checkpoint/);
  assert.doesNotMatch(markup, /Switch to automatic/);
  assert.doesNotMatch(markup, /Archive plan/);
  assert.doesNotMatch(markup, /Continue checkpoint/);
  assert.doesNotMatch(markup, /Accept this checkpoint/);
  assert.doesNotMatch(markup, /Restart/);
  assert.doesNotMatch(markup, /Follow-ups/);
  assert.doesNotMatch(markup, /Using default/);
  assert.doesNotMatch(markup, /Current setting:/);
  assert.doesNotMatch(markup, /Auto-add .* start \(Default\)/);
  assert.doesNotMatch(markup, /Ask first/);
  assert.doesNotMatch(markup, /Save as default/);
  assert.doesNotMatch(markup, /Inherit global default/);
  assert.doesNotMatch(markup, /Auto-add only/);
  assert.doesNotMatch(markup, /role="switch"/);
});

test("plan header omits the temporary checkpoint counter", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} />,
  );

  assert.doesNotMatch(markup, /Preview current checkpoint design/);
  assert.doesNotMatch(markup, /aria-label="Current checkpoint design"/);
  assert.doesNotMatch(markup, /aria-pressed=/);
});

test("the selected console checkpoint balances the status and Progress spacing", () => {
  const base = view();
  const nextTitle =
    "Add five selectable current-checkpoint row designs with enough detail to wrap cleanly at sidebar width";
  base.plan.document.checkpoints.push({
    ...base.plan.document.checkpoints[0],
    id: "followup-3",
    title: nextTitle,
    status: "pending",
    attemptId: "",
    runId: "",
    sessionId: "",
    startedAt: 0,
  });

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );
  const nextUpIndex = markup.indexOf("data-plan-next-up");
  const nextCheckpointIndex = markup.indexOf("data-plan-next-checkpoint");
  const nextIdIndex = markup.indexOf("followup-3", nextCheckpointIndex);
  const nextTitleIndex = markup.indexOf(nextTitle, nextCheckpointIndex);

  assert.doesNotMatch(markup, /data-plan-current-checkpoint-design/);
  assert.match(markup, /data-plan-status-treatment="plain-text"/);
  assert.match(markup, /data-plan-checkpoint-treatment="console-block"/);
  assert.match(
    markup,
    /class="pt-3" data-plan-checkpoint-box-wrapper=""><h3 class="min-w-0 line-clamp-3[^\"]*bg-\[var\(--app-surface-subtle\)\] px-2\.5 py-2 font-mono/,
  );
  assert.match(markup, /class="mt-3" data-plan-progress=""/);
  assert.match(markup, /data-plan-next-checkpoint=""/);
  assert.match(markup, /break-all font-mono/);
  assert.match(markup, /line-clamp-2 break-words/);
  assert.ok(nextUpIndex >= 0, "expected a distinct Next up section");
  assert.ok(nextCheckpointIndex > nextUpIndex, "expected the next checkpoint beneath its label");
  assert.ok(nextIdIndex > nextCheckpointIndex, "expected the next checkpoint id");
  assert.ok(nextTitleIndex > nextIdIndex, "expected the next title beneath its id");
  assert.doesNotMatch(markup, /mt-1 truncate text-\[var\(--app-text-muted\)\]/);
});

test("the status row stays above the fixed console checkpoint", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} />,
  );
  const labelIndex = markup.indexOf("data-plan-current-checkpoint-label");
  const statusIndex = markup.indexOf("data-plan-status");
  const titleIndex = markup.indexOf("data-plan-checkpoint-title");

  assert.match(markup, /data-plan-current-checkpoint-layout="row"/);
  assert.match(markup, /data-plan-current-checkpoint-row=""/);
  assert.match(markup, /flex min-w-0 items-center justify-between gap-3/);
  assert.match(markup, /data-plan-checkpoint-treatment="console-block"/);
  assert.match(
    markup,
    /data-plan-current-checkpoint-row=""><div[^>]*>Current checkpoint<\/div><span[^>]*data-plan-status-treatment="plain-text"[^>]*>.*?<\/span><\/div><div class="pt-3" data-plan-checkpoint-box-wrapper=""><h3/,
  );
  assert.ok(labelIndex >= 0, "expected the left checkpoint label");
  assert.ok(statusIndex > labelIndex, "expected status after the left label");
  assert.ok(titleIndex > statusIndex, "expected checkpoint title beneath the row");
  assert.match(markup, /Current checkpoint/i);
  assert.match(markup, /In Progress/);
  assert.match(markup, /Build UI/);
});

test("active plan status is plain text without a left badge", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} />,
  );
  const status = markup.match(/<span data-plan-status="" class="([^"]+)"/);
  assert.ok(status, "expected plan status indicator");
  assert.doesNotMatch(status[1], /before:/);
  assert.doesNotMatch(status[1], /rounded-full|rounded-md|rounded-lg/);
  assert.doesNotMatch(status[1], /bg-\[var\(--app-primary-soft\)\]/);
});

test("active checkpoint title shows three complete lines before truncating", () => {
  const base = view();
  const longTitle =
    "Implement a long active checkpoint title that needs a second compact line before the sidebar truncates it";
  base.activeCheckpoint = { ...base.activeCheckpoint!, title: longTitle };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={base} onAction={() => undefined} />,
  );
  const title = markup.match(
    /<h3[^>]*class="([^"]+)"[^>]*data-plan-checkpoint-title=""[^>]*>/,
  );
  assert.ok(title, "expected active checkpoint title");
  assert.match(title[1], /line-clamp-3/);
  assert.match(title[1], /leading-5/);
  assert.doesNotMatch(title[1], /leading-snug/);
  assert.match(title[1], /break-words/);
  assert.doesNotMatch(title[1], /(?:^|\s)truncate(?:\s|$)/);
  assert.match(markup, new RegExp(longTitle));
});

test("accepted plan sidebar restores the original execution view without AI helpers", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={() => undefined}
    />,
  );

  assert.match(markup, />Plan</);
  assert.doesNotMatch(markup, /Plan Agent/);
  assert.doesNotMatch(markup, /New Auto Agent chat/);
  assert.doesNotMatch(markup, /AI helper/);
});

test("paused checkpoint sidebar exposes the canonical same-session resume action", () => {
  const base = view({ paused: true, status: "paused" });
  base.activeCheckpoint = { ...base.activeCheckpoint!, status: "paused" };
  base.plan.document.checkpoints = [base.activeCheckpoint];
  const actions: DesktopPlanExecutionSidebarActionInput[] = [];
  const element = (
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={(input) => {
        actions.push(input);
      }}
    />
  );
  const markup = renderToStaticMarkup(element);
  const resumeButton = findSidebarButton(element, "Resume checkpoint");

  assert.match(markup, /Checkpoint paused/);
  assert.match(markup, /same checkpoint in this session/);
  assert.match(markup, /work already completed/);
  assert.doesNotMatch(markup, /fresh attempt/);
  assert.doesNotMatch(markup, /Resolve blocker/);
  assert.doesNotMatch(markup, /Switch to checkpoint-by-checkpoint/);
  assert.equal(resumeButton.props.disabled, false);
  resumeButton.props.onClick?.();
  assert.deepEqual(actions, [
    { action: "resume_checkpoint", checkpointId: "cp-1" },
  ]);
});

test("blocked checkpoint sidebar shows unblock controls without automatic pause controls", () => {
  const base = view({ blocked: true, status: "blocked" });
  base.activeCheckpoint = { ...base.activeCheckpoint!, status: "blocked" };
  base.plan.document.checkpoints = [
    base.activeCheckpoint,
    {
      ...base.activeCheckpoint,
      id: "cp-2",
      title: "Next checkpoint",
      status: "pending",
      attemptId: "",
      runId: "",
      sessionId: "",
      startedAt: 0,
    },
  ];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.match(markup, /Blocked checkpoint/);
  assert.match(markup, /Resolve blocker &amp; start next checkpoint/);
  assert.match(markup, /Resolve blocker only/);
  assert.match(markup, /does not restart or rewind/);
  assert.doesNotMatch(markup, /Pause automatic/);
  assert.doesNotMatch(markup, /after this checkpoint/);
  assert.doesNotMatch(markup, /Switch to checkpoint-by-checkpoint/);
  assert.doesNotMatch(markup, /Restart checkpoint/);
  assert.doesNotMatch(markup, /Rewind to checkpoint/);
});

test("blocked checkpoint resolve actions dispatch unblock requests", () => {
  const base = view({ blocked: true, status: "blocked" });
  base.activeCheckpoint = { ...base.activeCheckpoint!, status: "blocked" };
  base.plan.document.checkpoints = [
    base.activeCheckpoint,
    {
      ...base.activeCheckpoint,
      id: "cp-2",
      title: "Next checkpoint",
      status: "pending",
      attemptId: "",
      runId: "",
      sessionId: "",
      startedAt: 0,
    },
  ];
  const actions: DesktopPlanExecutionSidebarActionInput[] = [];
  const startButton = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={(input) => {
        actions.push(input);
      }}
      onEditPlan={() => undefined}
    />,
    "Resolve blocker & start next checkpoint",
  );
  const onlyButton = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={(input) => {
        actions.push(input);
      }}
      onEditPlan={() => undefined}
    />,
    "Resolve blocker only",
  );

  assert.equal(startButton.props.disabled, false);
  assert.equal(onlyButton.props.disabled, false);
  startButton.props.onClick?.();
  onlyButton.props.onClick?.();

  assert.deepEqual(actions, [
    { action: "resolve_blocked_checkpoint", checkpointId: "cp-1" },
    { action: "resolve_blocked_only", checkpointId: "cp-1" },
  ]);
});

test("legacy review-required plans keep review actions without exposing a manual mode", () => {
  const base = view({
    policyMode: "review_each_checkpoint",
    reviewRequired: true,
    status: "waiting_review",
  });
  base.plan.document.checkpoints.push({
    ...base.plan.document.checkpoints[0],
    id: "cp-2",
    title: "Follow-up",
    status: "pending",
    attemptId: "",
    runId: "",
    sessionId: "",
    startedAt: 0,
  });
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.doesNotMatch(markup, /Run state/);
  assert.match(markup, /Waiting review/);
  assert.match(markup, />Actions</);
  assert.doesNotMatch(markup, /Review Mode/);
  assert.doesNotMatch(markup, /Switch to automatic/);
  assert.match(markup, /Accept &amp; start next checkpoint/);
  assert.match(markup, /Accepting review starts the next checkpoint/);
  assert.match(markup, /ask the AI to add or adjust checkpoints/);
  assert.match(markup, /Archive plan/);
  assert.match(
    markup,
    /Move this plan to Archived without starting another checkpoint/,
  );
  assert.doesNotMatch(markup, /Actions \/ Plan Mode/);
  assert.doesNotMatch(markup, /Manual review mode/);
  assert.doesNotMatch(markup, /Restart/);
  assert.doesNotMatch(markup, /Edit plan/);
  assert.doesNotMatch(markup, /disabled="">Accept &amp; start next checkpoint/);
  assert.doesNotMatch(markup, /Accept this checkpoint/);
  assert.doesNotMatch(markup, /Accept &amp; move to next/);
});

test("legacy final review exposes acceptance before archiving without a mode toggle", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view({
        policyMode: "review_each_checkpoint",
        reviewRequired: true,
        status: "waiting_review",
      })}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.doesNotMatch(markup, /Run state/);
  assert.match(markup, /Waiting review/);
  assert.match(markup, />Actions</);
  assert.match(markup, /Accept &amp; archive plan/);
  assert.match(
    markup,
    /Accepting final review is recorded first, then this plan is archived/,
  );
  assert.doesNotMatch(markup, /Switch to automatic/);
  assert.doesNotMatch(markup, /Archive plan<\/button>/);
  assert.doesNotMatch(
    markup,
    /Move this plan to Archived without starting another checkpoint/,
  );
  assert.doesNotMatch(markup, /disabled="">Accept &amp; archive plan/);
  assert.doesNotMatch(markup, /Manual review mode/);
  assert.doesNotMatch(markup, /Restart/);
  assert.doesNotMatch(markup, /Edit plan/);
  assert.doesNotMatch(markup, /Accept this checkpoint/);
  assert.doesNotMatch(markup, /Accept &amp; move to next/);
});

test("automatic final review keeps automatic copy and no policy toggle when all checkpoints are complete", () => {
  const base = view({
    policyMode: "automatic",
    reviewRequired: true,
    status: "waiting_review",
    completed: true,
  });
  base.plan.document.executionPolicy.mode = "automatic";
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    status: "completed",
    review: {
      status: "pending",
      reviewerId: "",
      reviewerType: "",
      result: "",
      notes: "",
      reviewedAt: 0,
    },
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.doesNotMatch(markup, /Run state/);
  assert.match(markup, /Waiting review/);
  assert.match(markup, />Actions</);
  assert.doesNotMatch(markup, /Plan complete/);
  assert.match(markup, /Accept \u0026amp; archive plan/);
  assert.doesNotMatch(markup, /Review Mode/);
  assert.doesNotMatch(markup, /Backend policy is checkpoint-by-checkpoint/);
  assert.doesNotMatch(markup, /Switch to automatic/);
});

test("manual review mode keeps finish-plan action clickable when all checkpoints are complete but review is still pending", () => {
  const base = view({
    policyMode: "review_each_checkpoint",
    reviewRequired: true,
    status: "waiting_review",
    completed: true,
  });
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    status: "completed",
    review: {
      status: "pending",
      reviewerId: "",
      reviewerType: "",
      result: "",
      notes: "",
      reviewedAt: 0,
    },
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.match(markup, /Accept &amp; archive plan/);
  assert.doesNotMatch(markup, /disabled="">Accept &amp; archive plan/);
});

test("final accept-and-archive review action dispatches checkpoint acceptance first", () => {
  const actions: DesktopPlanExecutionSidebarActionInput[] = [];
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={view({
        policyMode: "review_each_checkpoint",
        reviewRequired: true,
        status: "waiting_review",
      })}
      onAction={(input) => {
        actions.push(input);
      }}
      onEditPlan={() => undefined}
    />,
    "Accept & archive plan",
  );

  assert.equal(button.props.disabled, false);
  button.props.onClick?.();

  assert.deepEqual(actions, [{ action: "accept_checkpoint", checkpointId: "cp-1" }]);
});

test("final review renders the terminal recommendation", () => {
  const base = view({ reviewRequired: true, status: "waiting_review" });
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    recommendation: {
      decision: "ship",
      action: "accept_and_archive",
      reason: "Focused lifecycle coverage passes.",
      actionState: "ready",
    },
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar view={base} onAction={() => undefined} onEditPlan={() => undefined} />,
  );
  assert.match(markup, /Recommendation/);
  assert.match(markup, /Ship — Accept And Archive/);
  assert.match(markup, /Focused lifecycle coverage passes/);
  assert.match(markup, /data-plan-recommendation=""/);
  assert.match(markup, /border border-\[var\(--app-primary-border\)\]/);
});

test("final review sidebar prefers the same canonical handoff recommendation as the inline card", () => {
  const base = view({ reviewRequired: true, status: "waiting_review" });
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    recommendation: {
      decision: "change",
      action: "old_action",
      reason: "Stale checkpoint value.",
      actionState: "ready",
    },
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      canonicalRecommendation={{
        decision: "ship",
        action: "review",
        reason: "Canonical projected value.",
        actionState: "ready",
      }}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );
  assert.match(markup, /Ship — Review/);
  assert.match(markup, /Canonical projected value/);
  assert.doesNotMatch(markup, /Stale checkpoint value/);
});

test("desktop plan sidebar never renders manual execution mode controls", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view({ policyMode: "review_each_checkpoint", reviewRequired: true, status: "waiting_review" })}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.doesNotMatch(markup, /Pause for review after each checkpoint/);
  assert.doesNotMatch(markup, /Switch to automatic/);
  assert.doesNotMatch(markup, /checkpoint-by-checkpoint/);
});

test("non-final review action dispatches checkpoint acceptance to start the next checkpoint", () => {
  const base = view({
    policyMode: "review_each_checkpoint",
    reviewRequired: true,
    status: "waiting_review",
  });
  base.plan.document.checkpoints.push({
    ...base.plan.document.checkpoints[0],
    id: "cp-2",
    title: "Follow-up",
    status: "pending",
    attemptId: "",
    runId: "",
    sessionId: "",
    startedAt: 0,
  });
  const actions: DesktopPlanExecutionSidebarActionInput[] = [];
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={(input) => {
        actions.push(input);
      }}
      onEditPlan={() => undefined}
    />,
    "Accept & start next checkpoint",
  );

  assert.equal(button.props.disabled, false);
  button.props.onClick?.();

  assert.deepEqual(actions, [
    { action: "accept_checkpoint", checkpointId: "cp-1" },
  ]);
});

test("plan modal keeps recovery hidden until opened and shows the full active checkpoint under the title", () => {
  const base = planRecord();
  base.document.checkpoints[0].title =
    "Build UI with a very long active checkpoint title that should wrap instead of truncating underneath the plan title";
  const markup = renderPlanModal({ plan: base, revisions: [planRevision()] });

  const titleIndex = markup.indexOf(">Plan</h2>");
  const activeIndex = markup.indexOf("CP 1 of 1");
  const activeTitleIndex = markup.indexOf(
    "Build UI with a very long active checkpoint title that should wrap instead of truncating underneath the plan title",
  );
  const recoveryButtonIndex = markup.indexOf(">Recovery</button>");
  const copyIndex = markup.indexOf(">Copy</button>");
  const documentIndex = markup.indexOf("Plan details");
  assert(titleIndex >= 0, "expected modal title");
  assert(
    activeIndex > titleIndex,
    "expected active checkpoint count under modal title",
  );
  assert(
    activeTitleIndex > activeIndex,
    "expected active checkpoint title under modal title",
  );
  assert(
    recoveryButtonIndex > titleIndex,
    "expected recovery entry in the header controls",
  );
  assert(
    copyIndex > recoveryButtonIndex,
    "expected recovery entry directly left of copy",
  );
  assert(
    documentIndex > copyIndex,
    "expected plan details after header controls",
  );
  assert.match(markup, /whitespace-normal break-words/);
  assert.doesNotMatch(markup, /Recovery mode/);
  assert.doesNotMatch(markup, /Checkpoint by checkpoint · Automatic/);
  assert.doesNotMatch(markup, /Active: /);
  assert.doesNotMatch(markup, /Plan revision/);
  assert.doesNotMatch(markup, /Recovery checkpoint/);
  assert.doesNotMatch(markup, /Revision history/);
  assert.doesNotMatch(markup, /Recovery controls/);
  assert.doesNotMatch(markup, /Edit plan/);
  assert.doesNotMatch(markup, /Save plan/);
  assert.doesNotMatch(markup, /<textarea/);
});

test("plan modal removes top-level approval cards in favor of recovery confirmation", () => {
  const markup = renderPlanModal();

  assert.doesNotMatch(markup, /Approve &amp; Start|Approve & Start/);
  assert.doesNotMatch(markup, /Execution on approval/);
  assert.doesNotMatch(markup, /Automatic mode/);
  assert.doesNotMatch(markup, />Single run<\/span>/);
  assert.doesNotMatch(markup, /Manual checkpoint review/);
  assert.doesNotMatch(markup, /role="radiogroup"/);
  assert.doesNotMatch(markup, /Execution style/);
  assert.doesNotMatch(markup, /Single run is disabled/);
  assert.doesNotMatch(markup, /Run through as one execution/);
  assert.match(markup, />Recovery<\/button>/);
  assert.doesNotMatch(markup, /Recovery mode/);
});
