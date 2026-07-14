import { useMemo, useState } from "react";
import { AlertCircle, Check, Copy } from "lucide-react";
import { Button } from "../../../../components/ui/button";
import type { DesktopPermissionRecord } from "../../types/realtime";
import {
  normalizeStructuredPlanDocument,
  structuredPlanDocumentToWire,
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
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");
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

  const copyPlan = async () => {
    const copyText = document
      ? document.displayText ||
        document.renderedText ||
        JSON.stringify(structuredPlanDocumentToWire(document), null, 2)
      : fallback;
    try {
      await navigator.clipboard.writeText(copyText);
      setCopyState("copied");
    } catch {
      setCopyState("error");
    }
  };

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
        <div className="flex flex-col items-end gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={loading}
            onClick={() => void copyPlan()}
          >
            {copyState === "copied" ? (
              <Check className="size-4" />
            ) : copyState === "error" ? (
              <AlertCircle className="size-4" />
            ) : (
              <Copy className="size-4" />
            )}
            {copyState === "copied" ? "Copied" : copyState === "error" ? "Copy failed" : "Copy"}
          </Button>
          {pendingCount > 1 ? (
            <div className="text-xs text-[var(--app-text-muted)]">
              {pendingPosition} of {pendingCount} pending plans
            </div>
          ) : null}
        </div>
      </div>

      <div className="mt-4">
        {document ? (
          <StructuredPlanReviewView document={document} />
        ) : (
          <ChatMarkdown content={fallback} className="text-sm leading-6" />
        )}
      </div>

      <div className="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] pt-4">
        {supportsExecutionChoice ? (
          <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-[var(--app-text)]">
            <input
              type="checkbox"
              role="switch"
              className="peer sr-only"
              checked={pauseForReview}
              disabled={loading}
              onChange={(event) => setPauseForReview(event.target.checked)}
            />
            <span
              aria-hidden="true"
              className="relative h-5 w-9 rounded-full bg-[var(--app-border-strong)] transition-colors after:absolute after:left-0.5 after:top-0.5 after:size-4 after:rounded-full after:bg-white after:shadow-sm after:transition-transform peer-checked:bg-[var(--app-primary)] peer-checked:after:translate-x-4 peer-disabled:opacity-50"
            />
            Pause for review after each checkpoint
          </label>
        ) : (
          <span />
        )}

        <div className="flex flex-wrap justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={loading}
            onClick={() => void resolve("deny")}
          >
            Reject
          </Button>
          <Button
            type="button"
            disabled={loading}
            onClick={() => void resolve("approve")}
          >
            Accept
          </Button>
        </div>
      </div>
    </section>
  );
}
