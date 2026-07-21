import {
  memo,
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { DesktopSessionPlanCheckpointRecommendation, TaskChildCardActions, TaskToolRow } from "../types/chat";
import type { DesktopV3TaskChildViewModel } from "../../state/desktop-v3-cache-selectors";
import { DesktopPlanSubagentList } from "./desktop-plan-subagent-list";
import { ChevronDown } from "lucide-react";
import { cn } from "../../../../lib/cn";
import { Button } from "../../../../components/ui/button";
import type { DesktopSessionPlanCheckpoint } from "../types/chat";
import type { DesktopPlanExecutionView } from "../../state/desktop-v3-cache-selectors";
type DesktopPlanExecutionSidebarAction =
  | "accept_checkpoint"
  | "resume_checkpoint"
  | "restart_checkpoint"
  | "resolve_blocked_checkpoint"
  | "resolve_blocked_only"
  | "archive_plan";

export interface DesktopPlanExecutionSidebarActionInput {
  action: DesktopPlanExecutionSidebarAction;
  checkpointId?: string;
}

export interface DesktopPlanExecutionSidebarProps {
  view: DesktopPlanExecutionView | null;
  embedded?: boolean;
  busyAction?: string | null;
  canStop?: boolean;
  onAction?: (
    input: DesktopPlanExecutionSidebarActionInput,
  ) => void | Promise<void>;
  onStop?: () => void | Promise<void>;
  onEditPlan?: () => void;
  belowActions?: ReactNode;
  onNewAutoChat?: () => void;
  onOpenPlanAgent?: () => void;
  displayMode?: "full" | "compact" | "thin";
  taskChildren?: Array<{ row: TaskToolRow; view: DesktopV3TaskChildViewModel | null }>;
  taskChildActions?: TaskChildCardActions;
  canonicalRecommendation?: DesktopSessionPlanCheckpointRecommendation | null;
}

type Tone = "muted" | "primary" | "success" | "warning" | "danger";

function humanize(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "Unknown";
  return trimmed
    .replace(/[-_]+/g, " ")
    .replace(
      /\w\S*/g,
      (word) => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase(),
    );
}

function displayCheckpointId(value: string, fallback: number): string {
  const trimmed = value.trim();
  if (!trimmed) return `CP-${fallback + 1}`;
  const match = trimmed.match(/^cp[-_ ]?(\d+)$/i);
  if (match) return `CP-${match[1]}`;
  return trimmed.toUpperCase().startsWith("CP-")
    ? trimmed.toUpperCase()
    : trimmed;
}

function statusTone(status: string, active = false): Tone {
  const normalized = status.trim().toLowerCase();
  if (
    normalized === "completed" ||
    normalized === "done" ||
    normalized === "success"
  )
    return "success";
  if (
    normalized === "needs_review" ||
    normalized === "waiting_review" ||
    normalized === "review"
  )
    return "warning";
  if (
    normalized === "blocked" ||
    normalized === "failed" ||
    normalized === "error"
  )
    return "danger";
  if (
    active ||
    normalized === "in_progress" ||
    normalized === "in-progress" ||
    normalized === "running" ||
    normalized === "active"
  )
    return "primary";
  return "muted";
}

function toneStatusClass(tone: Tone): string {
  switch (tone) {
    case "success":
      return "text-[var(--app-success)]";
    case "warning":
      return "text-[var(--app-warning-text)]";
    case "danger":
      return "text-[var(--app-danger)]";
    case "primary":
      return "text-[var(--app-primary)]";
    default:
      return "text-[var(--app-text-muted)]";
  }
}

function actionBusyKey(
  action: DesktopPlanExecutionSidebarAction,
  checkpointId?: string,
): string {
  return `${action}:${checkpointId ?? ""}`;
}

function checkpointIsIncomplete(
  checkpoint: DesktopSessionPlanCheckpoint,
): boolean {
  return checkpoint.status.trim().toLowerCase() !== "completed";
}

function statusLabel(
  view: DesktopPlanExecutionView,
  checkpoint?: DesktopSessionPlanCheckpoint,
): string {
  if (view.reviewRequired) return "Waiting review";
  if (view.completed) return "Completed";
  if (view.paused) return "Paused";
  if (view.blocked) return "Blocked";
  if (view.failed) return "Failed";
  return humanize(checkpoint?.status || view.status || "Ready");
}

type CheckpointTaskView = {
  text: string;
  checked: boolean | null;
};

function checkpointTaskView(value: string): CheckpointTaskView {
  const task = value.trim();
  const match = task.match(/^\[([ xX])\]\s*(.*)$/);
  if (!match) return { text: task, checked: null };
  return {
    text: match[2].trim() || task,
    checked: match[1].toLowerCase() === "x",
  };
}

const DEFAULT_VISIBLE_PENDING_TASKS = 1;
const MIN_TASK_VIEWPORT_HEIGHT = 88;
const MAX_TASK_VIEWPORT_HEIGHT = 240;

type SidebarTask = CheckpointTaskView & { active: boolean };

function taskViewportHeight(sidebarHeight: number): number {
  return Math.max(
    MIN_TASK_VIEWPORT_HEIGHT,
    Math.min(MAX_TASK_VIEWPORT_HEIGHT, Math.floor(sidebarHeight * 0.28)),
  );
}

function CheckpointDetails({
  checkpoint,
}: {
  checkpoint?: DesktopSessionPlanCheckpoint;
}) {
  const taskSectionRef = useRef<HTMLElement | null>(null);
  const taskViewportRef = useRef<HTMLDivElement | null>(null);
  const taskProbeRef = useRef<HTMLUListElement | null>(null);
  const previousViewportHeightRef = useRef(0);
  const [expanded, setExpanded] = useState(false);
  const [viewportHeight, setViewportHeight] = useState(MIN_TASK_VIEWPORT_HEIGHT);
  const [visiblePendingCount, setVisiblePendingCount] = useState(
    DEFAULT_VISIBLE_PENDING_TASKS,
  );

  const tasks: SidebarTask[] = checkpoint
    ? (checkpoint.subtasks?.length ?? 0) > 0
      ? checkpoint.subtasks!.map((subtask) => ({
          text: subtask.title,
          checked: subtask.status.toLowerCase() === "completed",
          active:
            subtask.id === checkpoint.activeSubtaskId ||
            subtask.status.toLowerCase() === "in_progress",
        }))
      : checkpoint.tasks
          .map(checkpointTaskView)
          .filter((task) => task.text.length > 0)
          .map((task) => ({ ...task, active: false }))
    : [];

  const taskSignature = tasks
    .map((task) => `${task.active}:${task.checked}:${task.text}`)
    .join("\u0000");
  const activeTasks = tasks.filter((task) => task.active && task.checked !== true);
  const pendingTasks = tasks.filter((task) => !task.active && task.checked !== true);
  const completedTasks = tasks.filter((task) => task.checked === true);
  const reservedPendingCount = Math.min(
    visiblePendingCount,
    Math.max(0, pendingTasks.length - 1),
  );
  const visiblePendingTasks = pendingTasks.slice(0, reservedPendingCount);
  const overflowPendingTasks = pendingTasks.slice(reservedPendingCount);
  const visibleTasks = [...activeTasks, ...visiblePendingTasks];
  const disclosureCount = overflowPendingTasks.length + completedTasks.length;

  const updateTaskFit = useCallback(() => {
    const viewport = taskViewportRef.current;
    const probe = taskProbeRef.current;
    if (!viewport || !probe) return;

    const sidebar = viewport.closest<HTMLElement>(
      '[data-testid="desktop-plan-execution-sidebar"]',
    );
    const sidebarHeight = sidebar?.clientHeight || window.innerHeight;
    const sectionTop = taskSectionRef.current?.getBoundingClientRect().top ?? 0;
    const sidebarTop = sidebar?.getBoundingClientRect().top ?? 0;
    const availableBelowSection = Math.max(
      MIN_TASK_VIEWPORT_HEIGHT,
      sidebarHeight - Math.max(0, sectionTop - sidebarTop) - 60,
    );
    const nextViewportHeight = Math.min(
      taskViewportHeight(sidebarHeight),
      availableBelowSection,
    );
    if (
      previousViewportHeightRef.current > 0 &&
      nextViewportHeight < previousViewportHeightRef.current &&
      expanded
    ) {
      setExpanded(false);
    }
    previousViewportHeightRef.current = nextViewportHeight;
    setViewportHeight(nextViewportHeight);

    const rows = Array.from(
      probe.querySelectorAll<HTMLElement>("[data-plan-task-probe-row]"),
    );
    const activeHeight = rows
      .slice(0, activeTasks.length)
      .reduce((height, row) => height + row.getBoundingClientRect().height + 6, 0);
    const disclosureHeight =
      pendingTasks.length > 0 || completedTasks.length > 0 ? 30 : 0;
    const availablePendingHeight = Math.max(
      0,
      nextViewportHeight - activeHeight - disclosureHeight,
    );
    let usedHeight = 0;
    let nextVisiblePendingCount = 0;
    for (const row of rows.slice(activeTasks.length)) {
      const rowHeight = row.getBoundingClientRect().height + 6;
      if (usedHeight + rowHeight > availablePendingHeight) break;
      usedHeight += rowHeight;
      nextVisiblePendingCount += 1;
    }
    if (activeTasks.length === 0 && pendingTasks.length > 0) {
      nextVisiblePendingCount = Math.max(1, nextVisiblePendingCount);
    }
    setVisiblePendingCount(
      Math.min(pendingTasks.length, nextVisiblePendingCount),
    );
  }, [
    activeTasks.length,
    expanded,
    completedTasks.length,
    pendingTasks.length,
    taskSignature,
    tasks.length,
  ]);

  useLayoutEffect(() => {
    updateTaskFit();
    const viewport = taskViewportRef.current;
    const sidebar = viewport?.closest<HTMLElement>(
      '[data-testid="desktop-plan-execution-sidebar"]',
    );
    const scrollRegion = viewport?.closest<HTMLElement>("[data-plan-scroll-region]");
    if (!viewport || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(updateTaskFit);
    observer.observe(viewport);
    if (sidebar) observer.observe(sidebar);
    if (scrollRegion) observer.observe(scrollRegion);
    return () => observer.disconnect();
  }, [updateTaskFit]);

  if (tasks.length === 0) return null;

  const renderTask = (task: SidebarTask, index: number, probe = false) => (
    <li
      key={`${index}:${task.text}`}
      className={cn(
        "flex min-w-0 items-start gap-2 leading-4",
        task.active && "font-medium text-[var(--app-primary)]",
      )}
      data-plan-task-active={task.active ? "true" : undefined}
      data-plan-task-probe-row={probe ? "" : undefined}
    >
      {task.checked === null ? (
        <span
          aria-hidden="true"
          className="mt-[7px] size-1 shrink-0 rounded-full bg-[var(--app-text-subtle)]"
        />
      ) : (
        <input
          aria-label={task.checked ? "Completed task" : "Incomplete task"}
          checked={task.checked}
          readOnly
          tabIndex={-1}
          type="checkbox"
          className="mt-0.5 size-3 shrink-0 accent-[var(--app-primary)]"
        />
      )}
      <span className="min-w-0 break-words [overflow-wrap:anywhere]">
        {task.text}
      </span>
    </li>
  );

  const disclosureLabel = expanded
    ? "Hide additional tasks"
    : overflowPendingTasks.length > 0
      ? `Show ${overflowPendingTasks.length} more task${overflowPendingTasks.length === 1 ? "" : "s"}${completedTasks.length > 0 ? ` and ${completedTasks.length} completed` : ""}`
      : `Show ${completedTasks.length} completed task${completedTasks.length === 1 ? "" : "s"}`;

  return (
    <section
      ref={taskSectionRef}
      className="mt-3 min-h-0 border-t border-[var(--app-border)] pt-3 text-xs text-[var(--app-text-muted)]"
    >
      <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">
        Tasks
      </div>
      <div
        ref={taskViewportRef}
        className="relative mt-1.5 flex min-h-0 flex-col overflow-hidden"
        style={{ maxHeight: viewportHeight }}
        data-plan-task-list
        data-plan-task-mode="bounded"
        data-plan-task-viewport
      >
        {visibleTasks.length > 0 ? (
          <ul
            className={cn(
              "grid min-h-0 gap-1.5 pr-1 [scrollbar-gutter:stable]",
              activeTasks.length > 0 && visiblePendingTasks.length === 0
                ? "overflow-y-auto"
                : "shrink-0 overflow-hidden",
            )}
            data-plan-visible-tasks
          >
            {visibleTasks.map((task, index) => renderTask(task, index))}
          </ul>
        ) : (
          <div className="shrink-0 text-[var(--app-text-subtle)]">
            All tasks completed
          </div>
        )}
        {disclosureCount > 0 ? (
          <div
            className="mt-1.5 flex min-h-0 shrink-0 flex-col border-t border-[var(--app-border)] pt-1"
            data-plan-task-expansion
            data-plan-completed-tasks={completedTasks.length > 0 ? "" : undefined}
          >
            <button
              type="button"
              className="flex shrink-0 items-center gap-1 py-1 text-left text-[10px] font-medium text-[var(--app-text-subtle)] hover:text-[var(--app-text-muted)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]"
              aria-expanded={expanded}
              aria-controls="desktop-plan-overflow-tasks"
              onClick={() => setExpanded((current) => !current)}
            >
              <ChevronDown
                aria-hidden="true"
                className={cn(
                  "size-3 shrink-0 transition-transform",
                  expanded && "rotate-180",
                )}
                data-plan-task-chevron
              />
              {disclosureLabel}
            </button>
            {expanded ? (
              <div
                id="desktop-plan-overflow-tasks"
                className="min-h-0 overflow-y-auto pb-1 pr-1 [scrollbar-gutter:stable]"
                data-plan-task-overflow
              >
                {overflowPendingTasks.length > 0 ? (
                  <ul className="mt-1 grid gap-1.5">
                    {overflowPendingTasks.map((task, index) =>
                      renderTask(task, index + visibleTasks.length),
                    )}
                  </ul>
                ) : null}
                {completedTasks.length > 0 ? (
                  <div className="mt-2 border-t border-[var(--app-border)] pt-1.5">
                    <div className="mb-1 text-[10px] font-medium text-[var(--app-text-subtle)]">
                      {completedTasks.length} completed
                    </div>
                    <ul className="grid gap-1.5">
                      {completedTasks.map((task, index) =>
                        renderTask(
                          task,
                          index + visibleTasks.length + overflowPendingTasks.length,
                        ),
                      )}
                    </ul>
                  </div>
                ) : null}
              </div>
            ) : null}
          </div>
        ) : null}
        <ul
          ref={taskProbeRef}
          aria-hidden="true"
          className="pointer-events-none absolute inset-x-0 top-0 -z-10 grid gap-1.5 opacity-0"
        >
          {[...activeTasks, ...pendingTasks].map((task, index) =>
            renderTask(task, index, true),
          )}
        </ul>
      </div>
    </section>
  );
}

function StatusIndicator({
  label,
  tone,
  className,
  treatment,
}: {
  label: string;
  tone: Tone;
  className?: string;
  treatment?: string;
}) {
  return (
    <span
      data-plan-status
      className={cn(
        "inline-flex max-w-[132px] shrink-0 items-center text-[10px] font-semibold leading-none",
        toneStatusClass(tone),
        className,
      )}
      data-plan-status-treatment={treatment}
    >
      <span className="truncate">{label}</span>
    </span>
  );
}

function CurrentCheckpointTitle({
  activeTitle,
  checkpointId,
  title,
}: {
  activeTitle: string;
  checkpointId: string;
  title: string;
}) {
  return (
    <div className="pt-2" data-plan-checkpoint-box-wrapper>
      <h3
        className={cn(
          "min-w-0 line-clamp-3 break-words text-sm font-semibold leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]",
          "bg-[var(--app-surface-subtle)] px-2.5 py-2 font-mono",
        )}
        title={activeTitle}
        data-plan-checkpoint-title
        data-plan-checkpoint-treatment="console-block"
      >
        <span className="mr-1.5 inline text-xs font-bold text-[var(--app-primary)] before:content-['>_']">
          {checkpointId}
        </span>
        <span className="inline text-[13px]">{title}</span>
      </h3>
    </div>
  );
}

function CurrentCheckpointRow({
  activeTitle,
  checkpointId,
  title,
  status,
  tone,
}: {
  activeTitle: string;
  checkpointId: string;
  title: string;
  status: string;
  tone: Tone;
}) {
  return (
    <div
      aria-live="polite"
      data-plan-current-checkpoint-layout="row"
      className="min-w-0"
    >
      <div
        className="flex min-w-0 items-center justify-between gap-3"
        data-plan-current-checkpoint-row
      >
        <div
          className="min-w-0 text-[10px] font-semibold uppercase tracking-[0.18em] text-[var(--app-text-muted)]"
          data-plan-current-checkpoint-label
        >
          Current checkpoint
        </div>
        <StatusIndicator
          label={status}
          tone={tone}
          className="uppercase tracking-[0.1em]"
          treatment="plain-text"
        />
      </div>
      <CurrentCheckpointTitle
        activeTitle={activeTitle}
        checkpointId={checkpointId}
        title={title}
      />
    </div>
  );
}

function ActiveCheckpointSection({
  view,
  checkpoints,
  completedCount,
  totalCount,
  activeIndex,
  onOpenPlan,
  compact = false,
}: {
  view: DesktopPlanExecutionView;
  checkpoints: DesktopSessionPlanCheckpoint[];
  completedCount: number;
  totalCount: number;
  activeIndex: number;
  onOpenPlan?: () => void;
  compact?: boolean;
}) {
  const checkpoint = view.activeCheckpoint;
  const activePosition = activeIndex >= 0 ? activeIndex : -1;
  const checkpointFallbackIndex = activeIndex >= 0 ? activeIndex : 0;
  const checkpointId = checkpoint
    ? displayCheckpointId(checkpoint.id, checkpointFallbackIndex)
    : "None";
  const title = checkpoint?.title || "No active checkpoint";
  const activeTitle = checkpoint ? `${checkpointId} ${title}` : title;
  const nextCheckpoint = checkpoints.find(
    (candidate, index) =>
      index > activePosition && checkpointIsIncomplete(candidate),
  );
  const nextIndex = nextCheckpoint
    ? checkpoints.findIndex((candidate) => candidate === nextCheckpoint)
    : -1;
  const progressValue =
    totalCount > 0
      ? Math.max(0, Math.min(100, (completedCount / totalCount) * 100))
      : 0;
  const tone = statusTone(
    view.reviewRequired ? "needs_review" : checkpoint?.status || view.status,
    Boolean(checkpoint),
  );

  return (
    <section className="min-w-0 border-b border-[var(--app-border)] pb-4" data-plan-section="checkpoint">
      <CurrentCheckpointRow
        activeTitle={activeTitle}
        checkpointId={checkpointId}
        title={title}
        status={statusLabel(view, checkpoint)}
        tone={tone}
      />

      <div className="mt-2" data-plan-progress>
        <div className="mb-1 flex items-center justify-between text-[10px] text-[var(--app-text-subtle)]">
          <span>Progress</span>
          <span className="font-medium text-[var(--app-text-muted)]">
            {completedCount} / {totalCount}
          </span>
        </div>
        <div className="h-1 overflow-hidden rounded-full bg-[var(--app-surface-subtle)]">
          <div
            className="h-full rounded-full bg-[var(--app-primary)] opacity-80"
            style={{ width: `${progressValue}%` }}
          />
        </div>
      </div>

      <div
        className="mt-4 min-w-0 border-t border-[var(--app-border)] pt-3.5 text-xs"
        data-plan-next-up
      >
        <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
          Next up
        </div>
        {nextCheckpoint ? (
          <div
            className="mt-2 flex min-w-0 items-start gap-2.5 border-l border-[var(--app-primary-border)] pl-2.5"
            data-plan-next-checkpoint
            title={`${displayCheckpointId(nextCheckpoint.id, nextIndex)} ${nextCheckpoint.title || "Untitled checkpoint"}`}
          >
            <span
              aria-hidden="true"
              className="mt-1.5 size-1 shrink-0 rounded-full bg-[var(--app-primary)]"
            />
            <div className="min-w-0">
              <div className="break-all font-mono text-[10px] font-semibold leading-4 text-[var(--app-primary)]">
                {displayCheckpointId(nextCheckpoint.id, nextIndex)}
              </div>
              <div className="mt-0.5 line-clamp-2 break-words leading-4 text-[var(--app-text-muted)] [overflow-wrap:anywhere]">
                {nextCheckpoint.title || "Untitled checkpoint"}
              </div>
            </div>
          </div>
        ) : (
          <div className="mt-1.5 text-[var(--app-text-muted)]">
            No remaining checkpoint
          </div>
        )}
      </div>

      {!compact ? <CheckpointDetails checkpoint={checkpoint} /> : null}

      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={onOpenPlan}
        disabled={!onOpenPlan}
        className="mt-3 h-8 w-full rounded-md text-xs"
      >
        Open full plan
      </Button>
    </section>
  );
}

function ReviewRecommendation({ recommendation }: { recommendation?: DesktopSessionPlanCheckpointRecommendation | null }) {
  if (!recommendation || !recommendation.decision || !recommendation.action || !recommendation.reason) return null;
  return (
    <div className="mt-3 rounded-md border border-[var(--app-primary-border)] px-2.5 py-2 text-[11px] leading-4 text-[var(--app-text-muted)]" data-plan-recommendation>
      <div className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--app-text-subtle)]">Recommendation</div>
      <div className="mt-1 font-medium text-[var(--app-text)]">{humanize(recommendation.decision)} — {humanize(recommendation.action)}</div>
      <p className="mt-1">{recommendation.reason}</p>
      <p className="mt-1 text-[11px]">Action state: {humanize(recommendation.actionState)}</p>
    </div>
  );
}

function ActionsSection({
  view,
  busyAction,
  canStop,
  onAction,
  canonicalRecommendation,
}: DesktopPlanExecutionSidebarProps & { view: DesktopPlanExecutionView }) {
  const checkpointId =
    view.activeCheckpointId || view.activeCheckpoint?.id || "";
  const acceptBusy =
    busyAction === actionBusyKey("accept_checkpoint", checkpointId);
  const resumeBusy =
    busyAction === actionBusyKey("resume_checkpoint", checkpointId);
  const archiveBusy = busyAction === actionBusyKey("archive_plan");
  const resolveStartBusy =
    busyAction === actionBusyKey("resolve_blocked_checkpoint", checkpointId);
  const resolveOnlyBusy =
    busyAction === actionBusyKey("resolve_blocked_only", checkpointId);
  const checkpoints = view.plan.document?.checkpoints ?? [];
  const activeIndex = view.activeCheckpoint
    ? checkpoints.findIndex(
        (checkpoint) => checkpoint.id === view.activeCheckpoint?.id,
      )
    : -1;
  const hasNextCheckpoint = checkpoints.some(
    (checkpoint, index) =>
      index > activeIndex && checkpointIsIncomplete(checkpoint),
  );
  const canAccept = Boolean(
    onAction &&
    checkpointId &&
    view.reviewRequired &&
    !view.blocked &&
    !view.failed &&
    !canStop,
  );
  const canArchive = Boolean(onAction && !canStop && !archiveBusy);
  const acceptReviewBusy = acceptBusy;
  const canAcceptReview = canAccept;
  const acceptReviewLabel = hasNextCheckpoint
    ? "Accept & start next checkpoint"
    : "Accept & archive plan";
  const acceptReviewHelp = hasNextCheckpoint
    ? "Accepting review starts the next checkpoint. You can keep chatting first or ask the AI to add or adjust checkpoints."
    : "Accepting final review is recorded first, then this plan is archived.";
  const showDirectArchiveAction = !view.reviewRequired || hasNextCheckpoint;

  if (view.paused) {
    return (
      <section className="min-w-0 pt-0.5" data-plan-section="actions">
        <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          Actions
        </div>
        <div className="mt-2 border-y border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-2" data-plan-system-message>
          <div className="text-xs font-semibold text-[var(--app-text)]">
            Checkpoint paused
          </div>
          <p className="mt-0.5 text-[11px] leading-4 text-[var(--app-text-muted)]">
            Continue the same checkpoint in this session with the work already completed.
          </p>
        </div>
        <div className="mt-3 grid gap-1.5">
          <Button
            type="button"
            size="sm"
            variant="primary"
            className={cn("rounded-lg", resumeBusy ? "animate-pulse" : "")}
            onClick={() => void onAction?.({ action: "resume_checkpoint", checkpointId })}
            disabled={!onAction || !checkpointId || resumeBusy}
          >
            Resume checkpoint
          </Button>
        </div>
      </section>
    );
  }

  if (view.blocked) {
    return (
      <section className="min-w-0 pt-0.5" data-plan-section="actions">
        <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          Actions
        </div>
        <div className="mt-2 border-y border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-2.5 py-2" data-plan-system-message>
          <div className="text-xs font-semibold text-[var(--app-text)]">
            Blocked checkpoint
          </div>
          <p className="mt-0.5 text-[11px] leading-4 text-[var(--app-text-muted)]">
            Resolve the blocker without restarting this checkpoint. If another
            checkpoint remains, you can continue directly to it.
          </p>
        </div>
        <div className="mt-3 grid gap-1.5">
          <Button
            type="button"
            size="sm"
            variant="primary"
            className={cn(
              "rounded-lg",
              resolveStartBusy ? "animate-pulse" : "",
            )}
            onClick={() =>
              void onAction?.({
                action: "resolve_blocked_checkpoint",
                checkpointId,
              })
            }
            disabled={
              !onAction ||
              !checkpointId ||
              !hasNextCheckpoint ||
              resolveStartBusy
            }
          >
            Resolve blocker &amp; start next checkpoint
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className={cn("rounded-lg", resolveOnlyBusy ? "animate-pulse" : "")}
            onClick={() =>
              void onAction?.({ action: "resolve_blocked_only", checkpointId })
            }
            disabled={!onAction || !checkpointId || resolveOnlyBusy}
          >
            Resolve blocker only
          </Button>
          <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
            This clears the blocked state. It does not restart or rewind the
            checkpoint.
          </p>
        </div>
      </section>
    );
  }

  if (
    !view.reviewRequired &&
    !view.blocked &&
    !view.failed &&
    !view.completed
  ) {
    return (
      <section className="min-w-0 pt-0.5" data-plan-section="actions">
        <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          Run state
        </div>
        <div className="mt-2 flex items-center justify-between gap-3 border-y border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2.5 py-2" data-plan-system-message>
          <span className="text-xs font-semibold text-[var(--app-text)]">Running automatically</span>
          <StatusIndicator label={statusLabel(view, view.activeCheckpoint)} tone="primary" treatment="plain-text" />
        </div>
      </section>
    );
  }

  return (
    <section className="min-w-0 pt-0.5" data-plan-section="actions">
      <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
        Run state
      </div>
      <div className="mt-2 flex items-center justify-between gap-3 border-y border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-2" data-plan-system-message>
        <span className="text-xs font-medium text-[var(--app-text)]">{statusLabel(view, view.activeCheckpoint)}</span>
        <StatusIndicator
          label={view.completed ? "Plan complete" : view.reviewRequired ? "Review required" : view.failed ? "Run failed" : "Automatic"}
          tone={view.failed ? "danger" : view.reviewRequired ? "warning" : "muted"}
          treatment="plain-text"
        />
      </div>

      <ReviewRecommendation recommendation={canonicalRecommendation ?? view.activeCheckpoint?.recommendation} />

      <div className="mt-3 grid gap-2">
        <div className="grid gap-1.5">
          <Button
            type="button"
            size="sm"
            variant="primary"
            className={cn(
              "rounded-lg",
              acceptReviewBusy ? "animate-pulse" : "",
            )}
            onClick={() =>
              void onAction?.(
                { action: "accept_checkpoint", checkpointId },
              )
            }
            disabled={!canAcceptReview || acceptReviewBusy}
          >
            {acceptReviewLabel}
          </Button>
          {view.reviewRequired ? (
            <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
              {acceptReviewHelp}
            </p>
          ) : null}
        </div>
        {showDirectArchiveAction ? (
          <div className="grid gap-1.5">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className={cn("rounded-lg", archiveBusy ? "animate-pulse" : "")}
              onClick={() => void onAction?.({ action: "archive_plan" })}
              disabled={!canArchive}
            >
              Archive plan
            </Button>
            <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
              Move this plan to Archived without starting another checkpoint.
            </p>
          </div>
        ) : null}
      </div>
    </section>
  );
}

export const DesktopPlanExecutionSidebar = memo(
  function DesktopPlanExecutionSidebar({
    view,
    embedded = false,
    busyAction,
    canStop = false,
    onAction,
    onStop: _onStop,
    onEditPlan,
    belowActions,
    displayMode = "full",
    taskChildren = [],
    taskChildActions,
    canonicalRecommendation,
  }: DesktopPlanExecutionSidebarProps) {
    const document = view?.plan.document ?? null;
    if (!view || !document) return null;

    const thin = displayMode === "thin";
    const compact = displayMode !== "full";
    const checkpoints = document.checkpoints;
    const completedCount = checkpoints.filter(
      (checkpoint) => checkpoint.status.toLowerCase() === "completed",
    ).length;
    const totalCount = checkpoints.length;
    const activeIndex = view.activeCheckpoint
      ? checkpoints.findIndex(
          (checkpoint) => checkpoint.id === view.activeCheckpoint?.id,
        )
      : -1;

    return (
      <aside
        className={embedded
          ? "min-h-0 min-w-0 w-full overflow-visible bg-[var(--app-surface)]"
          : cn(
              "hidden h-full min-h-0 min-w-0 flex-1 overflow-hidden border-l border-[var(--app-border)] bg-[var(--app-surface)] min-[1300px]:flex min-[1300px]:flex-col",
              thin ? "w-[56px] max-w-[56px] px-2 py-3" : compact ? "w-[280px] max-w-[280px] px-3 py-4" : "w-[360px] max-w-[360px] px-5 py-4",
            )}
        aria-label="Plan execution sidebar"
        data-testid="desktop-plan-execution-sidebar"
        data-display-mode={displayMode}
      >
        <div
          className={cn(
            "min-w-0 max-w-full gap-4 [&_*]:min-w-0",
            embedded
              ? "grid content-start overflow-visible"
              : "flex min-h-0 flex-1 flex-col overflow-hidden",
          )}
        >
          {!embedded ? (
            <header className="shrink-0 border-b border-[var(--app-border)] pb-3">
              <div className={cn("font-semibold text-[var(--app-text)]", thin ? "text-center text-xs" : "text-sm")} title="Plan execution">
                {thin ? "P" : "Plan"}
              </div>
            </header>
          ) : null}
          {thin ? (
            <div className="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto py-1" data-plan-thin-rail>
              <button type="button" onClick={onEditPlan} disabled={!onEditPlan} className="grid min-h-11 place-items-center rounded-lg border border-[var(--app-border)] text-xs font-semibold text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" title={`${statusLabel(view, view.activeCheckpoint)} · ${completedCount}/${totalCount} checkpoints`} aria-label={`Open full plan. ${statusLabel(view, view.activeCheckpoint)}. ${completedCount} of ${totalCount} checkpoints complete.`}>
                {completedCount}/{totalCount}
              </button>
              {taskChildren.length > 0 ? <DesktopPlanSubagentList children={taskChildren} actions={taskChildActions} mode="thin" /> : null}
            </div>
          ) : <div
            className={cn(
              "grid content-start gap-4",
              !embedded &&
                "min-h-0 shrink basis-auto overflow-y-auto [scrollbar-gutter:stable]",
            )}
            data-plan-scroll-region
          >
            <ActiveCheckpointSection
              view={view}
              checkpoints={checkpoints}
              completedCount={completedCount}
              totalCount={totalCount}
              activeIndex={activeIndex}
              onOpenPlan={onEditPlan}
              compact={compact}
            />
            {taskChildren.length > 0 ? <DesktopPlanSubagentList children={taskChildren} actions={taskChildActions} mode={compact ? "compact" : "full"} /> : null}
            <ActionsSection
              view={view}
              busyAction={busyAction}
              canStop={canStop}
              onAction={onAction}
              onEditPlan={onEditPlan}
              canonicalRecommendation={canonicalRecommendation}
            />
          </div>}
          {!thin && belowActions ? (
            <div
              className={cn(
                "border-t border-[var(--app-border)] pt-4",
                !embedded &&
                  "flex min-h-[160px] flex-1 flex-col overflow-hidden",
              )}
              data-plan-section="session"
            >
              {belowActions}
            </div>
          ) : null}
        </div>
      </aside>
    );
  },
);
