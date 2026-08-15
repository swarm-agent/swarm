import {
  memo,
  useState,
  type ReactNode,
} from "react";
import type { TaskChildCardActions, TaskToolRow } from "../types/chat";
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

const COLLAPSED_VISIBLE_PENDING_TASKS = 1;

type SidebarTask = CheckpointTaskView & { active: boolean };

function CheckpointDetails({
  checkpoint,
}: {
  checkpoint?: DesktopSessionPlanCheckpoint;
}) {
  const [expanded, setExpanded] = useState(false);

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

  const activeTasks = tasks.filter((task) => task.active && task.checked !== true);
  const pendingTasks = tasks.filter((task) => !task.active && task.checked !== true);
  const completedTasks = tasks.filter((task) => task.checked === true);
  const visiblePendingTasks = pendingTasks.slice(0, COLLAPSED_VISIBLE_PENDING_TASKS);
  const overflowPendingTasks = pendingTasks.slice(COLLAPSED_VISIBLE_PENDING_TASKS);
  const collapsedTasks = [...activeTasks, ...visiblePendingTasks];
  const displayedTasks = expanded
    ? [...activeTasks, ...pendingTasks, ...completedTasks]
    : collapsedTasks;
  const disclosureCount = overflowPendingTasks.length + completedTasks.length;

  if (tasks.length === 0) return null;

  const renderTask = (task: SidebarTask, index: number) => (
    <li
      key={`${index}:${task.text}`}
      className={cn(
        "flex min-w-0 items-start gap-2.5 rounded-lg px-2 py-1.5 leading-relaxed",
        task.active
          ? "bg-[var(--app-primary-soft)] font-medium text-[var(--app-primary)]"
          : "text-[var(--app-text-muted)]",
      )}
      data-plan-task-active={task.active ? "true" : undefined}
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
      className="mt-4 min-h-0 rounded-xl bg-[var(--app-bg-alt)] p-3 text-xs text-[var(--app-text-muted)]/90"
    >
      <div className="text-[10px] font-semibold uppercase tracking-[0.15em] text-[var(--app-text-subtle)]/80">
        Tasks
      </div>
      <div
        className="relative mt-1.5 flex min-h-0 max-h-[min(28vh,240px)] flex-col overflow-hidden"
        data-plan-task-list
        data-plan-task-mode="bounded"
        data-plan-task-viewport
      >
        {displayedTasks.length > 0 ? (
          <ul
            id="desktop-plan-task-list"
            className="grid min-h-0 flex-1 gap-1.5 overflow-y-auto pr-1 [scrollbar-gutter:stable]"
            data-plan-visible-tasks
            data-plan-task-list-scroll="single"
          >
            {displayedTasks.map((task, index) => renderTask(task, index))}
          </ul>
        ) : (
          <div className="shrink-0 text-[var(--app-text-subtle)]">
            All tasks completed
          </div>
        )}
        {disclosureCount > 0 ? (
          <div
            className="mt-1.5 shrink-0"
            data-plan-task-expansion
            data-plan-completed-tasks={completedTasks.length > 0 ? "" : undefined}
          >
            <button
              type="button"
              className="flex w-full items-center gap-1 py-1 text-left text-[10px] font-medium text-[var(--app-text-subtle)]/80 transition-colors hover:text-[var(--app-text-muted)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]"
              aria-expanded={expanded}
              aria-controls="desktop-plan-task-list"
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
          </div>
        ) : null}
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
        "text-[10px] font-semibold uppercase tracking-[0.12em] opacity-90",
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
    <div className="mt-3 min-w-0" data-plan-checkpoint-box-wrapper>
      <h3
        className={cn(
          "min-w-0 line-clamp-3 break-words rounded-xl border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)] px-3 py-2.5 font-mono text-sm font-medium leading-relaxed text-[var(--app-text)]/95 [overflow-wrap:anywhere]",
        )}
        title={activeTitle}
        data-plan-checkpoint-title
        data-plan-checkpoint-treatment="console-block"
      >
        <span className="mr-1.5 inline font-mono text-[10px] font-bold tracking-wide text-[var(--app-primary)]/80 before:content-['>_']">
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
          className="min-w-0 text-[10px] font-semibold uppercase tracking-[0.2em] text-[var(--app-text-subtle)]/90"
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
  unified = false,
}: {
  view: DesktopPlanExecutionView;
  checkpoints: DesktopSessionPlanCheckpoint[];
  completedCount: number;
  totalCount: number;
  activeIndex: number;
  onOpenPlan?: () => void;
  unified?: boolean;
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
    <section
      className={cn(
        "min-w-0 p-4",
        unified
          ? ""
          : "rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
      )}
      data-plan-section="checkpoint"
      data-plan-section-treatment={unified ? "unified-stack" : "inset-card"}
    >
      <CurrentCheckpointRow
        activeTitle={activeTitle}
        checkpointId={checkpointId}
        title={title}
        status={statusLabel(view, checkpoint)}
        tone={tone}
      />

      <div className="mt-4" data-plan-progress>
        <div className="mb-1.5 flex items-center justify-between text-[10px] text-[var(--app-text-subtle)]/90">
          <span>Progress</span>
          <span className="font-medium text-[var(--app-text-muted)]">
            {completedCount} / {totalCount}
          </span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full bg-[var(--app-border)]/40">
          <div
            className="h-full rounded-full bg-[var(--app-primary)]/70 transition-all duration-300"
            style={{ width: `${progressValue}%` }}
          />
        </div>
      </div>

      <div
        className="mt-4 min-w-0 rounded-xl border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)] p-3 text-xs"
        data-plan-next-up
      >
        <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
          Next up
        </div>
        {nextCheckpoint ? (
          <div
            className="mt-2.5 flex min-w-0 items-start gap-2.5"
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

      <CheckpointDetails checkpoint={checkpoint} />

      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={onOpenPlan}
        disabled={!onOpenPlan}
        className="mt-4 h-9 w-full rounded-xl border border-[var(--app-border)]/70 bg-transparent text-xs font-medium text-[var(--app-text-muted)] transition-all hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
      >
        Open full plan
      </Button>
      <p className="mt-2 px-1 text-[11px] leading-4 text-[var(--app-text-subtle)]">
        You can ask Swarm to remake a plan at any time, just pause and ask.
      </p>
    </section>
  );
}

function ActionsSection({
  view,
  busyAction,
  canStop,
  onAction,
  unified = false,
}: DesktopPlanExecutionSidebarProps & { view: DesktopPlanExecutionView; unified?: boolean }) {
  const checkpointId =
    view.activeCheckpointId || view.activeCheckpoint?.id || "";
  const acceptBusy =
    busyAction === actionBusyKey("accept_checkpoint", checkpointId);
  const resumeBusy =
    busyAction === actionBusyKey("resume_checkpoint", checkpointId);
  const archiveBusy = busyAction === actionBusyKey("archive_plan");
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
      <section className={cn("min-w-0 p-3", unified ? "border-t border-[var(--app-border)]/60" : "rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]")} data-plan-section="actions" data-plan-section-treatment={unified ? "unified-stack" : "inset-card"}>
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
      <section className={cn("min-w-0 p-3", unified ? "border-t border-[var(--app-border)]/60" : "rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]")} data-plan-section="actions" data-plan-section-treatment={unified ? "unified-stack" : "inset-card"}>
        <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          Blocked
        </div>
        <div className="mt-2 border-y border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-2.5 py-2" data-plan-system-message>
          <div className="text-xs font-semibold text-[var(--app-text)]">
            Blocked checkpoint
          </div>
          <p className="mt-0.5 text-[11px] leading-4 text-[var(--app-text-muted)]">
            When the dependency is resolved, tell Swarm what changed and ask it
            to continue. Swarm will clear the blocker and resume this same
            checkpoint in fresh context. It will finish any remaining work and
            explicitly complete the checkpoint before anything later starts.
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
    return null;
  }

  return (
    <section className={cn("min-w-0 p-3", unified ? "border-t border-[var(--app-border)]/60" : "rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]")} data-plan-section="actions" data-plan-section-treatment={unified ? "unified-stack" : "inset-card"}>
      <div className="text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
        Actions
      </div>

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
              "hidden h-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden border-l border-[var(--app-border)]/60 bg-[var(--app-bg-alt)] font-sans min-[1300px]:flex",
              thin
                ? "w-[56px] max-w-[56px] px-2 py-3"
                : compact
                  ? "w-[292px] max-w-[292px] p-3"
                  : "w-[372px] max-w-[372px] p-4",
            )}
        aria-label="Plan execution sidebar"
        data-testid="desktop-plan-execution-sidebar"
        data-display-mode={displayMode}
      >
        <div
          className={cn(
            "min-w-0 max-w-full [&_*]:min-w-0",
            embedded
              ? "grid content-start gap-4 overflow-visible"
              : "flex min-h-0 flex-1 flex-col gap-3 overflow-hidden",
          )}
        >
          {!embedded && thin ? (
            <header className="shrink-0 pb-1 text-center">
              <div
                className="text-center text-xs font-semibold tracking-tight text-[var(--app-text)]"
                title="Plan execution"
              >
                P
              </div>
            </header>
          ) : null}
          {thin ? (
            <div
              className="grid min-h-0 flex-1 content-start gap-3 overflow-hidden py-1"
              data-plan-thin-rail
            >
              <button
                type="button"
                onClick={onEditPlan}
                disabled={!onEditPlan}
                className="grid min-h-11 place-items-center rounded-lg border border-[var(--app-border)] text-xs font-semibold text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
                title={`${statusLabel(view, view.activeCheckpoint)} · ${completedCount}/${totalCount} checkpoints`}
                aria-label={`Open full plan. ${statusLabel(view, view.activeCheckpoint)}. ${completedCount} of ${totalCount} checkpoints complete.`}
              >
                {completedCount}/{totalCount}
              </button>
              {taskChildren.length > 0 ? (
                <DesktopPlanSubagentList
                  children={taskChildren}
                  actions={taskChildActions}
                  mode="thin"
                />
              ) : null}
            </div>
          ) : (
            <div
              className={cn(
                "grid content-start",
                embedded
                  ? "gap-4"
                  : "min-h-0 shrink overflow-hidden rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
              )}
              data-plan-scroll-region
              data-plan-top-stack={!embedded ? "unified" : undefined}
            >
              <div
                className={cn(
                  "grid content-start",
                  embedded
                    ? "gap-4"
                    : "min-h-0 max-h-full overflow-y-auto [scrollbar-gutter:stable]",
                )}
                data-plan-top-stack-content={!embedded ? "scrollable" : undefined}
              >
                <ActiveCheckpointSection
                  view={view}
                  checkpoints={checkpoints}
                  completedCount={completedCount}
                  totalCount={totalCount}
                  activeIndex={activeIndex}
                  onOpenPlan={onEditPlan}
                  unified={!embedded}
                />
                {taskChildren.length > 0 ? <DesktopPlanSubagentList children={taskChildren} actions={taskChildActions} mode={compact ? "compact" : "full"} /> : null}
                <ActionsSection
                  view={view}
                  busyAction={busyAction}
                  canStop={canStop}
                  onAction={onAction}
                  onEditPlan={onEditPlan}
                  unified={!embedded}
                />
              </div>
            </div>
          )}
          {!thin && belowActions ? (
            <div
              className={cn(
                embedded
                  ? ""
                  : "flex min-h-[7rem] flex-1 flex-col overflow-hidden rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3 shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
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
