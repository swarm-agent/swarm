import { useCallback, useEffect, useState } from "react";
import { Bot, Loader2, X } from "lucide-react";
import { Button } from "../../../../components/ui/button";
import { Textarea } from "../../../../components/ui/textarea";
import type { DesktopPermissionRecord } from "../../types/realtime";
import type { StructuredPlanDocument } from "./structured-plan-document";
import { ChatMarkdown } from "./chat-markdown";
import {
  createPlanReviewSidecar,
  fetchSessionMessages,
  sendSessionMessage,
} from "../queries/chat-queries";
import type { ChatMessageRecord } from "../types/chat";

interface DesktopPlanAgentSidecarProps {
  parentSessionId: string;
  permission: DesktopPermissionRecord;
  document: StructuredPlanDocument;
  onClose?: () => void;
  onSendChanges: (draft: string) => Promise<void>;
  embedded?: boolean;
  modelLabel?: string;
  mobileOpen?: boolean;
  allowSendChanges?: boolean;
}

export function DesktopPlanAgentSidecar({
  parentSessionId,
  permission,
  document,
  onClose,
  onSendChanges,
  embedded = false,
  modelLabel = "",
  mobileOpen = true,
  allowSendChanges = true,
}: DesktopPlanAgentSidecarProps) {
  const [sessionId, setSessionId] = useState("");
  const [messages, setMessages] = useState<ChatMessageRecord[]>([]);
  const [originLabel, setOriginLabel] = useState("");
  const [question, setQuestion] = useState("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(true);
  const [sendingChanges, setSendingChanges] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async (id: string) => {
    const result = await fetchSessionMessages(id, undefined, 0, {
      sessionApi: "v3",
      tail: true,
      limit: 100,
    });
    setMessages(result.messages);
    const latestAssistant = [...result.messages]
      .reverse()
      .find((message) => message.role === "assistant");
    if (latestAssistant?.content) setDraft(latestAssistant.content);
  }, []);

  useEffect(() => {
    let cancelled = false;
    setBusy(true);
    setError(null);
    void createPlanReviewSidecar({
      parentSessionId,
      permissionId: permission.id,
      planId: document.id || permission.id,
      planRevision: Math.max(1, Number.parseInt(document.revisionId, 10) || 1),
      plan: document,
    })
      .then(async (result) => {
        if (cancelled) return;
        setSessionId(result.sessionId);
        setOriginLabel(
          [result.originatingAgentName, result.model || modelLabel]
            .filter(Boolean)
            .join(" · "),
        );
        await refresh(result.sessionId);
      })
      .catch((cause) => {
        if (!cancelled)
          setError(
            cause instanceof Error
              ? cause.message
              : "Unable to open Plan Agent.",
          );
      })
      .finally(() => {
        if (!cancelled) setBusy(false);
      });
    return () => {
      cancelled = true;
    };
  }, [document, modelLabel, parentSessionId, permission.id, refresh]);

  useEffect(() => {
    if (!sessionId || busy) return;
    const timer = window.setInterval(
      () => void refresh(sessionId).catch(() => undefined),
      1500,
    );
    return () => window.clearInterval(timer);
  }, [busy, refresh, sessionId]);

  const ask = async () => {
    const content = question.trim();
    if (!content || !sessionId || busy) return;
    setBusy(true);
    setError(null);
    try {
      await sendSessionMessage(sessionId, "user", content, null, {
        sessionApi: "v3",
      });
      setQuestion("");
      await refresh(sessionId);
    } catch (cause) {
      setError(
        cause instanceof Error ? cause.message : "Plan Agent could not answer.",
      );
    } finally {
      setBusy(false);
    }
  };

  const sendChanges = async () => {
    const reason = draft.trim();
    if (!reason || sendingChanges) return;
    setSendingChanges(true);
    setError(null);
    try {
      await onSendChanges(reason);
      onClose();
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "The plan proposal is no longer available.",
      );
    } finally {
      setSendingChanges(false);
    }
  };

  return (
    <div
      className={embedded
        ? mobileOpen
          ? "fixed inset-0 z-50 flex bg-black/30 xl:static xl:z-auto xl:min-h-0 xl:w-[360px] xl:max-w-[360px] xl:border-l xl:border-[var(--app-border)] xl:bg-[var(--app-surface)]"
          : "hidden xl:flex xl:min-h-0 xl:w-[360px] xl:max-w-[360px] xl:border-l xl:border-[var(--app-border)] xl:bg-[var(--app-surface)]"
        : "fixed inset-0 z-50 bg-black/30 md:left-auto md:w-[28rem]"}
      data-testid="desktop-plan-agent-sidecar"
      data-embedded={embedded ? "true" : "false"}
    >
      <aside className={embedded
        ? "absolute inset-x-0 bottom-0 flex max-h-[88vh] min-h-0 min-w-0 flex-col rounded-t-2xl bg-[var(--app-surface)] shadow-2xl xl:static xl:max-h-none xl:flex-1 xl:rounded-none xl:shadow-none"
        : "absolute inset-x-0 bottom-0 flex max-h-[88vh] flex-col rounded-t-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-2xl md:inset-y-0 md:right-0 md:max-h-none md:w-[28rem] md:rounded-none md:rounded-l-2xl"}>
        <header className="flex items-center justify-between border-b border-[var(--app-border)] px-4 py-3">
          <div className="flex items-center gap-2">
            <Bot size={18} />
            <div>
              <div className="font-semibold">Plan Agent</div>
              <div className="text-xs text-[var(--app-text-muted)]">
                {originLabel || modelLabel
                  ? `Plan Agent · ${originLabel || modelLabel}`
                  : "Plan Agent"}
              </div>
            </div>
          </div>
          {onClose ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className={embedded ? "h-9 w-9 px-0 xl:hidden" : "h-9 w-9 px-0"}
              aria-label="Close Plan Agent"
              onClick={onClose}
            >
              <X size={18} />
            </Button>
          ) : null}
        </header>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
          <div className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-3 text-sm leading-5 text-[var(--app-text)]">
            Ask me anything about the plan, or if you&apos;d like any changes made.
          </div>
          {busy && messages.length === 0 ? (
            <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
              <Loader2 className="animate-spin" size={16} />
              Opening durable plan review…
            </div>
          ) : null}
          {messages.filter((message) => message.role === "user" || message.role === "assistant").map((message) => (
            <div
              key={message.id}
              className={
                message.role === "user"
                  ? "ml-8 rounded-xl bg-[var(--app-surface-hover)] p-3 text-sm"
                  : "mr-4 rounded-xl border border-[var(--app-border)] p-3 text-sm"
              }
            >
              <ChatMarkdown content={message.content} />
            </div>
          ))}
          {error ? (
            <div
              role="alert"
              className="rounded-lg border border-[var(--app-danger)] p-3 text-sm text-[var(--app-danger)]"
            >
              {error}
            </div>
          ) : null}
        </div>
        <div className="space-y-3 border-t border-[var(--app-border)] p-4">
          <Textarea
            value={question}
            onChange={(event) => setQuestion(event.target.value)}
            placeholder="Ask about the plan or request changes…"
            disabled={busy || !sessionId}
          />
          <Button
            type="button"
            variant="outline"
            className="w-full"
            disabled={busy || !question.trim() || !sessionId}
            onClick={() => void ask()}
          >
            {busy ? "Waiting for Plan Agent…" : "Ask Plan Agent"}
          </Button>
          {draft && allowSendChanges ? (
            <div className="space-y-2 border-t border-[var(--app-border)] pt-3">
              <label
                className="text-xs font-semibold text-[var(--app-text-muted)]"
                htmlFor={`plan-agent-draft-${permission.id}`}
              >
                Draft changes for Swarm
              </label>
              <Textarea
                id={`plan-agent-draft-${permission.id}`}
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                className="min-h-28"
              />
              <Button
                type="button"
                className="w-full"
                disabled={sendingChanges || !draft.trim()}
                onClick={() => void sendChanges()}
              >
                {sendingChanges ? "Sending…" : "Send changes to Swarm"}
              </Button>
              <p className="text-xs text-[var(--app-text-muted)]">
                Sending changes explicitly returns this proposal to Swarm with the draft above.
              </p>
            </div>
          ) : null}
        </div>
      </aside>
    </div>
  );
}
