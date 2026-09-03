"use client";

import { useEffect, useState } from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ArrowLeft, Link2, MessageSquare, Pencil, Users } from "lucide-react";
import { toast } from "sonner";
import { useDefaultLayout } from "react-resizable-panels";
import { api } from "@multica/core/api";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  ResizableHandle,
  ResizablePanel,
  ResizablePanelGroup,
} from "@multica/ui/components/ui/resizable";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import {
  agentCoordinatorSessionsOptions,
  agentSceneMemoryKeys,
  agentSceneRelationKeys,
  agentSceneMemoryOptions,
  agentSceneRelationOptions,
  useAgentPresenceDetail,
} from "@multica/core/agents";
import {
  chatMessagesPageOptions,
  pendingChatTaskOptions,
} from "@multica/core/chat/queries";
import { hideQueuedChatMessages } from "@multica/core/chat/pending";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent, AgentSceneMemory, ChatSession } from "@multica/core/types";
import { PageHeader } from "../../../layout/page-header";
import { AppLink } from "../../../navigation";
import {
  ChatMessageList,
  ChatMessageSkeleton,
} from "../../../chat/components/chat-message-list";
import { ChatSessionHeader } from "../../../chat/components/chat-session-header";
import { ChatThreadList } from "../../../chat/components/chat-thread-list";
import { useT, useTimeAgo } from "../../../i18n";
import {
  displayMatterTitle,
  isEmptyMemoryBody,
  memoryStatusKey,
  parseMemorySections,
  partitionSceneMemories,
  sceneDisplayTitle,
  scenePreview,
} from "./scene-memory-view";

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

  useEffect(() => {
    const firstMemory = memories[0];
    if (!showMemory || selectedId || selectedMemoryId || !firstMemory) {
      return;
    }
    setSelectedMemoryId(firstMemory.id);
  }, [showMemory, selectedId, selectedMemoryId, memories]);

  const selected =
    sessions.find((session) => session.id === selectedId) ?? null;
  const selectedMemory =
    memories.find((memory) => memory.id === selectedMemoryId) ?? null;

  const listHeader = (
    <PageHeader>
      <h1 className="truncate text-body font-semibold text-pretty">
        {t(($) => $.tabs.inbound)}
      </h1>
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
    <div className="px-2 pb-3">
      {showMemory ? (
        <p className="px-2 pb-1.5 pt-3 text-caption font-medium text-muted-foreground">
          {t(($) => $.tab_body.inbound.conversations_title)}
        </p>
      ) : null}
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
    <SceneMemoryDetail
      agent={agent}
      memory={selectedMemory}
      canEdit={canEdit}
    />
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
          {memoryList}
          {listBody}
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
            {memoryList}
            {listBody}
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
  const { dms, groups } = partitionSceneMemories(memories);
  return (
    <div className="border-b">
      <p className="px-4 pb-1.5 pt-3 text-caption font-medium text-muted-foreground">
        {t(($) => $.tab_body.inbound.memory_title)}
      </p>
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
        <p className="px-4 pb-3 text-caption text-muted-foreground">
          {t(($) => $.tab_body.inbound.memory_empty)}
        </p>
      ) : (
        <div className="px-2 pb-3">
          {dms.length > 0 ? (
            <section className="pb-1">
              <p className="flex items-center gap-1.5 px-2 pb-1 pt-1 text-caption font-medium text-muted-foreground">
                <MessageSquare className="size-3.5" aria-hidden="true" />
                {t(($) => $.tab_body.inbound.memory_kind_dm)}
              </p>
              <ul>
                {dms.map((memory) => (
                  <SceneMemoryRow
                    key={memory.id}
                    memory={memory}
                    selected={memory.id === selectedId}
                    onSelect={onSelect}
                  />
                ))}
              </ul>
            </section>
          ) : null}
          {groups.length > 0 ? (
            <section className="pb-1">
              <p className="flex items-center gap-1.5 px-2 pb-1 pt-1 text-caption font-medium text-muted-foreground">
                <Users className="size-3.5" aria-hidden="true" />
                {t(($) => $.tab_body.inbound.memory_kind_group)}
              </p>
              <ul>
                {groups.map((memory) => (
                  <SceneMemoryRow
                    key={memory.id}
                    memory={memory}
                    selected={memory.id === selectedId}
                    onSelect={onSelect}
                  />
                ))}
              </ul>
            </section>
          ) : null}
        </div>
      )}
    </div>
  );
}

function SceneMemoryRow({
  memory,
  selected,
  onSelect,
}: {
  memory: AgentSceneMemory;
  selected: boolean;
  onSelect: (memory: AgentSceneMemory) => void;
}) {
  const { t } = useT("agents");
  const untitled =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_untitled_group)
      : t(($) => $.tab_body.inbound.memory_untitled_dm);
  const title = sceneDisplayTitle(memory, untitled);
  const preview = scenePreview(memory);
  const status = memoryStatusKey(memory.status);
  const kind =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_kind_group)
      : t(($) => $.tab_body.inbound.memory_kind_dm);
  return (
    <li>
      <button
        type="button"
        data-active={selected ? "true" : undefined}
        aria-current={selected ? "true" : undefined}
        aria-label={`${kind} ${title}`}
        className="flex w-full min-w-0 items-start gap-2.5 rounded-lg px-2 py-2 text-left transition-colors hover:bg-muted data-active:bg-muted data-active:font-medium data-active:text-foreground data-active:hover:bg-muted"
        onClick={() => onSelect(memory)}
      >
        <span
          aria-hidden="true"
          className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground"
        >
          {memory.scene_kind === "group" ? (
            <Users className="size-3.5" />
          ) : (
            <MessageSquare className="size-3.5" />
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate text-body">{title}</span>
            {status !== "clean" ? (
              <Badge
                variant={
                  status === "blocked" || status === "retrying"
                    ? "destructive"
                    : "secondary"
                }
                className="shrink-0"
              >
                {t(($) => $.tab_body.inbound[`status_${status}`])}
              </Badge>
            ) : null}
          </span>
          {preview ? (
            <span className="mt-0.5 block truncate text-caption text-muted-foreground">
              {preview}
            </span>
          ) : null}
        </span>
      </button>
    </li>
  );
}

function SceneMemoryDetail({
  agent,
  memory,
  canEdit,
}: {
  agent: Agent;
  memory: AgentSceneMemory;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(memory.memory_text);
  const [confirm, setConfirm] = useState<"reset" | "clear" | null>(null);
  useEffect(() => {
    setDraft(memory.memory_text);
    setEditing(false);
  }, [memory.id, memory.memory_revision, memory.memory_text]);
  const { data: relations = [], isLoading: relationsLoading } = useQuery(
    agentSceneRelationOptions(wsId, agent.id, memory.scene_key, true),
  );
  const save = useMutation({
    mutationFn: () =>
      api.updateAgentSceneMemory(agent.id, memory.id, {
        memory_text: draft,
        expected_revision: memory.memory_revision,
      }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentSceneMemoryKeys.list(wsId, agent.id),
      });
      setEditing(false);
      toast.success(t(($) => $.tab_body.inbound.memory_saved));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.memory_save_failed));
    },
  });
  const reset = useMutation({
    mutationFn: () => api.resetAgentSceneMemory(agent.id, memory.id),
    onSuccess: async () => {
      setConfirm(null);
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: agentSceneMemoryKeys.list(wsId, agent.id),
        }),
        queryClient.invalidateQueries({
          queryKey: agentSceneRelationKeys.list(wsId, agent.id, memory.scene_key),
        }),
      ]);
      toast.success(t(($) => $.tab_body.inbound.memory_reset_done));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.memory_reset_failed));
    },
  });
  const clearRelations = useMutation({
    mutationFn: () => api.clearAgentSceneRelations(agent.id, memory.id),
    onSuccess: async () => {
      setConfirm(null);
      await queryClient.invalidateQueries({
        queryKey: agentSceneRelationKeys.list(wsId, agent.id, memory.scene_key),
      });
      toast.success(t(($) => $.tab_body.inbound.relations_cleared));
    },
    onError: () => {
      toast.error(t(($) => $.tab_body.inbound.relations_clear_failed));
    },
  });
  const untitled =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_untitled_group)
      : t(($) => $.tab_body.inbound.memory_untitled_dm);
  const title = sceneDisplayTitle(memory, untitled);
  const kind =
    memory.scene_kind === "group"
      ? t(($) => $.tab_body.inbound.memory_kind_group)
      : t(($) => $.tab_body.inbound.memory_kind_dm);
  const status = memoryStatusKey(memory.status);
  const statusLabel =
    status === "pending"
      ? t(($) => $.tab_body.inbound.status_pending)
      : status === "running"
        ? t(($) => $.tab_body.inbound.status_running)
        : status === "retrying"
          ? t(($) => $.tab_body.inbound.status_retrying)
          : status === "blocked"
            ? t(($) => $.tab_body.inbound.status_blocked)
            : t(($) => $.tab_body.inbound.status_clean);
  const dirty = draft !== memory.memory_text;
  const sections = parseMemorySections(memory.memory_text);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-start justify-between gap-3 border-b px-5 py-4">
        <div className="min-w-0">
          <h1 className="text-title font-semibold text-pretty">{title}</h1>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
            <Badge variant="outline">{kind}</Badge>
            <span>{statusLabel}</span>
            {memory.updated_at ? <span>{timeAgo(memory.updated_at)}</span> : null}
          </p>
          {memory.last_error ? (
            <p className="mt-2 text-caption text-destructive">{memory.last_error}</p>
          ) : null}
        </div>
        {canEdit && !editing ? (
          <div className="flex shrink-0 gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="gap-1.5"
              onClick={() => setEditing(true)}
            >
              <Pencil className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.inbound.memory_edit)}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={reset.isPending}
              onClick={() => setConfirm("reset")}
            >
              {t(($) => $.tab_body.inbound.memory_reset)}
            </Button>
          </div>
        ) : null}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <section className="px-5 py-5">
          {editing ? (
            <div className="space-y-3">
              <label className="text-caption font-medium text-muted-foreground" htmlFor="scene-memory-draft">
                {t(($) => $.tab_body.inbound.memory_editor_label)}
              </label>
              <Textarea
                id="scene-memory-draft"
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                rows={14}
                spellCheck={false}
                className="min-h-56 text-body leading-7"
              />
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  disabled={!dirty || save.isPending}
                  onClick={() => save.mutate()}
                >
                  {save.isPending
                    ? t(($) => $.tab_body.inbound.memory_saving)
                    : t(($) => $.tab_body.inbound.memory_save)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={save.isPending}
                  onClick={() => {
                    setDraft(memory.memory_text);
                    setEditing(false);
                  }}
                >
                  {t(($) => $.tab_body.inbound.memory_cancel)}
                </Button>
              </div>
            </div>
          ) : sections.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.tab_body.inbound.memory_empty_section)}
            </p>
          ) : (
            <div className="space-y-6">
              {sections.map((section) => (
                <article key={section.heading || "body"} className="space-y-2">
                  {section.heading ? (
                    <h2 className="text-caption font-medium tracking-wide text-muted-foreground">
                      {section.heading}
                    </h2>
                  ) : null}
                  {isEmptyMemoryBody(section.body) || !section.body ? (
                    <p className="text-body text-muted-foreground">
                      {t(($) => $.tab_body.inbound.memory_empty_section)}
                    </p>
                  ) : (
                    <p className="whitespace-pre-wrap text-body leading-7 text-pretty">
                      {section.body}
                    </p>
                  )}
                </article>
              ))}
            </div>
          )}
        </section>
        <section className="border-t px-5 py-5">
          <div className="mb-3 flex items-center justify-between gap-3">
            <h2 className="flex items-center gap-1.5 text-caption font-medium text-muted-foreground">
              <Link2 className="size-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.inbound.relations_title)}
            </h2>
            {canEdit && relations.length > 0 ? (
              <Button
                type="button"
                size="sm"
                variant="ghost"
                disabled={clearRelations.isPending}
                onClick={() => setConfirm("clear")}
              >
                {t(($) => $.tab_body.inbound.relations_clear)}
              </Button>
            ) : null}
          </div>
          {relationsLoading ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.inbound.relations_loading)}
            </p>
          ) : relations.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.tab_body.inbound.relations_empty)}
            </p>
          ) : (
            <ul className="space-y-2">
              {relations.map((item) => {
                const issueId = item.issue_id || item.issue;
                return (
                  <li
                    key={issueId || item.purpose}
                    className="rounded-lg border bg-muted/30 px-3 py-2.5"
                  >
                    {issueId ? (
                      <AppLink
                        href={paths.issueDetail(issueId)}
                        className="text-body font-medium text-brand hover:underline"
                      >
                        {displayMatterTitle(item.purpose, issueId)}
                      </AppLink>
                    ) : (
                      <p className="text-body font-medium">
                        {displayMatterTitle(
                          item.purpose,
                          t(($) => $.tab_body.inbound.relations_untitled),
                        )}
                      </p>
                    )}
                    <p className="mt-1 text-caption text-muted-foreground">
                      {item.status}
                      {item.on_this_scene
                        ? ` · ${t(($) => $.tab_body.inbound.relations_on_scene)}`
                        : ""}
                    </p>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      </div>
      <AlertDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear)
                : t(($) => $.tab_body.inbound.memory_reset)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear_confirm)
                : t(($) => $.tab_body.inbound.memory_reset_confirm)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.tab_body.inbound.memory_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                if (confirm === "clear") {
                  clearRelations.mutate();
                } else {
                  reset.mutate();
                }
              }}
            >
              {confirm === "clear"
                ? t(($) => $.tab_body.inbound.relations_clear)
                : t(($) => $.tab_body.inbound.memory_reset)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
