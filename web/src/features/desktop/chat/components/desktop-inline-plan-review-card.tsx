import { useMemo, useState } from "react";
import { Button } from "../../../../components/ui/button";
import type { DesktopPermissionRecord } from "../../types/realtime";
import {
  normalizeStructuredPlanDocument,
  StructuredPlanReviewView,
} from "./structured-plan-document";
import {
  parseExitPlanPermission,
  parsePlanUpdatePermission,
  permissionKind,
} from "../../permissions/services/permission-payload";
import { exitPlanExecutionArguments } from "../../permissions/components/desktop-permission-modal";
import { ChatMarkdown } from "./chat-markdown";

export function structuredPlanDocumentFromPermission(
  permission: DesktopPermissionRecord,
) {
  const kind = permissionKind(permission);
  const exitPayload =
    kind === "exit-plan" ? parseExitPlanPermission(permission) : null;
  const planPayload =
    kind === "exit-plan" ? null : parsePlanUpdatePermission(permission);
  return normalizeStructuredPlanDocument(
    exitPayload?.document ??
      planPayload?.document ??
      planPayload?.approvedArguments.document,
  );
}

interface DesktopInlinePlanReviewCardProps {
  permission: DesktopPermissionRecord;
  parentSessionId: string;
  pendingPosition: number;
  pendingCount: number;
  onOpenPlanAgent?: () => void;
  onResolve: (
    permission: DesktopPermissionRecord,
    action: "approve" | "deny",
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) => Promise<void>;
}

export function DesktopInlinePlanReviewCard({
  permission,
  parentSessionId: _parentSessionId,
  pendingPosition,
  pendingCount,
  onOpenPlanAgent,
  onResolve,
}: DesktopInlinePlanReviewCardProps) {
  const kind = permissionKind(permission);
  const exitPayload =
    kind === "exit-plan" ? parseExitPlanPermission(permission) : null;
  const planPayload =
    kind === "exit-plan" ? null : parsePlanUpdatePermission(permission);
  const document = useMemo(
    () => structuredPlanDocumentFromPermission(permission),
    [permission],
  );
  const [pauseForReview, setPauseForReview] = useState(false);
  const [loading, setLoading] = useState(false);
  const supportsExecutionChoice =
    kind === "exit-plan" || kind === "plan-new-request";
  const title =
    document?.title ||
    exitPayload?.title ||
    planPayload?.title ||
    "Plan proposal";
  const fallback =
    exitPayload?.body ||
    planPayload?.plan ||
    planPayload?.changeRequest ||
    "Review this plan proposal.";

  const resolve = async (action: "approve" | "deny") => {
    if (loading) return;
    setLoading(true);
    try {
      // The pending backend permission remains proposal authority. The client may
      // only overlay execution policy, never replay a stale proposal document.
      const approvedArguments = action === "approve" && supportsExecutionChoice
        ? exitPlanExecutionArguments(pauseForReview)
        : undefined;
      await onResolve(permission, action, "", approvedArguments);
    } finally {
      setLoading(false);
    }
  };

  return (
    <section
      className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 shadow-sm"
      data-testid="desktop-inline-plan-review"
      data-permission-id={permission.id}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-primary)]">
            Pending plan edit
          </div>
          <h2 className="mt-1 text-lg font-semibold text-[var(--app-text)]">
            {title}
          </h2>
        </div>
        {pendingCount > 1 ? (
          <div className="text-xs text-[var(--app-text-muted)]">
            {pendingPosition} of {pendingCount} pending plans
          </div>
        ) : null}
      </div>

      <div className="mt-4">
        {document ? (
          <StructuredPlanReviewView document={document} />
        ) : (
          <ChatMarkdown content={fallback} className="text-sm leading-6" />
        )}
      </div>

      {supportsExecutionChoice ? (
        <label className="mt-4 flex items-center gap-2 text-sm text-[var(--app-text)]">
          <input
            type="checkbox"
            checked={pauseForReview}
            disabled={loading}
            onChange={(event) => setPauseForReview(event.target.checked)}
          />
          Pause for review after each checkpoint
        </label>
      ) : null}

      <div className="mt-4 flex flex-wrap justify-end gap-2">
        {document && onOpenPlanAgent ? (
          <Button
            type="button"
            variant="outline"
            disabled={loading}
            onClick={onOpenPlanAgent}
          >
            Request another revision
          </Button>
        ) : null}
        <Button
          type="button"
          variant="outline"
          disabled={loading}
          onClick={() => void resolve("deny")}
        >
          Reject edit
        </Button>
        <Button
          type="button"
          disabled={loading}
          onClick={() => void resolve("approve")}
        >
          Accept edit
        </Button>
      </div>
    </section>
  );
}
