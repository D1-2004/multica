"use client";

import { useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { agentCoordinatorSessionsOptions } from "@multica/core/agents";
import { chatMessagesPageOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import type { ChatMessage, ChatSession } from "@multica/core/types";
import { RichContent } from "../../../rich-content";
import { useT } from "../../../i18n";

function formatChatTime(dateStr: string): string {
  const d = new Date(dateStr);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  if (d.getFullYear() === now.getFullYear()) {
    return d.toLocaleDateString([], { month: "numeric", day: "numeric" });
  }
  return d.toLocaleDateString();
}

function toPreview(content: string): string {
  return content
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#*`>~]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

function CoordinatorTranscript({
  session,
  onBack,
}: {
  session: ChatSession;
  onBack: () => void;
}) {
  const { t } = useT("agents");
  const {
    data: rawMessagePages,
    isLoading,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteQuery(chatMessagesPageOptions(session.id));
  const messages = useMemo(() => {
    const pages = rawMessagePages?.pages ?? [];
    return [...pages].reverse().flatMap((page) => page.messages);
  }, [rawMessagePages]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-12 shrink-0 items-center gap-2 border-b px-3">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="md:hidden"
          onClick={onBack}
        >
          <ChevronLeft className="h-4 w-4" />
          {t(($) => $.tab_body.inbound.back)}
        </Button>
        <div className="min-w-0 truncate text-body font-medium">
          {session.title.trim() || t(($) => $.tab_body.inbound.untitled)}
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        {isLoading ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.loading)}
          </p>
        ) : messages.length === 0 ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.no_messages)}
          </p>
        ) : (
          <div className="mx-auto flex max-w-2xl flex-col gap-3">
            {hasNextPage ? (
              <div className="flex justify-center">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={isFetchingNextPage}
                  onClick={() => void fetchNextPage()}
                >
                  {t(($) => $.tab_body.inbound.load_older)}
                </Button>
              </div>
            ) : null}
            {messages.map((message) => (
              <CoordinatorMessage key={message.id} message={message} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function CoordinatorMessage({ message }: { message: ChatMessage }) {
  const isUser = message.role === "user";
  if (isUser) {
    return (
      <div className="flex justify-end">
        <div className="max-w-[80%] break-words rounded-2xl bg-muted px-3.5 py-2 text-body">
          <RichContent
            content={message.content}
            attachments={message.attachments}
            density="compact"
            phase="settled"
          />
        </div>
      </div>
    );
  }
  return (
    <div className="max-w-[90%] break-words text-body">
      <RichContent
        content={message.content}
        attachments={message.attachments}
        density="compact"
        phase="settled"
      />
    </div>
  );
}

export function CoordinatorSessionsTab({ agentId }: { agentId: string }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data: sessions = [], isLoading, isError, refetch } = useQuery(
    agentCoordinatorSessionsOptions(wsId, agentId),
  );
  const [selectedId, setSelectedId] = useState<string | null>(null);

  useEffect(() => {
    if (selectedId && !sessions.some((session) => session.id === selectedId)) {
      setSelectedId(null);
    }
  }, [selectedId, sessions]);

  const selected = sessions.find((session) => session.id === selectedId) ?? null;
  const showTranscript = selected != null;

  return (
    <div className="flex min-h-[620px] flex-1">
      <aside
        className={cn(
          "w-full shrink-0 overflow-y-auto border-r md:w-72",
          showTranscript && "hidden md:block",
        )}
      >
        {isLoading ? (
          <p className="p-4 text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.loading)}
          </p>
        ) : isError ? (
          <div className="space-y-2 p-4">
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.inbound.load_failed)}
            </p>
            <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
              {t(($) => $.tab_body.inbound.retry)}
            </Button>
          </div>
        ) : sessions.length === 0 ? (
          <p className="p-4 text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.empty)}
          </p>
        ) : (
          <ul className="divide-y">
            {sessions.map((session) => {
              const active = session.id === selectedId;
              const preview = session.last_message?.content
                ? toPreview(session.last_message.content)
                : "";
              const time = formatChatTime(
                session.last_message?.created_at ?? session.updated_at,
              );
              return (
                <li key={session.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(session.id)}
                    className={cn(
                      "flex w-full flex-col gap-1 px-4 py-3 text-left hover:bg-surface-hover",
                      active && "bg-surface-selected",
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="min-w-0 truncate text-body font-medium">
                        {session.title.trim() ||
                          t(($) => $.tab_body.inbound.untitled)}
                      </span>
                      <span className="shrink-0 text-caption text-muted-foreground">
                        {time}
                      </span>
                    </div>
                    {preview ? (
                      <span className="line-clamp-2 text-caption text-muted-foreground">
                        {preview}
                      </span>
                    ) : null}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </aside>
      <section
        className={cn(
          "min-w-0 flex-1",
          !showTranscript && "hidden md:flex md:items-center md:justify-center",
          showTranscript && "flex",
        )}
      >
        {selected ? (
          <CoordinatorTranscript
            session={selected}
            onBack={() => setSelectedId(null)}
          />
        ) : (
          <p className="p-4 text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.select_prompt)}
          </p>
        )}
      </section>
    </div>
  );
}
