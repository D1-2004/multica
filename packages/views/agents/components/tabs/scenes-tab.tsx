"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Brain,
  FileText,
  Loader2,
  MessageSquare,
  Plug,
  RefreshCw,
  Users,
} from "lucide-react";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  agentCoordinatorConversationsKeys,
  agentSceneMemoryDetailOptions,
} from "@multica/core/agents";
import {
  agentContextCapabilitiesOptions,
  agentSceneOptions,
  agentScenesOptions,
  contextCapabilityKeys,
  useSetAgentSceneBinding,
  useSetAgentScenePrompt,
  type AgentSceneBinding,
  type AgentSceneDetail,
  type AgentSceneSummary,
  type ContextResourceType,
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
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { ConnectorLogo } from "../../../common/connector-logo";
import { useNavigation } from "../../../navigation";
import { SkillIcon } from "../../../skills/lib/skill-icon";
import { useT, useTimeAgo } from "../../../i18n";
import { CoordinatorConversationMessages, CoordinatorSessionsTab } from "./coordinator-sessions-tab";
import { ConfigureLink } from "./context-offers-section";
import { MemoryFlagBar, SceneMemoryDetail, SceneMemoryTab } from "./scene-memory-tab";

/** Server limit of a scene prompt, in characters. */
export const SCENE_PROMPT_MAX_LENGTH = 8000;

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
            isCompact ? (activeKey ? "hidden" : "flex w-full") : "flex w-80 shrink-0",
          )}
        >
          <div className="shrink-0 space-y-1 p-4">
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
              <h3 id="scenes-other-records" className="text-caption font-medium text-muted-foreground">
                {t(($) => $.tab_body.scenes.other_records_title)}
              </h3>
              <p className="pb-1 text-caption text-pretty text-muted-foreground">
                {t(($) => $.tab_body.scenes.other_records_hint)}
              </p>
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
        className="mb-1 flex w-full min-w-0 items-start gap-2.5 rounded-md p-3 text-left transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring data-active:bg-accent data-active:font-semibold data-active:text-accent-foreground data-active:hover:bg-accent"
      >
        <span
          aria-hidden="true"
          className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground"
        >
          {scene.kind === "dm" ? <MessageSquare className="size-3.5" /> : <Users className="size-3.5" />}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-body">{title}</span>
          <span className="mt-1 flex min-w-0 items-center gap-1.5 text-caption font-normal text-muted-foreground">
            <Badge variant="outline" className="shrink-0 font-normal">
              {kind}
            </Badge>
            {scene.lastActiveAt ? (
              <span className="truncate">{timeAgo(scene.lastActiveAt)}</span>
            ) : null}
          </span>
          <span className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-caption font-normal text-muted-foreground">
            {scene.memoryId ? (
              <Brain className="size-3.5" role="img" aria-label={t(($) => $.tab_body.scenes.has_memory)} />
            ) : null}
            {scene.hasPrompt ? (
              <FileText className="size-3.5" role="img" aria-label={t(($) => $.tab_body.scenes.has_prompt)} />
            ) : null}
            {scene.connectorCount > 0 ? (
              <span>{t(($) => $.tab_body.scenes.connector_count, { count: scene.connectorCount })}</span>
            ) : null}
            {scene.skillCount > 0 ? (
              <span>{t(($) => $.tab_body.scenes.skill_count, { count: scene.skillCount })}</span>
            ) : null}
          </span>
        </span>
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

function SceneConfigPanel({
  agent,
  detail,
  canEdit,
  onDirtyChange,
}: {
  agent: Agent;
  detail: AgentSceneDetail;
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const capabilities = useQuery({
    ...agentContextCapabilitiesOptions(wsId, agent.id),
    enabled: canEdit && Boolean(wsId),
  });
  const configureUrl = capabilities.data?.configureUrl ?? "";

  return (
    <div className="mx-auto w-full max-w-3xl space-y-10 p-4 sm:p-6">
      <ScenePromptEditor
        key={detail.scene.sceneKey}
        agentId={agent.id}
        sceneKey={detail.scene.sceneKey}
        prompt={detail.prompt}
        canEdit={canEdit}
        onDirtyChange={onDirtyChange}
      />
      <SceneCapabilities agentId={agent.id} detail={detail} canEdit={canEdit} />
      {configureUrl ? (
        <div className="space-y-1.5">
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.configure_hint)}</p>
          <ConfigureLink url={configureUrl} />
        </div>
      ) : null}
    </div>
  );
}

function ScenePromptEditor({
  agentId,
  sceneKey,
  prompt,
  canEdit,
  onDirtyChange,
}: {
  agentId: string;
  sceneKey: string;
  prompt: AgentSceneDetail["prompt"];
  canEdit: boolean;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const timeAgo = useTimeAgo();
  const save = useSetAgentScenePrompt(wsId, agentId);
  const [draft, setDraft] = useState(prompt.text);
  // The stored prompt the draft started from. When a save (this one or
  // another admin's, via a refetch) changes the stored prompt, a draft
  // without edits follows it; a draft with unsaved edits is kept.
  const [baseline, setBaseline] = useState(prompt.text);
  if (prompt.text !== baseline) {
    setBaseline(prompt.text);
    if (draft === baseline) setDraft(prompt.text);
  }
  const dirty = draft !== prompt.text;
  const tooLong = draft.length > SCENE_PROMPT_MAX_LENGTH;
  const inputId = `scene-prompt-${sceneKey}`;

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);

  const submit = async () => {
    if (!dirty || tooLong) return;
    const submitted = draft;
    try {
      const saved = await save.mutateAsync({ sceneKey, prompt: submitted });
      // The server trims the prompt; adopt the stored text unless the admin
      // kept typing while the save was in flight.
      setDraft((current) => (current === submitted ? saved.text : current));
      toast.success(t(($) => $.tab_body.scenes.prompt_saved));
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.scenes.prompt_save_failed),
      );
    }
  };

  const updated = prompt.updatedAt
    ? prompt.updatedByName
      ? t(($) => $.tab_body.scenes.prompt_updated, {
          when: timeAgo(prompt.updatedAt),
          name: prompt.updatedByName,
        })
      : t(($) => $.tab_body.scenes.prompt_updated_no_name, { when: timeAgo(prompt.updatedAt) })
    : "";

  return (
    <section className="space-y-3" aria-labelledby={`${inputId}-title`}>
      <div>
        <h3 id={`${inputId}-title`} className="flex items-center gap-1.5 text-body font-medium">
          <FileText className="size-4 text-muted-foreground" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.prompt_title)}
        </h3>
        <p className="mt-1 text-caption leading-5 text-muted-foreground">
          {t(($) => $.tab_body.scenes.prompt_hint)}
        </p>
      </div>
      {canEdit ? (
        <>
          <label htmlFor={inputId} className="sr-only">
            {t(($) => $.tab_body.scenes.prompt_title)}
          </label>
          <Textarea
            id={inputId}
            value={draft}
            rows={6}
            placeholder={t(($) => $.tab_body.scenes.prompt_placeholder)}
            aria-invalid={tooLong || undefined}
            onChange={(event) => setDraft(event.target.value)}
            className="min-h-32 text-body leading-6"
          />
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className={cn("text-caption", tooLong ? "text-destructive" : "text-muted-foreground")}>
              {tooLong
                ? t(($) => $.tab_body.scenes.prompt_too_long, { max: SCENE_PROMPT_MAX_LENGTH })
                : updated}
            </p>
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="ghost"
                disabled={!dirty || save.isPending}
                onClick={() => setDraft(prompt.text)}
              >
                {t(($) => $.tab_body.scenes.prompt_discard)}
              </Button>
              <Button size="sm" disabled={!dirty || tooLong || save.isPending} onClick={() => void submit()}>
                {save.isPending && (
                  <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                )}
                {save.isPending
                  ? t(($) => $.tab_body.scenes.prompt_saving)
                  : t(($) => $.tab_body.scenes.prompt_save)}
              </Button>
            </div>
          </div>
        </>
      ) : (
        <>
          <p className="whitespace-pre-wrap rounded-md border bg-muted/30 px-3 py-2 text-body">
            {prompt.text || t(($) => $.tab_body.scenes.prompt_empty)}
          </p>
          {updated ? <p className="text-caption text-muted-foreground">{updated}</p> : null}
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.prompt_read_only)}</p>
        </>
      )}
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.prompt_pending_note)}</p>
    </section>
  );
}

function SceneCapabilities({
  agentId,
  detail,
  canEdit,
}: {
  agentId: string;
  detail: AgentSceneDetail;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const timeAgo = useTimeAgo();
  const setBinding = useSetAgentSceneBinding(wsId, agentId);
  // One entry per row with a write in flight, so overlapping toggles of
  // different rows never clear each other's pending state.
  const [busyKeys, setBusyKeys] = useState<ReadonlySet<string>>(() => new Set());
  const bindings = useMemo(
    () =>
      new Map<string, AgentSceneBinding>(
        detail.bindings.map((binding) => [`${binding.resourceType}:${binding.resourceId}`, binding]),
      ),
    [detail.bindings],
  );
  const { connectors, skills } = detail.offers;

  const toggle = async (resourceType: ContextResourceType, resourceId: string, enabled: boolean) => {
    const key = `${resourceType}:${resourceId}`;
    if (busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
    try {
      await setBinding.mutateAsync({
        sceneKey: detail.scene.sceneKey,
        resourceType,
        resourceId,
        enabled,
      });
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.tab_body.scenes.toggle_failed),
      );
    } finally {
      setBusyKeys((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    }
  };

  const row = (
    resourceType: ContextResourceType,
    id: string,
    name: string,
    icon: React.ReactNode,
    description?: string,
  ) => {
    const key = `${resourceType}:${id}`;
    const binding = bindings.get(key);
    const enabled = binding?.enabled === true;
    const busy = busyKeys.has(key);
    return (
      <li key={key} className="flex items-center gap-3 p-3">
        {icon}
        <div className="min-w-0 flex-1">
          <p className="truncate text-body font-medium">{name}</p>
          {description ? (
            <p className="truncate text-caption text-muted-foreground">{description}</p>
          ) : null}
          {binding?.updatedByName ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.scenes.changed_by, {
                name: binding.updatedByName,
                when: binding.updatedAt ? timeAgo(binding.updatedAt) : "",
              })}
            </p>
          ) : null}
        </div>
        <span className="flex h-8 w-10 shrink-0 items-center justify-end">
          {busy ? (
            <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />
          ) : (
            <Switch
              checked={enabled}
              disabled={!canEdit}
              onCheckedChange={(next) => void toggle(resourceType, id, next)}
              aria-label={t(($) => $.tab_body.scenes.toggle_aria, { name })}
            />
          )}
        </span>
      </li>
    );
  };

  return (
    <section className="space-y-3" aria-labelledby={`scene-capabilities-${detail.scene.sceneKey}`}>
      <div>
        <h3
          id={`scene-capabilities-${detail.scene.sceneKey}`}
          className="flex items-center gap-1.5 text-body font-medium"
        >
          <Plug className="size-4 text-muted-foreground" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.capabilities_title)}
        </h3>
        <p className="mt-1 text-caption leading-5 text-muted-foreground">
          {t(($) => $.tab_body.scenes.capabilities_hint)}
        </p>
        {detail.scene.kind === "dm" ? (
          <p className="mt-1 text-caption leading-5 text-muted-foreground">
            {t(($) => $.tab_body.scenes.dm_pending_note)}
          </p>
        ) : null}
      </div>
      {connectors.length === 0 && skills.length === 0 ? (
        <PanelNotice>{t(($) => $.tab_body.scenes.offers_empty)}</PanelNotice>
      ) : (
        <>
          {connectors.length > 0 ? (
            <div className="space-y-1.5">
              <h4 className="text-caption font-medium text-muted-foreground">
                {t(($) => $.tab_body.scenes.connectors_label)}
              </h4>
              <ul className="divide-y rounded-lg border bg-card">
                {connectors.map((connector) =>
                  row(
                    "connector",
                    connector.id,
                    connector.name,
                    <ConnectorLogo slug={connector.catalogSlug} />,
                  ),
                )}
              </ul>
            </div>
          ) : null}
          {skills.length > 0 ? (
            <div className="space-y-1.5">
              <h4 className="text-caption font-medium text-muted-foreground">
                {t(($) => $.tab_body.scenes.skills_label)}
              </h4>
              <ul className="divide-y rounded-lg border bg-card">
                {skills.map((skill) =>
                  row(
                    "skill",
                    skill.id,
                    skill.name,
                    <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
                      <SkillIcon className="size-4" />
                    </span>,
                    skill.description,
                  ),
                )}
              </ul>
            </div>
          ) : null}
        </>
      )}
    </section>
  );
}

function PanelNotice({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center text-body text-muted-foreground">
      {children}
    </div>
  );
}
