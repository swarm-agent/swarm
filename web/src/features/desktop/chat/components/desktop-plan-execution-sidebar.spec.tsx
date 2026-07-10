import React from "react";
import type { ReactElement, ReactNode } from "react";
import test from "node:test";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";

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
    blocked: false,
    failed: false,
    completed: false,
    attemptCount: 0,
    ...overrides,
  };
}

test("normal run-through sidebar shows the plan title instead of the synthetic checkpoint", () => {
  const base = view({ policyShape: "single_run" });
  base.plan.title = "Plan: ship sidebar fix";
  base.plan.document.title = "Plan: ship sidebar fix";
  base.plan.document.executionPolicy = {
    mode: "automatic",
    shape: "single_run",
    followupCheckpointPolicy: "",
  };
  base.plan.document.activeCheckpointId = "plan-run";
  base.activeCheckpointId = "plan-run";
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    id: "plan-run",
    title: "Run approved plan",
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.match(markup, /Plan execution/);
  assert.match(markup, /Plan: ship sidebar fix/);
  assert.match(markup, /Continue normally/);
  assert.match(markup, /Running the approved plan normally/);
  assert.match(markup, /open \/plan and restore a plan revision/);
  assert.match(
    markup,
    /Run-through recovery is managed from \/plan revision restore/,
  );
  assert.match(markup, /Archive plan/);
  assert.match(
    markup,
    /Archive this plan when you no longer need the chat in your active workspace/,
  );
  assert.doesNotMatch(markup, /Active checkpoint/);
  assert.doesNotMatch(markup, /plan-run/);
  assert.doesNotMatch(markup, /Run approved plan/);
  assert.doesNotMatch(markup, /No remaining checkpoint/);
});

test("checkpoint sidebar projects objective, task checklist, criteria, notes, and plain tasks from plan state", () => {
  const base = view();
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    objective: "Ship the durable task projection",
    tasks: ["[x] Persist task changes", "[ ] Render sidebar state", "Plain task"],
    acceptanceCriteria: ["Tasks survive reconnect"],
    notes: "Do not add a second task store.",
  };
  base.plan.document.checkpoints = [base.activeCheckpoint];

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.match(markup, /Objective/);
  assert.match(markup, /Ship the durable task projection/);
  assert.match(markup, /Tasks/);
  assert.match(markup, /Persist task changes/);
  assert.match(markup, /Render sidebar state/);
  assert.match(markup, /Plain task/);
  assert.match(markup, /Acceptance criteria/);
  assert.match(markup, /Tasks survive reconnect/);
  assert.match(markup, /Notes/);
  assert.match(markup, /Do not add a second task store/);
  assert.match(markup, /checked=""/);
  assert.match(markup, /type="checkbox"/);
  assert.doesNotMatch(markup, /\[x\] Persist task changes/);
  assert.doesNotMatch(markup, /\[ \] Render sidebar state/);
});

test("automatic checkpointed mode sidebar actions card explains continuation and exposes backend policy toggle", () => {
  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  );

  assert.match(markup, /Automatic mode on/);
  assert.match(markup, /Backend policy is automatic/);
  assert.match(markup, /Switch to checkpoint-by-checkpoint/);
  assert.match(markup, /next checkpoint completion pauses for review/);
  assert.match(markup, /Archive plan/);
  assert.match(
    markup,
    /Archive this plan when you no longer need the chat in your active workspace/,
  );
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

test("manual review mode keeps the start-next review button visible and enabled when more checkpoints remain", () => {
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

  assert.match(markup, /Actions/);
  assert.match(markup, /Review Mode/);
  assert.match(markup, /Backend policy is checkpoint-by-checkpoint/);
  assert.match(markup, /Switch to automatic/);
  assert.match(markup, /next checkpoint completion can auto-start/);
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

test("manual review mode exposes final acceptance before archiving on the final checkpoint", () => {
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

  assert.match(markup, /Review Mode/);
  assert.match(markup, /Backend policy is checkpoint-by-checkpoint/);
  assert.match(markup, /Accept &amp; archive plan/);
  assert.match(
    markup,
    /Accepting final review is recorded first, then this plan is archived/,
  );
  assert.match(markup, /Switch to automatic/);
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

  assert.match(markup, /Automatic mode paused/);
  assert.match(markup, /Backend policy is automatic/);
  assert.match(markup, /All checkpoints are complete and waiting for final review/);
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
});

test("automatic mode toggle dispatches checkpointed policy action", () => {
  const actions: DesktopPlanExecutionSidebarActionInput[] = [];
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={(input) => {
        actions.push(input);
      }}
      onEditPlan={() => undefined}
    />,
    "Switch to checkpoint-by-checkpoint",
  );

  assert.equal(button.props.disabled, false);
  button.props.onClick?.();

  assert.deepEqual(actions, [{ action: "resume_checkpointed" }]);
});

test("checkpoint-by-checkpoint mode toggle dispatches automatic policy action", () => {
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
    "Switch to automatic",
  );

  assert.equal(button.props.disabled, false);
  button.props.onClick?.();

  assert.deepEqual(actions, [{ action: "resume_automatic" }]);
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
  assert.match(markup, />Recovery<\/button>/);
  assert.doesNotMatch(markup, /Recovery mode/);
});
