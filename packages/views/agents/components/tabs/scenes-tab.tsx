"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Brain, Building2, Loader2, MessageSquare, RefreshCw, User, Users } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { agentCoordinatorConversationsKeys, agentSceneMemoryDetailOptions } from "@multica/core/agents";
import {
  agentContextCapabilitiesOptions,
  agentTenantGroupsOptions,
  agentTenantPersonsOptions,
  agentTenantsOptions,
  contextCapabilityKeys,
  contextNodeOptions,
  type AgentTenant,
  type ContextNodeRef,
  type ContextSceneKind,
} from "@multica/core/context-capabilities";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
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
import { useT } from "../../../i18n";
import { APP_PARAM, useConnectReturnToast, useDesktopConnectHandoff, useReplaceSearch } from "./connect-flow";
import { ContextBuilderPanel, type ContextBuilderConnect } from "./context-builder-panel";
import { ConfigureLink } from "./context-offers-section";
import { CoordinatorConversationMessages, CoordinatorSessionsTab } from "./coordinator-sessions-tab";
import { MemoryFlagBar, SceneMemoryDetail, SceneMemoryTab } from "./scene-memory-tab";
import { SceneTree, type SceneSelection } from "./scene-tree";
import { TenantCreateDialog, TenantSettings } from "./tenant-dialog";

export type SceneSubTab = "inbound" | "memory" | "config" | "settings";

/** Sub-tabs of each node: a tenant has its 配置 and 设置; a scene (a group
 * chat or a 1:1 chat) and a person have 入站记录, 记忆 and 配置. The first one
 * is the default. */
const SUB_TABS: Record<SceneSelection["type"], readonly SceneSubTab[]> = {
  org: ["config", "settings"],
  scene: ["inbound", "memory", "config"],
  person: ["inbound", "memory", "config"],
};

/** A Tag employee's scenes: memory runs on defaults and tenants are managed
 * on the Tag page, so a level is its configuration and its inbound history. */
const TAG_SUB_TABS: Record<SceneSelection["type"], readonly SceneSubTab[]> = {
  org: ["config"],
  scene: ["config", "inbound"],
  person: ["config", "inbound"],
};

function subTabsFor(type: SceneSelection["type"], tagManaged: boolean): readonly SceneSubTab[] {
  return (tagManaged ? TAG_SUB_TABS : SUB_TABS)[type];
}

function subTabFor(type: SceneSelection["type"], value: string | null, tagManaged = false): SceneSubTab {
  const tabs = subTabsFor(type, tagManaged);
  return tabs.find((tab) => tab === value) ?? tabs[0] ?? "config";
}

/** `?tenant=<orgId>&node=<scopeType>:<scopeKey>` (a scene's key is its
 * scene_id, a person's their staffId); a tenant itself has no `node` (or
 * `node=org:<orgId>`). */
function selectionFromParams(params: URLSearchParams): SceneSelection | null {
  const orgId = params.get("tenant") ?? "";
  if (!orgId) return null;
  const node = params.get("node") ?? "";
  const separator = node.indexOf(":");
  const type = separator > 0 ? node.slice(0, separator) : "";
  const key = separator > 0 ? node.slice(separator + 1) : "";
  if ((type === "scene" || type === "person") && key) return { orgId, type, key };
  return { orgId, type: "org", key: orgId };
}

function nodeOf(selection: SceneSelection): ContextNodeRef {
  return { orgId: selection.orgId, scopeType: selection.type, scopeKey: selection.key };
}

/** The tenant an old `?scene=<key>` link belongs to: the agent's own
 * DingTalk org, else the first tenant. */
function legacyTenant(tenants: AgentTenant[]): AgentTenant | undefined {
  return tenants.find((tenant) => tenant.source === "identity") ?? tenants[0];
}

/** Full lists of records the tree may not cover. */
type SceneArchive = "inbound" | "memory";

/**
 * 场域: a tree of the agent's tenants (企业, each with its DingTalk OrgId)
 * and, under each, its scenes (群聊和单聊: a group chat or a 1:1 chat, each its
 * own scene keyed by scene_id) and its 个人 (people's personal levels). A
 * tenant opens on 配置 (its Context Builder) and 设置; a scene and a person on
 * 入站记录, 记忆 and 配置 (a person's records are their 1:1 chat's). The
 * selection lives in the URL (`tenant`, `node`, `scene_tab`); an old
 * `scene=<key>` link opens that scene under the agent's own org. 其他记录
 * opens the full inbound conversation and scene memory lists, which also
 * cover records the tree does not list.
 *
 * Switching node, sub-tab or view unmounts the prompt editor, so it goes
 * through the same discard confirmation as the pane's own tabs while the
 * prompt components have unsaved edits.
 */
/** Where a scene's connect flow returns when the scenes are not on the agent
 * page (the Tag page shows them in its tenant pane). */
export interface ScenesReturnPage {
  pathname: string;
  /** Params kept on return (e.g. the selected Tag tenant). */
  params: string;
  viewParam: string;
}

export function ScenesTab({
  agent,
  canEdit,
  tagManaged = false,
  returnPage,
  onUpdate,
  onDirtyChange,
}: {
  agent: Agent;
  canEdit: boolean;
  returnPage?: ScenesReturnPage;
  /** The agent is a Tag tenant's employee: its one enterprise is its DingTalk
   * identity and tenants are created, renamed and removed on the Tag page, so
   * the tree offers no 新建租户 and org nodes have no 设置. */
  tagManaged?: boolean;
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const isCompact = useIsCompact();
  useConnectReturnToast();
  const tenantsQuery = useQuery(agentTenantsOptions(wsId, agent.id));
  const tenants = useMemo(() => tenantsQuery.data?.tenants ?? [], [tenantsQuery.data]);
  const unassignedOrgs = tenantsQuery.data?.unassignedOrgs ?? [];

  const [selection, setSelection] = useState<SceneSelection | null>(() =>
    selectionFromParams(navigation.searchParams),
  );
  const [subTab, setSubTab] = useState<SceneSubTab>(() =>
    subTabFor(selection?.type ?? "org", navigation.searchParams.get("scene_tab"), tagManaged),
  );
  // An old `?scene=<key>` link, resolved once the tenants are known.
  const [legacyScene, setLegacyScene] = useState(() =>
    selection ? "" : (navigation.searchParams.get("scene") ?? ""),
  );

  const writeUrl = useCallback(
    (next: SceneSelection | null, tab: SceneSubTab) => {
      const params = new URLSearchParams(navigation.searchParams);
      for (const key of ["scene", "tenant", "node", "scene_tab"]) params.delete(key);
      if (next) {
        params.set("tenant", next.orgId);
        if (next.type !== "org") params.set("node", `${next.type}:${next.key}`);
        if (tab !== subTabsFor(next.type, tagManaged)[0]) params.set("scene_tab", tab);
      }
      // An app dialog belongs to the node it was opened in.
      params.delete(APP_PARAM);
      const search = params.toString();
      navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
    },
    [navigation, tagManaged],
  );

  // Map an old scene link, or open the first tenant on wide screens, once
  // the tenants are known. The default is pinned into state so a refetch
  // never swaps the open detail (and an unsaved draft) away.
  const firstTenant = tenants[0];
  useEffect(() => {
    if (selection || !tenantsQuery.isSuccess) return;
    if (legacyScene) {
      const tenant = legacyTenant(tenants);
      setLegacyScene("");
      if (!tenant) return;
      const next: SceneSelection = { orgId: tenant.orgId, type: "scene", key: legacyScene };
      const tab = subTabFor("scene", navigation.searchParams.get("scene_tab"), tagManaged);
      setSelection(next);
      setSubTab(tab);
      writeUrl(next, tab);
      return;
    }
    if (!isCompact && firstTenant) {
      setSelection({ orgId: firstTenant.orgId, type: "org", key: firstTenant.orgId });
      setSubTab("config");
    }
  }, [firstTenant, isCompact, legacyScene, navigation.searchParams, selection, tagManaged, tenants, tenantsQuery.isSuccess, writeUrl]);

  const [archive, setArchive] = useState<SceneArchive | null>(null);
  const [creating, setCreating] = useState<{ orgId: string } | null>(null);
  const [draftDirty, setDraftDirty] = useState(false);
  // The navigation waiting for the discard confirmation.
  const [pendingAction, setPendingAction] = useState<(() => void) | null>(null);

  const reportDirty = useCallback(
    (dirty: boolean) => {
      setDraftDirty(dirty);
      onDirtyChange?.(dirty);
    },
    [onDirtyChange],
  );

  // Runs a navigation that unmounts the prompt editor, after confirmation
  // when the prompt components have unsaved edits.
  const guarded = (action: () => void) => {
    if (draftDirty) {
      setPendingAction(() => action);
      return;
    }
    action();
  };

  const sameNode = (a: SceneSelection | null, b: SceneSelection | null) =>
    a?.orgId === b?.orgId && a?.type === b?.type && a?.key === b?.key;

  const select = (next: SceneSelection | null) => {
    const tab = next ? (sameNode(next, selection) ? subTab : subTabsFor(next.type, tagManaged)[0] ?? "config") : "config";
    const commit = () => {
      setSelection(next);
      setSubTab(tab);
      writeUrl(next, tab);
    };
    // Re-selecting the open node keeps its detail (and editor) mounted.
    if (sameNode(next, selection)) commit();
    else guarded(commit);
  };

  const selectSubTab = (tab: SceneSubTab) => {
    if (tab === subTab) return;
    guarded(() => {
      setSubTab(tab);
      if (selection) writeUrl(selection, tab);
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

  const selectedTenant = selection ? (tenants.find((tenant) => tenant.orgId === selection.orgId) ?? null) : null;

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      {tagManaged ? null : <MemoryFlagBar agent={agent} canEdit={canEdit} onUpdate={onUpdate} />}
      <div className="flex min-h-0 flex-1">
        <aside
          className={cn(
            "min-h-0 flex-col border-r",
            isCompact ? (selection ? "hidden" : "flex w-full") : "flex w-72 shrink-0",
          )}
        >
          <div className="shrink-0 px-3 pt-3 pb-2">
            <h2 className="text-body font-semibold">{t(($) => $.tabs.scenes)}</h2>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-3">
            {tenantsQuery.isLoading ? (
              <p className="p-2 text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.loading)}</p>
            ) : tenantsQuery.isError && !tenantsQuery.data ? (
              <div role="alert" className="space-y-2 p-2 text-caption">
                <p>{t(($) => $.tab_body.scenes.load_failed)}</p>
                <Button size="sm" variant="outline" onClick={() => void tenantsQuery.refetch()}>
                  {t(($) => $.tab_body.scenes.retry)}
                </Button>
              </div>
            ) : (
              <>
                {tenants.length === 0 && unassignedOrgs.length === 0 ? (
                  <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-muted-foreground">
                    <Building2 className="size-8 text-faint-foreground" aria-hidden="true" />
                    <p className="text-body font-medium text-foreground">
                      {tagManaged ? t(($) => $.tab_body.scenes.tag_empty_title) : t(($) => $.tab_body.scenes.empty_title)}
                    </p>
                    <p className="text-caption text-pretty">
                      {tagManaged ? t(($) => $.tab_body.scenes.tag_empty_hint) : t(($) => $.tab_body.scenes.empty_hint)}
                    </p>
                  </div>
                ) : null}
                <SceneTree
                  wsId={wsId}
                  agentId={agent.id}
                  tenants={tenants}
                  unassignedOrgs={unassignedOrgs}
                  selection={selection}
                  onSelect={select}
                  onCreateTenant={tagManaged ? undefined : (orgId) => setCreating({ orgId: orgId ?? "" })}
                />
              </>
            )}
            <section className="mt-6 space-y-1 px-2" aria-labelledby="scenes-other-records">
              <h3
                id="scenes-other-records"
                title={t(($) => $.tab_body.scenes.other_records_hint)}
                className="text-caption font-medium text-muted-foreground"
              >
                {t(($) => $.tab_body.scenes.other_records_title)}
              </h3>
              <Button variant="ghost" size="sm" className="w-full justify-start" onClick={() => openArchive("inbound")}>
                <MessageSquare className="size-3.5" aria-hidden="true" />
                {t(($) => $.tab_body.scenes.all_inbound)}
              </Button>
              {tagManaged ? null : (
                <Button variant="ghost" size="sm" className="w-full justify-start" onClick={() => openArchive("memory")}>
                  <Brain className="size-3.5" aria-hidden="true" />
                  {t(($) => $.tab_body.scenes.all_memory)}
                </Button>
              )}
            </section>
          </div>
        </aside>
        <section
          className={cn("min-h-0 min-w-0 flex-1 flex-col", isCompact && !selection ? "hidden" : "flex")}
        >
          {selection ? (
            <NodeDetail
              key={`${selection.orgId}|${selection.type}|${selection.key}`}
              agent={agent}
              selection={selection}
              tenant={selectedTenant}
              tenantsLoading={tenantsQuery.isLoading}
              subTab={subTab}
              onSubTab={selectSubTab}
              canEdit={canEdit}
              tagManaged={tagManaged}
              returnPage={returnPage}
              onBack={isCompact ? () => select(null) : undefined}
              onTenantDeleted={() => {
                const next = tenants.find((tenant) => tenant.orgId !== selection.orgId);
                const fallback: SceneSelection | null =
                  next && !isCompact ? { orgId: next.orgId, type: "org", key: next.orgId } : null;
                setSelection(fallback);
                setSubTab("config");
                writeUrl(fallback, "config");
              }}
              onDirtyChange={reportDirty}
            />
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-muted-foreground">
              <Building2 className="size-10" aria-hidden="true" />
              <p className="text-body">{t(($) => $.tab_body.scenes.select_prompt)}</p>
            </div>
          )}
        </section>
      </div>
      <TenantCreateDialog
        wsId={wsId}
        agentId={agent.id}
        open={creating !== null}
        initialOrgId={creating?.orgId ?? ""}
        onOpenChange={(open) => {
          if (!open) setCreating(null);
        }}
        onCreated={(tenant) => {
          setCreating(null);
          select({ orgId: tenant.orgId, type: "org", key: tenant.orgId });
        }}
      />
      {discardDialog}
    </div>
  );
}

/** Connect plumbing of the agent detail page: the provider returns to this
 * node's 配置 with the app dialog open; desktop runs the connect in the
 * system browser. */
function useAgentPageConnect(
  agentId: string,
  selection: SceneSelection,
  returnPage?: ScenesReturnPage,
): ContextBuilderConnect {
  const paths = useWorkspacePaths();
  const handOff = useDesktopConnectHandoff();
  return {
    returnPath: (slug) => {
      const params = new URLSearchParams(returnPage?.params ?? "");
      params.set(returnPage?.viewParam ?? "view", "scenes");
      params.set("tenant", selection.orgId);
      if (selection.type !== "org") params.set("node", `${selection.type}:${selection.key}`);
      if (selection.type !== "org") params.set("scene_tab", "config");
      params.set(APP_PARAM, slug);
      return `${returnPage?.pathname ?? paths.agentDetail(agentId)}?${params.toString()}`;
    },
    navigate: (url) => window.location.assign(url),
    handOff,
  };
}

function NodeDetail({
  agent,
  selection,
  tenant,
  tenantsLoading,
  subTab,
  onSubTab,
  canEdit,
  tagManaged,
  returnPage,
  onBack,
  onTenantDeleted,
  onDirtyChange,
}: {
  agent: Agent;
  selection: SceneSelection;
  tenant: AgentTenant | null;
  tenantsLoading: boolean;
  subTab: SceneSubTab;
  onSubTab: (tab: SceneSubTab) => void;
  canEdit: boolean;
  tagManaged: boolean;
  returnPage?: ScenesReturnPage;
  onBack?: () => void;
  onTenantDeleted: () => void;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const replaceSearch = useReplaceSearch();
  const connect = useAgentPageConnect(agent.id, selection, returnPage);
  const capabilities = useQuery({
    ...agentContextCapabilitiesOptions(wsId, agent.id),
    enabled: canEdit && Boolean(wsId),
  });
  const configureUrl = capabilities.data?.configureUrl ?? "";
  const summary = useNodeSummary(wsId, agent.id, selection);
  const labels: Record<SceneSubTab, string> = {
    inbound: t(($) => $.tab_body.scenes.tab_inbound),
    memory: t(($) => $.tab_body.scenes.tab_memory),
    config: t(($) => $.tab_body.scenes.tab_config),
    settings: t(($) => $.tab_body.scenes.tab_settings),
  };
  const tenantTitle = tenant ? tenant.name || tenant.orgId : selection.orgId;
  const dm = selection.type === "scene" && summary.kind === "dm";
  const title =
    selection.type === "org"
      ? tenantTitle
      : summary.title ||
        (selection.type === "person"
          ? selection.key
          : dm
            ? t(($) => $.context_config.scene_untitled_dm)
            : t(($) => $.tab_body.scenes.untitled_group));
  const kindLabel =
    selection.type === "org"
      ? t(($) => $.tab_body.context_builder.layer_org)
      : selection.type === "person"
        ? t(($) => $.tab_body.context_builder.layer_person)
        : dm
          ? t(($) => $.context_config.kind_dm)
          : t(($) => $.tab_body.context_builder.layer_scene);
  const Icon = selection.type === "org" ? Building2 : selection.type === "person" ? User : dm ? MessageSquare : Users;

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.tenants(wsId, agent.id) });
    void queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.contextNodes(wsId, agent.id) });
    if (summary.inboundSessionId) {
      void queryClient.invalidateQueries({
        queryKey: agentCoordinatorConversationsKeys.messages(wsId, agent.id, summary.inboundSessionId),
      });
    }
  };

  const builder = (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-3xl p-4 sm:p-6">
        <ContextBuilderPanel
          wsId={wsId}
          agentId={agent.id}
          node={nodeOf(selection)}
          canEdit={canEdit}
          tagFraming={tagManaged}
          connect={connect}
          openApp={navigation.searchParams.get(APP_PARAM) ?? ""}
          onOpenAppChange={(slug) =>
            replaceSearch((params) => {
              if (slug) params.set(APP_PARAM, slug);
              else params.delete(APP_PARAM);
            })
          }
          onDirtyChange={onDirtyChange}
          footer={
            configureUrl ? (
              <div className="space-y-1.5">
                <p className="text-caption font-medium text-muted-foreground">
                  {t(($) => $.tab_body.context_offers.configure_title)}
                </p>
                <ConfigureLink url={configureUrl} />
              </div>
            ) : null
          }
        />
      </div>
    </div>
  );

  let body: React.ReactNode;
  if (selection.type === "org" && !tenant) {
    body = tenantsLoading ? (
      <PanelNotice>
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.scenes.loading)}
      </PanelNotice>
    ) : (
      <PanelNotice>{t(($) => $.tab_body.scenes.tenant_missing)}</PanelNotice>
    );
  } else if (subTab === "settings" && tenant) {
    body = (
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl p-4 sm:p-6">
          <TenantSettings wsId={wsId} agentId={agent.id} tenant={tenant} onDeleted={onTenantDeleted} />
        </div>
      </div>
    );
  } else if (subTab === "inbound") {
    body = summary.loading ? (
      <PanelNotice>
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.scenes.detail_loading)}
      </PanelNotice>
    ) : summary.inboundSessionId ? (
      <CoordinatorConversationMessages
        key={summary.inboundSessionId}
        agentId={agent.id}
        sessionId={summary.inboundSessionId}
      />
    ) : (
      <PanelNotice>{t(($) => $.tab_body.scenes.inbound_empty)}</PanelNotice>
    );
  } else if (subTab === "memory") {
    body = (
      <SceneMemoryPanel
        agent={agent}
        sceneId={summary.memorySceneId}
        loading={summary.loading}
        canEdit={canEdit}
      />
    );
  } else {
    body = builder;
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2.5 sm:px-4">
        {onBack ? (
          <Button variant="ghost" size="sm" onClick={onBack}>
            <ArrowLeft className="size-4" aria-hidden="true" />
            {t(($) => $.tab_body.scenes.back)}
          </Button>
        ) : null}
        <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="flex min-w-0 flex-1 items-baseline gap-2">
          <h2 className="min-w-0 truncate text-body font-semibold">{title}</h2>
          {selection.type === "org" ? (
            <span className="min-w-0 truncate font-mono text-caption text-muted-foreground">{selection.orgId}</span>
          ) : (
            <span className="min-w-0 truncate text-caption text-muted-foreground">{tenantTitle}</span>
          )}
        </div>
        <Badge variant="outline" className="shrink-0">
          {kindLabel}
        </Badge>
        <Button variant="ghost" size="sm" onClick={refresh}>
          <RefreshCw className="size-3.5" aria-hidden="true" />
          {t(($) => $.tab_body.scenes.refresh)}
        </Button>
      </div>
      <div
        className="flex shrink-0 items-center gap-5 overflow-x-auto border-b px-4"
        role="tablist"
        aria-label={title}
      >
        {subTabsFor(selection.type, tagManaged).map((tab) => (
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
      {body}
    </div>
  );
}

interface NodeSummary {
  loading: boolean;
  title: string;
  /** Kind of the node's chat; "group" until it is known. */
  kind: ContextSceneKind;
  inboundSessionId: string;
  /** scene_id of the node's chat when it has Scene Memory, else "". */
  memorySceneId: string;
}

/** What the detail shows about the selected scene or person: its title,
 * and its chat's kind, newest inbound session and memory. The node's own
 * read (shared with the builder) names the chat, a person's 1:1 chat scene
 * included; a scene's list row answers first when the tree has it. */
function useNodeSummary(wsId: string, agentId: string, selection: SceneSelection): NodeSummary {
  const leaf = selection.type !== "org";
  const node = useQuery({
    ...contextNodeOptions(wsId, agentId, nodeOf(selection)),
    enabled: leaf && Boolean(wsId && agentId && selection.orgId && selection.key),
  });
  // The tree's lists, read from the cache only.
  const scenes = useInfiniteQuery({ ...agentTenantGroupsOptions(wsId, agentId, selection.orgId), enabled: false });
  const persons = useQuery({ ...agentTenantPersonsOptions(wsId, agentId, selection.orgId), enabled: false });
  if (!leaf) return { loading: false, title: "", kind: "group", inboundSessionId: "", memorySceneId: "" };
  const listedScene =
    selection.type === "scene"
      ? (scenes.data?.pages ?? []).flatMap((page) => page.scenes).find((scene) => scene.sceneId === selection.key)
      : undefined;
  const listedPerson =
    selection.type === "person" ? (persons.data ?? []).find((entry) => entry.staffId === selection.key) : undefined;
  const scene = node.data?.scene ?? listedScene ?? null;
  return {
    loading: node.isLoading && !listedScene,
    title: listedScene?.title || listedPerson?.title || node.data?.scope?.title || scene?.title || "",
    kind: scene?.kind ?? "group",
    inboundSessionId: scene?.inboundSessionId ?? "",
    memorySceneId: scene?.hasMemory === true ? scene.sceneId : "",
  };
}

/** A scene's or person's memory, loaded by the scene_id of its chat (the
 * agent-wide list holds only the 200 newest rows). */
function SceneMemoryPanel({
  agent,
  sceneId,
  loading,
  canEdit,
}: {
  agent: Agent;
  sceneId: string;
  loading: boolean;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const uiEnabled = agent.scene_memory_ui_enabled === true;
  const query = useQuery(agentSceneMemoryDetailOptions(wsId, agent.id, sceneId, uiEnabled));
  if (!uiEnabled) {
    return <PanelNotice>{t(($) => $.tab_body.scenes.memory_ui_off)}</PanelNotice>;
  }
  if (loading || (sceneId && query.isLoading)) {
    return (
      <PanelNotice>
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.tab_body.inbound.memory_loading)}
      </PanelNotice>
    );
  }
  if (!sceneId) {
    return <PanelNotice>{t(($) => $.tab_body.scenes.memory_empty)}</PanelNotice>;
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
  // A malformed response parses to a row without a scene_id.
  const memory = query.data?.scene_id ? query.data : null;
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
