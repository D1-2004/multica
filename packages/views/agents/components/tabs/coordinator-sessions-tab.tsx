"use client";

import { useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, MessageSquare } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import {
  agentCoordinatorConversationsOptions,
  agentCoordinatorConversationsKeys,
  agentCoordinatorConversationMessagesOptions,
} from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Agent, CoordinatorConversation } from "@multica/core/types";
import {
  ChatMessageList,
  ChatMessageSkeleton,
} from "../../../chat/components/chat-message-list";
import { useT, useTimeAgo } from "../../../i18n";

/**
 * Transcript of one inbound Coordinator chat session. Also used by the scene
 * detail (场域 → 入站记录), which knows the latest session of its
 * conversation.
 */
export function CoordinatorConversationMessages({
  agentId,
  sessionId,
}: {
  agentId: string;
  sessionId: string;
}) {
  const wsId = useWorkspaceId();
  const { t } = useT("agents");
  const query = useInfiniteQuery(
    agentCoordinatorConversationMessagesOptions(wsId, agentId, sessionId),
  );
  const pages = query.data?.pages ?? [];
  const messages = [...pages].reverse().flatMap((page) => page.messages);
  const olderCount = pages
    .slice(1)
    .reduce((sum, page) => sum + page.messages.length, 0);
  return (
    <div className="flex min-h-0 flex-1 flex-col @container">
      {query.isError && (
        <div
          role="alert"
          className="flex shrink-0 items-center gap-2 p-3 text-caption"
        >
          {t(($) => $.tab_body.inbound.load_failed)}
          <Button
            variant="outline"
            size="sm"
            onClick={() =>
              void (query.isFetchNextPageError
                ? query.fetchNextPage()
                : query.refetch())
            }
          >
            {t(($) => $.tab_body.inbound.retry)}
          </Button>
        </div>
      )}
      {query.isLoading ? (
        <ChatMessageSkeleton />
      ) : messages.length > 0 ? (
        <ChatMessageList
          messages={messages}
          pendingTask={null}
          availability={undefined}
          firstItemIndex={1_000_000 - olderCount}
          hasOlderMessages={query.hasNextPage}
          isFetchingOlderMessages={query.isFetchingNextPage}
          onLoadOlderMessages={() => {
            if (!query.isFetchingNextPage && !query.isFetchNextPageError)
              void query.fetchNextPage();
          }}
          quickActionsDisabled
        />
      ) : !query.isError ? (
        <p className="p-4 text-caption text-muted-foreground">
          {t(($) => $.tab_body.inbound.no_messages)}
        </p>
      ) : null}
    </div>
  );
}

export function CoordinatorSessionsTab({ agent }: { agent: Agent }) {
  const queryClient = useQueryClient();
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const isCompact = useIsCompact();
  const timeAgo = useTimeAgo();
  const query = useInfiniteQuery(
    agentCoordinatorConversationsOptions(wsId, agent.id),
  );
  const conversations = [
    ...new Map(
      (query.data?.pages ?? [])
        .flatMap((page) => page.conversations)
        .map((item) => [item.id, item]),
    ).values(),
  ];
  // Keep the selected anchor session stable when a newer judgment updates the list.
  const [selected, setSelected] = useState<CoordinatorConversation | null>(
    null,
  );
  const title = (item: CoordinatorConversation) =>
    item.title ||
    (item.conversation_type === "group"
      ? t(($) => $.tab_body.inbound.memory_untitled_group)
      : t(($) => $.tab_body.inbound.untitled));
  const source = (item: CoordinatorConversation) =>
    item.source === "digital_employee"
      ? t(($) => $.tab_body.inbound.source_employee)
      : item.source === "robot"
        ? t(($) => $.tab_body.inbound.source_robot)
        : t(($) => $.tab_body.inbound.source_unknown);

  return (
    <div className="flex h-full min-h-0 flex-1">
      <aside
        className={cn(
          "min-h-0 flex-col border-r",
          isCompact
            ? selected
              ? "hidden"
              : "flex w-full"
            : "flex w-80 shrink-0",
        )}
      >
        <div className="shrink-0 space-y-1 p-4">
          <h2 className="text-body font-semibold">
            {t(($) => $.tabs.inbound)}
          </h2>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.grouped_hint)}
          </p>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
          {query.isLoading && (
            <p className="p-2 text-caption text-muted-foreground">
              {t(($) => $.tab_body.inbound.loading)}
            </p>
          )}
          {query.isError && (
            <div role="alert" className="space-y-2 p-2 text-caption">
              <p>{t(($) => $.tab_body.inbound.load_failed)}</p>
              <Button
                size="sm"
                variant="outline"
                onClick={() =>
                  void (query.isFetchNextPageError
                    ? query.fetchNextPage()
                    : query.refetch())
                }
              >
                {t(($) => $.tab_body.inbound.retry)}
              </Button>
            </div>
          )}
          {!query.isLoading && !query.isError && conversations.length === 0 && (
            <p className="p-2 text-caption text-muted-foreground">
              {t(($) => $.tab_body.inbound.empty)}
            </p>
          )}
          {conversations.map((item) => (
            <button
              key={item.id}
              type="button"
              aria-pressed={selected?.id === item.id}
              onClick={() => setSelected(item)}
              className={cn(
                "mb-1 w-full rounded-md p-3 text-left hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring",
                selected?.id === item.id &&
                  "bg-accent font-semibold text-accent-foreground",
              )}
            >
              <div className="truncate text-body">{title(item)}</div>
              <div className="mt-1 flex gap-2 text-caption font-normal text-muted-foreground">
                <span className="truncate">{source(item)}</span>
                <span className="ml-auto shrink-0">
                  {t(($) => $.tab_body.inbound.judgment_count, {
                    count: item.session_count,
                  })}
                </span>
              </div>
              <div className="mt-1 text-caption font-normal text-muted-foreground">
                {timeAgo(item.updated_at)}
              </div>
            </button>
          ))}
          {query.hasNextPage && (
            <Button
              className="mt-2 w-full"
              variant="outline"
              disabled={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage
                ? t(($) => $.tab_body.inbound.loading)
                : t(($) => $.tab_body.inbound.load_more)}
            </Button>
          )}
        </div>
      </aside>
      <section
        className={cn(
          "min-h-0 min-w-0 flex-1 flex-col",
          isCompact && !selected ? "hidden" : "flex",
        )}
      >
        {selected ? (
          <>
            <div className="flex shrink-0 items-center gap-2 border-b p-3">
              {isCompact && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setSelected(null)}
                >
                  <ArrowLeft className="size-4" />
                  {t(($) => $.tab_body.inbound.back)}
                </Button>
              )}
              <h2 className="min-w-0 flex-1 truncate text-body font-semibold">
                {title(selected)}
              </h2>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  void query.refetch();
                  void queryClient.invalidateQueries({
                    queryKey: agentCoordinatorConversationsKeys.messages(
                      wsId,
                      agent.id,
                      selected.session_id,
                    ),
                  });
                }}
              >
                {t(($) => $.tab_body.inbound.refresh)}
              </Button>
            </div>
            <CoordinatorConversationMessages
              key={selected.id}
              agentId={agent.id}
              sessionId={selected.session_id}
            />
          </>
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-muted-foreground">
            <MessageSquare className="size-10" />
            <p className="text-body">
              {t(($) => $.tab_body.inbound.select_prompt)}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
