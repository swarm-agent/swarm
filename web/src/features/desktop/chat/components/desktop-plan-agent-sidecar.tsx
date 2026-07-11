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
  onClose: () => void;
  onSendChanges: (draft: string) => Promise<void>;
}

export function DesktopPlanAgentSidecar({
  parentSessionId,
  permission,
  document,
  onClose,
  onSendChanges,
}: DesktopPlanAgentSidecarProps) {
  const [sessionId, setSessionId] = useState("");
  const [messages, setMessages] = useState<ChatMessageRecord[]>([]);
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
        const [sidecarHistory, parentHistory] = await Promise.all([
          fetchSessionMessages(result.sessionId, undefined, 0, { sessionApi: "v3", tail: true, limit: 100 }),
          fetchSessionMessages(parentSessionId, undefined, 0, { sessionApi: "v3", tail: true, limit: 40 }),
        ]);
        if (sidecarHistory.messages.length === 0 && parentHistory.messages.length > 0) {
          await sendSessionMessage(
            result.sessionId,
            "system",
            `Parent conversation context for this plan review:\n${parentHistory.messages
              .map((message) => `${message.role}: ${message.content}`)
              .join("\n\n")}`,
            null,
            { sessionApi: "v3", clientRequestId: `plan-review-context:${permission.id}` },
          );
        }
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
  }, [document, parentSessionId, permission.id, refresh]);

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
      className="fixed inset-0 z-50 bg-black/30 md:left-auto md:w-[28rem]"
      data-testid="desktop-plan-agent-sidecar"
    >
      <aside className="absolute inset-x-0 bottom-0 flex max-h-[88vh] flex-col rounded-t-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-2xl md:inset-y-0 md:right-0 md:max-h-none md:w-[28rem] md:rounded-none md:rounded-l-2xl">
        <header className="flex items-center justify-between border-b border-[var(--app-border)] px-4 py-3">
          <div className="flex items-center gap-2">
            <Bot size={18} />
            <div>
              <div className="font-semibold">Plan Agent</div>
              <div className="text-xs text-[var(--app-text-muted)]">
                Explains this proposal and drafts requested changes
              </div>
            </div>
          </div>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-9 w-9 px-0"
            aria-label="Close Plan Agent"
            onClick={onClose}
          >
            <X size={18} />
          </Button>
        </header>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
          {busy && messages.length === 0 ? (
            <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
              <Loader2 className="animate-spin" size={16} />
              Opening durable plan review…
            </div>
          ) : null}
          {messages.map((message) => (
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
            placeholder="Ask about the complete plan or request a rejection draft…"
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
          {draft ? (
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
                Plan Agent cannot resolve this proposal. This button explicitly
                denies it with the draft above.
              </p>
            </div>
          ) : null}
        </div>
      </aside>
    </div>
  );
}
