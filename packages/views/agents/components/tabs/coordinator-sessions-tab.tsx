"use client";

import { useEffect, useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { ArrowLeft, MessageSquare } from "lucide-react";
import { useDefaultLayout } from "react-resizable-panels";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import {
  agentCoordinatorSessionsOptions,
  agentSceneMemoryOptions,
  useAgentPresenceDetail,
} from "@multica/core/agents";
import {
  chatMessagesPageOptions,
  pendingChatTaskOptions,
} from "@multica/core/chat/queries";
import { hideQueuedChatMessages } from "@multica/core/chat/pending";
import { useWorkspaceId } from "@multica/core/hooks";
import type { Agent, AgentSceneMemory, ChatSession } from "@multica/core/types";
import { PageHeader } from "../../../layout/page-header";
import {
  ChatMessageList,
  ChatMessageSkeleton,
} from "../../../chat/components/chat-message-list";
import { ChatSessionHeader } from "../../../chat/components/chat-session-header";
import { ChatThreadList } from "../../../chat/components/chat-thread-list";
import { useT } from "../../../i18n";

const CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX = 1_000_000;

function CoordinatorConversation({
  session,
  agent,
}: {
  session: ChatSession;
  agent: Agent;
}) {
  const {
    data: rawMessagePages,
    isLoading: messagesLoading,
    fetchNextPage: fetchOlderMessages,
    hasNextPage: hasOlderMessages,
    isFetchingNextPage: isFetchingOlderMessages,
  } = useInfiniteQuery(chatMessagesPageOptions(session.id));
  const { data: pendingTask, isLoading: pendingTaskLoading } = useQuery(
    pendingChatTaskOptions(session.id),
  );
  const wsId = useWorkspaceId();
  const presenceDetail = useAgentPresenceDetail(wsId, agent.id);
  const availability =
    presenceDetail === "loading" ? undefined : presenceDetail.availability;

  const messagePages = rawMessagePages?.pages ?? [];
  const allMessages = [...messagePages]
    .reverse()
    .flatMap((page) => page.messages);
  const messages = hideQueuedChatMessages(allMessages, pendingTask);
  const olderMessageCount = messagePages
    .slice(1)
    .reduce((sum, page) => sum + page.messages.length, 0);
  const firstItemIndex =
    messages.length > 0
      ? CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX - olderMessageCount
      : 0;
  const showSkeleton = messagesLoading || pendingTaskLoading;

  return (
    <div className="flex min-h-0 flex-1 flex-col @container">
      <ChatSessionHeader session={session} agent={agent} readOnly />
      {showSkeleton ? (
        <ChatMessageSkeleton />
      ) : (
        <ChatMessageList
          key={session.id}
          messages={messages}
          pendingTask={pendingTask}
          availability={availability}
          firstItemIndex={firstItemIndex}
          hasOlderMessages={hasOlderMessages}
          isFetchingOlderMessages={isFetchingOlderMessages}
          onLoadOlderMessages={() => void fetchOlderMessages()}
          quickActionsDisabled
        />
      )}
    </div>
  );
}

export function CoordinatorSessionsTab({
  agent,
  canEdit = false,
}: {
  agent: Agent;
  canEdit?: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const isCompact = useIsCompact();
  const showMemory = canEdit && agent.scene_memory_ui_enabled === true;
  const { data: sessions = [], isLoading, isError, refetch } = useQuery(
    agentCoordinatorSessionsOptions(wsId, agent.id),
  );
  const {
    data: memories = [],
    isLoading: memoryLoading,
    isError: memoryError,
    refetch: refetchMemory,
  } = useQuery(agentSceneMemoryOptions(wsId, agent.id, showMemory));
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [selectedMemoryId, setSelectedMemoryId] = useState<string | null>(null);
  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_agent_inbound_layout",
  });

  useEffect(() => {
    if (selectedId && !sessions.some((session) => session.id === selectedId)) {
      setSelectedId(null);
    }
  }, [selectedId, sessions]);

  useEffect(() => {
    if (
      selectedMemoryId &&
      !memories.some((memory) => memory.id === selectedMemoryId)
    ) {
      setSelectedMemoryId(null);
    }
  }, [selectedMemoryId, memories]);

  const selected =
    sessions.find((session) => session.id === selectedId) ?? null;
  const selectedMemory =
    memories.find((memory) => memory.id === selectedMemoryId) ?? null;

  const listHeader = (
    <PageHeader className="justify-between">
      <h1 className="text-body font-semibold">{t(($) => $.tabs.inbound)}</h1>
    </PageHeader>
  );

  const listBody = isLoading ? (
    <p className="px-4 py-3 text-caption text-muted-foreground">
      {t(($) => $.tab_body.inbound.loading)}
    </p>
  ) : isError ? (
    <div className="space-y-2 px-4 py-3">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.tab_body.inbound.load_failed)}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={() => void refetch()}>
        {t(($) => $.tab_body.inbound.retry)}
      </Button>
    </div>
  ) : sessions.length === 0 ? (
    <p className="px-4 py-3 text-caption text-muted-foreground">
      {t(($) => $.tab_body.inbound.empty)}
    </p>
  ) : (
    <div className="px-2 py-1">
      <ChatThreadList
        sessions={sessions}
        agents={[agent]}
        activeSessionId={selectedId}
        onSelectSession={(session) => {
          setSelectedMemoryId(null);
          setSelectedId(session.id);
        }}
        onArchive={() => undefined}
        readOnly
      />
    </div>
  );

  const memoryList = showMemory ? (
    <SceneMemoryList
      memories={memories}
      selectedId={selectedMemoryId}
      isLoading={memoryLoading}
      isError={memoryError}
      onRetry={() => void refetchMemory()}
      onSelect={(memory) => {
        setSelectedId(null);
        setSelectedMemoryId(memory.id);
      }}
    />
  ) : null;

  const conversation = selectedMemory ? (
    <SceneMemoryDetail memory={selectedMemory} />
  ) : selected ? (
    <CoordinatorConversation session={selected} agent={agent} />
  ) : (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
      <MessageSquare className="h-10 w-10 text-faint-foreground" />
      <p className="text-body">
        {showMemory
          ? t(($) => $.tab_body.inbound.memory_select_prompt)
          : t(($) => $.tab_body.inbound.select_prompt)}
      </p>
    </div>
  );

  if (isCompact) {
    if (selected || selectedMemory) {
      return (
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="flex h-12 shrink-0 items-center border-b px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setSelectedId(null);
                setSelectedMemoryId(null);
              }}
              className="gap-1.5 text-muted-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {t(($) => $.tabs.inbound)}
            </Button>
          </div>
          {conversation}
        </div>
      );
    }
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {listHeader}
        <div className="min-h-0 flex-1 overflow-y-auto">
          {listBody}
          {memoryList}
        </div>
      </div>
    );
  }

  return (
    <ResizablePanelGroup
      orientation="horizontal"
      className="min-h-0 flex-1"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id="list"
        defaultSize={320}
        minSize={240}
        maxSize={480}
        groupResizeBehavior="preserve-pixel-size"
      >
        <div className="flex h-full flex-col border-r">
          {listHeader}
          <div className="min-h-0 flex-1 overflow-y-auto">
            {listBody}
            {memoryList}
          </div>
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        <div className="flex h-full min-h-0 flex-col">{conversation}</div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}

function SceneMemoryList({
  memories,
  selectedId,
  isLoading,
  isError,
  onRetry,
  onSelect,
}: {
  memories: AgentSceneMemory[];
  selectedId: string | null;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  onSelect: (memory: AgentSceneMemory) => void;
}) {
  const { t } = useT("agents");
  return (
    <div className="border-t">
      <h2 className="px-4 py-2 text-caption font-medium text-muted-foreground">
        {t(($) => $.tab_body.inbound.memory_title)}
      </h2>
      {isLoading ? (
        <p className="px-4 py-2 text-caption text-muted-foreground">
          {t(($) => $.tab_body.inbound.memory_loading)}
        </p>
      ) : isError ? (
        <div className="space-y-2 px-4 py-2">
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.inbound.memory_load_failed)}
          </p>
          <Button type="button" variant="outline" size="sm" onClick={onRetry}>
            {t(($) => $.tab_body.inbound.retry)}
          </Button>
        </div>
      ) : memories.length === 0 ? (
        <p className="px-4 py-2 text-caption text-muted-foreground">
          {t(($) => $.tab_body.inbound.memory_empty)}
        </p>
      ) : (
        <ul className="px-2 pb-2">
          {memories.map((memory) => {
            const active = memory.id === selectedId;
            const title =
              memory.scene_title.trim() ||
              t(($) => $.tab_body.inbound.memory_untitled);
            const kind =
              memory.scene_kind === "group"
                ? t(($) => $.tab_body.inbound.memory_kind_group)
                : t(($) => $.tab_body.inbound.memory_kind_dm);
            return (
              <li key={memory.id}>
                <button
                  type="button"
                  data-active={active ? "true" : undefined}
                  className="flex w-full flex-col items-start gap-0.5 rounded-md px-2 py-1.5 text-left hover:bg-muted data-active:font-medium data-active:text-foreground data-active:hover:bg-muted"
                  onClick={() => onSelect(memory)}
                >
                  <span className="text-body">{title}</span>
                  <span className="text-caption text-muted-foreground">
                    {kind}
                    {" · "}
                    {t(($) => $.tab_body.inbound.memory_revision, {
                      revision: memory.memory_revision,
                    })}
                    {memory.status ? ` · ${memory.status}` : ""}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function SceneMemoryDetail({ memory }: { memory: AgentSceneMemory }) {
  const { t } = useT("agents");
  const title =
    memory.scene_title.trim() || t(($) => $.tab_body.inbound.memory_untitled);
  const kind =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_kind_group)
      : t(($) => $.tab_body.inbound.memory_kind_dm);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="shrink-0 border-b px-4 py-3">
        <h1 className="text-body font-semibold">{title}</h1>
        <p className="text-caption text-muted-foreground">
          {kind}
          {" · "}
          {t(($) => $.tab_body.inbound.memory_revision, {
            revision: memory.memory_revision,
          })}
          {memory.status ? ` · ${memory.status}` : ""}
        </p>
      </div>
      <pre className="min-h-0 flex-1 overflow-y-auto whitespace-pre-wrap px-4 py-3 text-body">
        {memory.memory_text.trim() || t(($) => $.tab_body.inbound.memory_empty)}
      </pre>
    </div>
  );
}
