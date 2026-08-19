import { useMemo, useState } from "react";
import { AlertCircle, Check, Copy, MessageCircle } from "lucide-react";
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
import { savePlanAcceptanceMode } from "../../permissions/services/capability-policy";

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
  onAskForChanges?: () => void;
  resolutionPending?: boolean;
}

export function DesktopInlinePlanReviewCard({
  permission,
  parentSessionId: _parentSessionId,
  pendingPosition,
  pendingCount,
  onResolve,
  onAskForChanges,
  resolutionPending = false,
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
  const [loading, setLoading] = useState(false);
  const [copyState, setCopyState] = useState<"idle" | "copied" | "error">("idle");
  const resolving = loading || resolutionPending;
  const supportsExecutionChoice =
    kind === "exit-plan" ||
    kind === "plan-new-request" ||
    kind === "plan-amendment-request" ||
    kind === "plan-followup-request";
  const supportsPersistentAcceptance =
    kind === "exit-plan" || kind === "plan-followup-request" || kind === "plan-amendment-request" || kind === "plan-new-request";
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

  const resolve = async (action: "approve" | "deny" | "approve_always") => {
    if (loading) return;
    setLoading(true);
    try {
      // The pending backend permission remains proposal authority. The client may
      // only overlay execution policy, never replay a stale proposal document.
      if (action === "approve_always") await savePlanAcceptanceMode("always_allow");
      const approvedArguments = action !== "deny" && supportsExecutionChoice
        ? exitPlanExecutionArguments()
        : undefined;
      await onResolve(permission, action === "deny" ? "deny" : "approve", "", approvedArguments);
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
          <div className="flex items-center gap-2">
            {onAskForChanges ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="xl:hidden"
                disabled={resolving}
                onClick={onAskForChanges}
              >
                <MessageCircle className="size-4" />
                Ask Swarm
              </Button>
            ) : null}
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={resolving}
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
          </div>
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
          <span className="text-sm text-[var(--app-text-muted)]">Starts automatically after approval</span>
        ) : (
          <span />
        )}

        <div className="flex flex-wrap justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={resolving}
            onClick={() => void resolve("deny")}
          >
            Reject
          </Button>
          {supportsPersistentAcceptance ? (
            <Button type="button" variant="outline" disabled={resolving} onClick={() => void resolve("approve_always")}>
              Always allow
            </Button>
          ) : null}
          <Button
            type="button"
            disabled={resolving}
            onClick={() => void resolve("approve")}
          >
            {resolutionPending ? "Starting execution…" : "Accept once"}
          </Button>
        </div>
      </div>
    </section>
  );
}
