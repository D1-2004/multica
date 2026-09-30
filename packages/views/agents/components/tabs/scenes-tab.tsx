"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Brain,
  FileText,
  Loader2,
  MessageSquare,
  RefreshCw,
  Users,
} from "lucide-react";
import type { Agent } from "@multica/core/types";
import {
  agentCoordinatorConversationsKeys,
  agentSceneMemoryDetailOptions,
} from "@multica/core/agents";
import {
  agentSceneOptions,
  agentScenesOptions,
  contextCapabilityKeys,
  type AgentSceneSummary,
  type ContextSceneKind,
} from "@multica/core/context-capabilities";
import { useWorkspaceId } from "@multica/core/hooks";
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
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../../navigation";
import { useT, useTimeAgo } from "../../../i18n";
import { CoordinatorConversationMessages, CoordinatorSessionsTab } from "./coordinator-sessions-tab";
import { SceneConfigPanel } from "./scene-config-panel";
import { MemoryFlagBar, SceneMemoryDetail, SceneMemoryTab } from "./scene-memory-tab";

export type SceneSubTab = "inbound" | "memory" | "config";

const SUB_TABS: readonly SceneSubTab[] = ["inbound", "memory", "config"];

function subTabOf(value: string | null): SceneSubTab {
  return value === "memory" || value === "config" ? value : "inbound";
}

function useSceneTitle() {
  const { t } = useT("agents");
  return (title: string, kind: ContextSceneKind) =>
    title ||
    (kind === "dm"
      ? t(($) => $.tab_body.scenes.untitled_dm)
      : t(($) => $.tab_body.scenes.untitled_group));
}

function useSceneKindLabel() {
  const { t } = useT("agents");
  return (kind: ContextSceneKind) =>
    kind === "dm"
      ? t(($) => $.tab_body.scenes.kind_dm)
      : t(($) => $.tab_body.scenes.kind_group);
}

/** Full lists of records that the scene list may not cover. */
type SceneArchive = "inbound" | "memory";

/**
 * 场域: the agent's IM scenes (DingTalk group chats and 1:1 chats). Each
 * scene opens on three sub-tabs — 入站记录 (the latest inbound conversation
 * transcript), 记忆 (the scene memory) and 配置 (scene prompt and scene
 * connectors / skills). The selected scene and sub-tab live in the URL
 * (`scene`, `scene_tab`) so a scene can be linked directly.
 *
 * 其他记录 opens the full inbound conversation and scene memory lists: the
 * scene list keeps only conversations of the agent's current DingTalk org
 * with an openConversationId, so older or unkeyed records stay reachable
 * there.
 *
 * Switching scene, sub-tab or view unmounts the scene prompt editor, so it
 * goes through the same discard confirmation as the pane's own tabs while
 * the prompt has unsaved edits.
 */
export function ScenesTab({
  agent,
  canEdit,
  onUpdate,
  onDirtyChange,
}: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const isCompact = useIsCompact();
  const query = useInfiniteQuery(agentScenesOptions(wsId, agent.id));
  const scenes = useMemo(
    () => [
      ...new Map(
        (query.data?.pages ?? [])
          .flatMap((page) => page.scenes)
          .map((scene) => [scene.sceneKey, scene]),
      ).values(),
    ],
    [query.data],
  );
  const urlScene = navigation.searchParams.get("scene") ?? "";
  const [selectedKey, setSelectedKey] = useState(urlScene);
  const [subTab, setSubTab] = useState<SceneSubTab>(() =>
    subTabOf(navigation.searchParams.get("scene_tab")),
  );

  // Desktop opens the most recent scene when nothing is selected yet.
  const firstKey = scenes[0]?.sceneKey ?? "";
  const activeKey = selectedKey || (isCompact ? "" : firstKey);
  // Pin that default, so a list refetch that brings another scene to the top
  // never swaps the open detail (and an unsaved prompt draft) away.
  useEffect(() => {
    if (!selectedKey && !isCompact && firstKey) setSelectedKey(firstKey);
  }, [firstKey, isCompact, selectedKey]);

  const writeUrl = (sceneKey: string, tab: SceneSubTab) => {
    const params = new URLSearchParams(navigation.searchParams);
    if (sceneKey) params.set("scene", sceneKey);
    else params.delete("scene");
    if (sceneKey && tab !== "inbound") params.set("scene_tab", tab);
    else params.delete("scene_tab");
    // An app dialog belongs to the scene it was opened in.
    params.delete("app");
    const search = params.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  };

  const [archive, setArchive] = useState<SceneArchive | null>(null);
  const [promptDirty, setPromptDirty] = useState(false);
  // The navigation waiting for the discard confirmation.
  const [pendingAction, setPendingAction] = useState<(() => void) | null>(null);

  const reportDirty = useCallback(
    (dirty: boolean) => {
      setPromptDirty(dirty);
      onDirtyChange?.(dirty);
    },
    [onDirtyChange],
  );

  // Runs a navigation that unmounts the prompt editor, after confirmation
  // when the prompt has unsaved edits.
  const guarded = (action: () => void) => {
    if (promptDirty) {
      setPendingAction(() => action);
      return;
    }
    action();
  };

  const selectScene = (sceneKey: string) => {
    const commit = () => {
      setSelectedKey(sceneKey);
      writeUrl(sceneKey, subTab);
    };
    // Re-selecting the open scene keeps its detail (and editor) mounted.
    if (sceneKey === activeKey) commit();
    else guarded(commit);
  };

  const selectSubTab = (tab: SceneSubTab) => {
    if (tab === subTab) return;
    guarded(() => {
      setSubTab(tab);
      if (activeKey) writeUrl(activeKey, tab);
    });
  };

  const openArchive = (next: SceneArchive | null) => {
    if (next === archive) return;
    guarded(() => setArchive(next));
  };

  const confirmPending = () => {
    const action = pendingAction;
    setPendingAction(null);
    action?.();
  };

  const listSummary = scenes.find((scene) => scene.sceneKey === activeKey) ?? null;

  const discardDialog =
    pendingAction !== null ? (
      <AlertDialog
        open
        onOpenChange={(open) => {
          if (!open) setPendingAction(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.tabs.discard_dialog_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.tabs.discard_dialog_description)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.tabs.discard_keep)}</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={confirmPending}>
              {t(($) => $.tabs.discard_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    ) : null;

  if (archive) {
    return (
      <div className="flex h-full min-h-0 flex-1 flex-col">
        <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2.5 sm:px-4">
          <Button variant="ghost" size="sm" onClick={() => openArchive(null)}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t(($) => $.tab_body.scenes.back)}
          </Button>
          <h2 className="min-w-0 truncate text-body font-semibold">
            {archive === "inbound"
              ? t(($) => $.tab_body.scenes.all_inbound)
              : t(($) => $.tab_body.scenes.all_memory)}
          </h2>
        </div>
        {archive === "inbound" ? (
          <CoordinatorSessionsTab key={agent.id} agent={agent} />
        ) : (
          <SceneMemoryTab agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
        )}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <MemoryFlagBar agent={agent} canEdit={canEdit} onUpdate={onUpdate} />
      <div className="flex min-h-0 flex-1">
        <aside
          className={cn(
            "min-h-0 flex-col border-r",
            isCompact ? (activeKey ? "hidden" : "flex w-full") : "flex w-64 shrink-0",
          )}
        >
          <div className="shrink-0 px-3 pt-3 pb-2">
            <h2 className="text-body font-semibold">{t(($) => $.tabs.scenes)}</h2>
            <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.intro)}</p>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
            {query.isLoading ? (
              <p className="p-2 text-caption text-muted-foreground">
                {t(($) => $.tab_body.scenes.loading)}
              </p>
            ) : null}
            {query.isError ? (
              <div role="alert" className="space-y-2 p-2 text-caption">
                <p>{t(($) => $.tab_body.scenes.load_failed)}</p>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    void (query.isFetchNextPageError ? query.fetchNextPage() : query.refetch())
                  }
                >
                  {t(($) => $.tab_body.scenes.retry)}
                </Button>
              </div>
            ) : null}
            {!query.isLoading && !query.isError && scenes.length === 0 ? (
              <div className="flex flex-col items-center gap-2 px-4 py-10 text-center text-muted-foreground">
                <Users className="size-8 text-faint-foreground" aria-hidden="true" />
                <p className="text-body font-medium text-foreground">
                  {t(($) => $.tab_body.scenes.empty_title)}
                </p>
                <p className="text-caption text-pretty">{t(($) => $.tab_body.scenes.empty_hint)}</p>
              </div>
            ) : null}
            <ul>
              {scenes.map((scene) => (
                <SceneRow
                  key={scene.sceneKey}
                  scene={scene}
                  selected={scene.sceneKey === activeKey}
                  onSelect={() => selectScene(scene.sceneKey)}
                />
              ))}
            </ul>
            {query.hasNextPage ? (
              <Button
                className="mt-2 w-full"
                variant="outline"
                disabled={query.isFetchingNextPage}
                onClick={() => void query.fetchNextPage()}
              >
                {query.isFetchingNextPage
                  ? t(($) => $.tab_body.scenes.loading)
                  : t(($) => $.tab_body.scenes.load_more)}
              </Button>
            ) : null}
            <section
              className="mt-6 space-y-1 px-2"
              aria-labelledby="scenes-other-records"
            >
              <h3
                id="scenes-other-records"
                title={t(($) => $.tab_body.scenes.other_records_hint)}
                className="text-caption font-medium text-muted-foreground"
              >
                {t(($) => $.tab_body.scenes.other_records_title)}
              </h3>
              <Button
                variant="ghost"
                size="sm"
                className="w-full justify-start"
                onClick={() => openArchive("inbound")}
              >
                <MessageSquare className="size-3.5" aria-hidden="true" />
                {t(($) => $.tab_body.scenes.all_inbound)}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="w-full justify-start"
                onClick={() => openArchive("memory")}
              >
                <Brain className="size-3.5" aria-hidden="true" />
                {t(($) => $.tab_body.scenes.all_memory)}
              </Button>
            </section>
          </div>
        </aside>
        <section
          className={cn(
            "min-h-0 min-w-0 flex-1 flex-col",
            isCompact && !activeKey ? "hidden" : "flex",
          )}
        >
          {activeKey ? (
            <SceneDetail
              key={activeKey}
              agent={agent}
              sceneKey={activeKey}
              summary={listSummary}
              subTab={subTab}
              onSubTab={selectSubTab}
              canEdit={canEdit}
              onBack={isCompact ? () => selectScene("") : undefined}
              onDirtyChange={reportDirty}
            />
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-muted-foreground">
              <MessageSquare className="size-10" aria-hidden="true" />
              <p className="text-body">{t(($) => $.tab_body.scenes.select_prompt)}</p>
            </div>
          )}
        </section>
      </div>
      {discardDialog}
    </div>
  );
}

function SceneRow({
  scene,
  selected,
  onSelect,
}: {
  scene: AgentSceneSummary;
  selected: boolean;
  onSelect: () => void;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const sceneTitle = useSceneTitle();
  const kindLabel = useSceneKindLabel();
  const title = sceneTitle(scene.title, scene.kind);
  const kind = kindLabel(scene.kind);
  return (
    <li>
      <button
        type="button"
        data-active={selected ? "true" : undefined}
        aria-current={selected ? "true" : undefined}
        aria-label={`${kind} ${title}`}
        onClick={onSelect}
        className="flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring data-active:bg-accent data-active:font-medium data-active:text-accent-foreground data-active:hover:bg-accent"
      >
        {scene.kind === "dm" ? (
          <MessageSquare className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <Users className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <span className="min-w-0 flex-1 truncate text-body">{title}</span>
        {scene.hasPrompt ? (
          <FileText
            className="size-3.5 shrink-0 text-muted-foreground"
            role="img"
            aria-label={t(($) => $.tab_body.scenes.has_prompt)}
          />
        ) : null}
        {scene.memoryId ? (
          <Brain
            className="size-3.5 shrink-0 text-muted-foreground"
            role="img"
            aria-label={t(($) => $.tab_body.scenes.has_memory)}
          />
        ) : null}
        {scene.lastActiveAt ? (
          <span className="shrink-0 text-caption font-normal text-muted-foreground">
            {timeAgo(scene.lastActiveAt)}
          </span>
        ) : null}
      </button>
    </li>
  );
}

function SceneDetail({
  agent,
  sceneKey,
  summary,
  subTab,
  onSubTab,
  canEdit,
  onBack,
  onDirtyChange,
}: {
  agent: Agent;
  sceneKey: string;
  summary: AgentSceneSummary | null;
  subTab: SceneSubTab;
  onSubTab: (tab: SceneSubTab) => void;
  canEdit: boolean;
  onBack?: () => void;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const sceneTitle = useSceneTitle();
  const kindLabel = useSceneKindLabel();
  const detailQuery = useQuery(agentSceneOptions(wsId, agent.id, sceneKey));
  const detail = detailQuery.data ?? null;
  const scene = detail?.scene ?? summary;
  const labels: Record<SceneSubTab, string> = {
    inbound: t(($) => $.tab_body.scenes.tab_inbound),
    memory: t(($) => $.tab_body.scenes.tab_memory),
    config: t(($) => $.tab_body.scenes.tab_config),
  };

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.scenes(wsId, agent.id) });
    if (scene?.inboundSessionId) {
      void queryClient.invalidateQueries({
        queryKey: agentCoordinatorConversationsKeys.messages(wsId, agent.id, scene.inboundSessionId),
      });
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2.5 sm:px-4">
        {onBack ? (
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t(($) => $.tab_body.scenes.back)}
          </Button>
        ) : null}
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-body font-semibold">
            {scene ? sceneTitle(scene.title, scene.kind) : sceneKey}
          </h2>
        </div>
        {scene ? (
          <Badge variant="outline" className="shrink-0">
            {kindLabel(scene.kind)}
          </Badge>
        ) : null}
        <Button variant="ghost" size="sm" onClick={refresh}>
          <RefreshCw className="size-3.5" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.refresh)}
        </Button>
      </div>
      <div
        className="flex shrink-0 items-center gap-5 overflow-x-auto border-b px-4"
        role="tablist"
        aria-label={scene ? sceneTitle(scene.title, scene.kind) : sceneKey}
      >
        {SUB_TABS.map((tab) => (
          <button
            key={tab}
            type="button"
            role="tab"
            aria-selected={subTab === tab}
            onClick={() => onSubTab(tab)}
            className={cn(
              "relative shrink-0 py-2.5 text-body transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
              subTab === tab
                ? "font-medium text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {labels[tab]}
          </button>
        ))}
      </div>
      {detailQuery.isLoading && !scene ? (
        <PanelNotice>
          <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.detail_loading)}
        </PanelNotice>
      ) : !scene ? (
        <PanelNotice>
          <span>{t(($) => $.tab_body.scenes.detail_load_failed)}</span>
          <Button size="sm" variant="outline" onClick={() => void detailQuery.refetch()}>
            {t(($) => $.tab_body.scenes.retry)}
          </Button>
        </PanelNotice>
      ) : subTab === "inbound" ? (
        scene.inboundSessionId ? (
          <CoordinatorConversationMessages
            key={scene.inboundSessionId}
            agentId={agent.id}
            sessionId={scene.inboundSessionId}
          />
        ) : (
          <PanelNotice>{t(($) => $.tab_body.scenes.inbound_empty)}</PanelNotice>
        )
      ) : subTab === "memory" ? (
        <SceneMemoryPanel agent={agent} scene={scene} canEdit={canEdit} />
      ) : detail ? (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <SceneConfigPanel
            agent={agent}
            detail={detail}
            canEdit={canEdit}
            onDirtyChange={onDirtyChange}
          />
        </div>
      ) : detailQuery.isLoading ? (
        <PanelNotice>
          <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.detail_loading)}
        </PanelNotice>
      ) : (
        <PanelNotice>
          <span>{t(($) => $.tab_body.scenes.detail_load_failed)}</span>
          <Button size="sm" variant="outline" onClick={() => void detailQuery.refetch()}>
            {t(($) => $.tab_body.scenes.retry)}
          </Button>
        </PanelNotice>
      )}
    </div>
  );
}

function SceneMemoryPanel({
  agent,
  scene,
  canEdit,
}: {
  agent: Agent;
  scene: AgentSceneSummary;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const uiEnabled = agent.scene_memory_ui_enabled === true;
  // Loaded by id: the agent-wide list holds only the 200 newest rows.
  const query = useQuery(agentSceneMemoryDetailOptions(wsId, agent.id, scene.memoryId, uiEnabled));
  if (!uiEnabled) {
    return <PanelNotice>{t(($) => $.tab_body.scenes.memory_ui_off)}</PanelNotice>;
  }
  if (!scene.memoryId) {
    return <PanelNotice>{t(($) => $.tab_body.scenes.memory_empty)}</PanelNotice>;
  }
  if (query.isLoading) {
    return (
      <PanelNotice>
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.inbound.memory_loading)}
      </PanelNotice>
    );
  }
  if (query.isError) {
    return (
      <PanelNotice>
        <span>{t(($) => $.tab_body.inbound.memory_load_failed)}</span>
        <Button size="sm" variant="outline" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.scenes.retry)}
        </Button>
      </PanelNotice>
    );
  }
  // A malformed response parses to a row without an id.
  const memory = query.data?.id ? query.data : null;
  if (!memory) {
    return <PanelNotice>{t(($) => $.tab_body.scenes.memory_empty)}</PanelNotice>;
  }
  return <SceneMemoryDetail agent={agent} memory={memory} canEdit={canEdit} showTitle={false} />;
}

function PanelNotice({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center text-body text-muted-foreground">
      {children}
    </div>
  );
}
