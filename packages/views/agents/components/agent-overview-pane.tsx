"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentSource,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import { runtimeSupportsMcpConfig } from "@multica/core/agents";
import { useFeatureEnabled } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { useWorkspaceId } from "@multica/core/hooks";
import { dingtalkInstallationsOptions } from "@multica/core/dingtalk";
import { larkInstallationsOptions } from "@multica/core/lark";
import { slackInstallationsOptions } from "@multica/core/slack";
import { wecomInstallationsOptions } from "@multica/core/wecom";
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
import { cn } from "@multica/ui/lib/utils";
import { ActivityTab } from "./tabs/activity-tab";
import { InstructionsTab } from "./tabs/instructions-tab";
import { OKRTab } from "./tabs/okr-tab";
import { SkillsTab } from "./tabs/skills-tab";
import { EnvTab } from "./tabs/env-tab";
import { CustomArgsTab } from "./tabs/custom-args-tab";
import { ConnectorsTab } from "./tabs/connectors-tab";
import { AgentMcpTab } from "./tabs/agent-mcp-tab";
import { DshHomeTab } from "./tabs/dsh-home-tab";
import { DshConfigTab } from "./tabs/dsh-config-tab";
import { IntegrationsTab } from "./tabs/integrations-tab";
import { RuntimeConfigTab } from "./tabs/runtime-config-tab";
import { LLMTraceTab } from "./tabs/llm-trace-tab";
import { A2ATab } from "./tabs/a2a-tab";
import { RunnerTab } from "./tabs/runner-tab";
import { AgentDetailInspector } from "./agent-detail-inspector";
import { AgentAccessSettings } from "./agent-access-settings";
import { AgentOverviewSummary } from "./agent-overview-summary";
import { ActorIssuesPanel } from "../../common/actor-issues-panel";
import { CoordinatorSessionsTab } from "./tabs/coordinator-sessions-tab";
import { ScenesTab } from "./tabs/scenes-tab";
import { ExportTab } from "./tabs/export-tab";
import { PackageBindingsPanel } from "./package-bindings-panel";
import { PublishTab } from "./tabs/publish-tab";
import { DigitalEmployeeTab } from "./tabs/digital-employee-tab";
import { AgentMCPAccessTab } from "./tabs/mcp-access-tab";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";
import { AgentConfigNav } from "./agent-config-nav";
import {
  AGENT_CONFIG_GROUPS,
  type AgentConfigGroup,
  type DetailSection,
  type DetailTab,
  isConfigView,
  normalizeDetailView,
  sectionForView,
} from "./agent-config-navigation";

export type { DetailTab } from "./agent-config-navigation";

const TOP_TABS: { id: DetailSection; labelKey: DetailSection }[] = [
  { id: "overview", labelKey: "overview" },
  { id: "work", labelKey: "work" },
  { id: "scenes", labelKey: "scenes" },
  { id: "configuration", labelKey: "configuration" },
];

/** How the agent takes part in the workspace Tag. The template holds the
 * Tag's shared configuration; an employee embodies one tenant and owns its
 * digital employee and scenes, so its shared configuration is managed by the
 * Tag rather than edited here. */
export type AgentTagRole = "template" | "employee";

/** Config views a Tag agent shows; everything else (this computer, local
 * MCP access, runtime-specific tuning, export/publish, access) does not apply
 * to a cloud-only multi-tenant employee. */
const TAG_TEMPLATE_VIEWS = new Set<DetailTab>(["instructions", "skills", "mcp_config", "dsh", "general"]);
const TAG_EMPLOYEE_VIEWS = new Set<DetailTab>(["digital_employee"]);

/** A Tag employee's sections: its tenant configuration, its scenes and its
 * recent work. The template has no sections, only the shared configuration. */
const TAG_EMPLOYEE_TABS: { id: DetailSection; label: "tenant_config" | "scenes" | "recent_work" }[] = [
  { id: "configuration", label: "tenant_config" },
  { id: "scenes", label: "scenes" },
  { id: "overview", label: "recent_work" },
];

/** What a Tag employee's 租户配置 renders: identity, event perception and the
 * inbound coordinator, supplied by the Tag page. */
export type TagTenantConfigRenderer = (props: {
  agent: Agent;
  canEdit: boolean;
  onUpdate: (data: Record<string, unknown>) => Promise<void>;
}) => ReactNode;

interface AgentOverviewPaneProps {
  agent: Agent;
  /** Set when the agent is shown on the Tag page. */
  tagRole?: AgentTagRole;
  /** Renders a Tag employee's 租户配置. */
  renderTenantConfig?: TagTenantConfigRenderer;
  /** URL search param holding the selected view (default "view"). */
  viewParam?: string;
  /** Set by the Tag page, which owns the tab bar: the pane shows exactly
   * this view, with no tab bar, config nav or URL view of its own. */
  tagTab?: DetailTab;
  /** Unsaved edits in the shown view, for a guard owned by the caller. */
  onDirtyChange?: (dirty: boolean) => void;
  runtime: AgentRuntime | null;
  owner: MemberWithUser | null;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>;
  currentUserId?: string | null;
  canEdit: boolean;
  canTransferOwner?: boolean;
  canOperateDingTalkBinding: boolean;
  dingTalkBindingPermissionLoading: boolean;
  source?: AgentSource | null;
  sourceSyncing?: boolean;
  onSourceSync?: () => void;
  onTransferOwner?: (userId: string) => Promise<void>;
  navIntent?: DetailTab | null;
  onNavIntentHandled?: () => void;
}

/**
 * Agent workbench organised around user intent instead of backend fields.
 * Overview answers "what is happening now?", Work owns the issue surface,
 * Scenes lists the DingTalk group chats and 1:1 chats the agent works in
 * (each with its inbound history, memory and scene configuration), and
 * Configuration groups identity, capabilities, connections, execution, and
 * management without changing persistence or permission semantics.
 */
export function AgentOverviewPane({
  agent,
  tagRole,
  renderTenantConfig,
  viewParam = "view",
  tagTab,
  onDirtyChange,
  runtime,
  owner,
  runtimes,
  members,
  onUpdate,
  currentUserId,
  canEdit,
  canTransferOwner = false,
  canOperateDingTalkBinding,
  dingTalkBindingPermissionLoading,
  source = null,
  sourceSyncing = false,
  onSourceSync,
  onTransferOwner,
  navIntent,
  onNavIntentHandled,
}: AgentOverviewPaneProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const urlView = navigation.searchParams.get(viewParam);
  const composioMCPAppsEnabled = useFeatureEnabled(
    COMPOSIO_MCP_APPS_FLAG,
    false,
  );
  // The template opens on its first config view (it has no sections); an
  // employee opens on its tenant configuration.
  const defaultView: DetailTab =
    tagRole === "template" ? "instructions" : tagRole === "employee" ? "digital_employee" : "overview";
  const ownsSceneParams = tagRole !== "template";
  const controlled = tagTab !== undefined;
  const initialView = normalizeDetailView(urlView) ?? defaultView;
  const [activeView, setActiveView] = useState<DetailTab>(() => initialView);
  const lastConfigViewRef = useRef<DetailTab>(
    isConfigView(initialView) ? initialView : "digital_employee",
  );
  const [activeDirty, setActiveDirty] = useState(false);
  const [pendingView, setPendingView] = useState<DetailTab | null>(null);
  const lastUrlViewRef = useRef(urlView);

  const { data: larkListing } = useQuery({
    ...larkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: slackListing } = useQuery({
    ...slackInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: dingtalkListing } = useQuery({
    ...dingtalkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: wecomListing } = useQuery({
    ...wecomInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const botIntegrationsConfigured =
    larkListing?.configured === true ||
    slackListing?.configured === true ||
    dingtalkListing?.configured === true ||
    wecomListing?.configured === true;
  const isAgentOwner =
    !!currentUserId &&
    !!agent.owner_id &&
    agent.owner_id === currentUserId;

  // The agent's own MCP servers need a runtime that reads mcp_config; the
  // rest of the 连接器 tab (official apps, Aone FaaS grants, offers) goes
  // through the server relay and applies to every runtime.
  const supportsOwnMcpConfig = runtime
    ? runtimeSupportsMcpConfig(runtime.provider, runtime.metadata)
    : true;

  const visibleConfigGroups = useMemo<AgentConfigGroup[]>(() => {
    const showComposioMcp =
      composioMCPAppsEnabled && isAgentOwner;

    const tagViews =
      tagRole === "template" ? TAG_TEMPLATE_VIEWS : tagRole === "employee" ? TAG_EMPLOYEE_VIEWS : null;
    return AGENT_CONFIG_GROUPS.map((group) => ({
      ...group,
      items: group.items.filter((item) => {
        if (tagViews && !tagViews.has(item.id)) return false;
        if (item.id === "dsh") return runtime?.provider === "dsh";
        if (item.id === "filesystem") return canEdit && agent.runtime_mode === "cloud";
        if (item.id === "composio_mcp") return showComposioMcp;
        if (item.id === "integrations") return botIntegrationsConfigured;
        if (item.id === "mcp_access" || item.id === "a2a") {
          return isAgentOwner;
        }
        if (item.id === "runner") return canEdit;
        if (item.id === "env") return canEdit;
        if (item.id === "runtime_config") {
          return runtime?.provider === "openclaw";
        }
        if (item.id === "llm_trace") {
          return agent.runtime_mode === "cloud";
        }
        return true;
      }),
    })).filter((group) => group.items.length > 0);
  }, [
    agent.runtime_mode,
    botIntegrationsConfigured,
    canEdit,
    composioMCPAppsEnabled,
    isAgentOwner,
    runtime,
    tagRole,
  ]);

  const visibleViews = useMemo(
    () =>
      new Set<DetailTab>([
        ...(tagRole === "template"
          ? []
          : tagRole === "employee"
            ? (["overview", "scenes"] as DetailTab[])
            : (["overview", "work", "scenes"] as DetailTab[])),
        ...visibleConfigGroups.flatMap((group) =>
          group.items.map((item) => item.id),
        ),
      ]),
    [tagRole, visibleConfigGroups],
  );

  const defaultConfigView = visibleConfigGroups[0]?.items[0]?.id;
  const effectiveView: DetailTab = tagTab !== undefined
    ? tagTab
    : visibleViews.has(activeView)
    ? activeView
    : (isConfigView(activeView) || tagRole === "template") && defaultConfigView
      ? defaultConfigView
      : tagRole === "employee"
        ? "digital_employee"
        : "overview";
  const activeSection = sectionForView(effectiveView);

  const commitView = useCallback(
    (next: DetailTab) => {
      if (isConfigView(next)) lastConfigViewRef.current = next;
      setActiveView(next);
      const params = new URLSearchParams(navigation.searchParams);
      if (next === defaultView) params.delete(viewParam);
      else params.set(viewParam, next);
      // The selected tenant, group or person only means something inside
      // the scenes section.
      if (next !== "scenes" && ownsSceneParams) {
        params.delete("tenant");
        params.delete("node");
        params.delete("scene");
        params.delete("scene_tab");
      }
      // An open app dialog belongs to the view it was opened in (the
      // connector tab or a scene's configuration): never carry it over.
      params.delete("app");
      const query = params.toString();
      navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
    },
    [defaultView, navigation, ownsSceneParams, viewParam],
  );

  const requestView = useCallback(
    (next: DetailTab) => {
      if (next === effectiveView) return;
      if (activeDirty) {
        setPendingView(next);
        return;
      }
      commitView(next);
    },
    [activeDirty, commitView, effectiveView],
  );

  const requestSection = (section: DetailSection) => {
    if (section === "overview" || section === "work" || section === "scenes") {
      requestView(section);
      return;
    }
    const previousConfigView = lastConfigViewRef.current;
    const current = isConfigView(effectiveView)
      ? effectiveView
      : visibleViews.has(previousConfigView)
        ? previousConfigView
        : defaultConfigView;
    if (current) requestView(current);
  };

  const commitViewChange = () => {
    if (!pendingView) return;
    commitView(pendingView);
    setActiveDirty(false);
    setPendingView(null);
  };

  useEffect(() => {
    if (controlled) return;
    if (urlView === lastUrlViewRef.current) return;
    lastUrlViewRef.current = urlView;
    const nextView =
      urlView === null ? defaultView : normalizeDetailView(urlView);
    if (!nextView || !visibleViews.has(nextView)) return;

    if (activeDirty && nextView !== effectiveView) {
      setPendingView(nextView);
      const params = new URLSearchParams(navigation.searchParams);
      if (effectiveView === defaultView) params.delete(viewParam);
      else params.set(viewParam, effectiveView);
      const query = params.toString();
      navigation.replace(
        `${navigation.pathname}${query ? `?${query}` : ""}`,
      );
      return;
    }
    if (isConfigView(nextView)) lastConfigViewRef.current = nextView;
    setActiveView(nextView);
  }, [activeDirty, controlled, defaultView, effectiveView, navigation, urlView, viewParam, visibleViews]);

  // Legacy view names keep working and are rewritten to their new home.
  useEffect(() => {
    if (controlled || urlView === null) return;
    const normalized = normalizeDetailView(urlView);
    if (normalized === null || normalized === urlView) return;
    const params = new URLSearchParams(navigation.searchParams);
    params.set(viewParam, normalized);
    navigation.replace(`${navigation.pathname}?${params.toString()}`);
  }, [controlled, navigation, urlView, viewParam]);

  useEffect(() => {
    if (controlled || urlView === null || normalizeDetailView(urlView) !== null) {
      return;
    }
    const params = new URLSearchParams(navigation.searchParams);
    params.delete(viewParam);
    const query = params.toString();
    navigation.replace(
      `${navigation.pathname}${query ? `?${query}` : ""}`,
    );
  }, [controlled, navigation, urlView, viewParam]);

  useEffect(() => {
    if (navIntent == null) return;
    if (visibleViews.has(navIntent)) requestView(navIntent);
    onNavIntentHandled?.();
  }, [navIntent, onNavIntentHandled, requestView, visibleViews]);

  useEffect(() => {
    onDirtyChange?.(activeDirty);
  }, [activeDirty, onDirtyChange]);

  useEffect(() => {
    if (!activeDirty) return;
    const preventUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", preventUnload);
    return () => window.removeEventListener("beforeunload", preventUnload);
  }, [activeDirty]);

  // A Tag employee's scenes live on the Tag page: connect flows return there,
  // keeping its non-scene params (the selected tenant).
  const sceneReturnPage = useMemo(() => {
    const params = new URLSearchParams(navigation.searchParams);
    for (const key of [viewParam, "tenant", "node", "scene", "scene_tab", "app"]) params.delete(key);
    return { pathname: navigation.pathname, params: params.toString(), viewParam };
  }, [navigation.pathname, navigation.searchParams, viewParam]);

  const secondaryTabs =
    activeSection === "configuration"
      ? visibleConfigGroups.flatMap((group) => group.items)
      : [];
  const activeSecondaryTab = secondaryTabs.find(
    (tab) => tab.id === effectiveView,
  );
  const isSecondaryLayout =
    secondaryTabs.length > 0 && activeSecondaryTab != null;

  // The Tag template is only its shared configuration; a Tag employee has
  // its tenant configuration, scenes and recent work.
  const topTabs: { id: DetailSection; label: string }[] =
    tagRole === "template"
      ? []
      : tagRole === "employee"
        ? TAG_EMPLOYEE_TABS.map((tab) => ({
            id: tab.id,
            label:
              tab.label === "scenes"
                ? t(($) => $.tabs.scenes)
                : tab.label === "tenant_config"
                  ? t(($) => $.tag_tenant.tab_tenant_config)
                  : t(($) => $.tag_tenant.tab_recent_work),
          }))
        : TOP_TABS.map((tab) => ({ id: tab.id, label: t(($) => $.tabs[tab.labelKey]) }));

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      {topTabs.length > 0 && !controlled ? (
        <div
          className="shrink-0 overflow-x-auto border-b px-4 sm:px-6"
          role="tablist"
          aria-label={t(($) => $.tabs.page_navigation_aria)}
        >
          <div className="mx-auto flex max-w-[1440px] items-center gap-6">
            {topTabs.map((tab) => (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={activeSection === tab.id}
                onClick={() => requestSection(tab.id)}
                className={cn(
                  "relative shrink-0 py-3 text-body font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                  activeSection === tab.id
                    ? "text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {tab.label}
              </button>
            ))}
          </div>
        </div>
      ) : null}

      {/* Overview/Work scroll as one page. Sidebar views split scrolling on
          md+ (nav rail pinned, content pane scrolls) like settings-page.tsx;
          below md the rail is a horizontal strip and the page scrolls whole. */}
      <div
        className={cn(
          "min-h-0 flex-1",
          isSecondaryLayout
            ? "overflow-y-auto md:overflow-hidden"
            : effectiveView === "scenes"
              ? "overflow-hidden"
              : "overflow-y-auto",
        )}
      >
        {effectiveView === "overview" && tagRole === "employee" && (
          <div className="mx-auto max-w-3xl p-4 sm:p-6">
            <ActivityTab agent={agent} showPerformance={false} />
          </div>
        )}

        {effectiveView === "overview" && tagRole !== "employee" && (
          <div className="mx-auto max-w-[1440px] p-4 sm:p-6">
            <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_320px]">
              <ActivityTab agent={agent} showPerformance={false} />
              <AgentOverviewSummary
                agent={agent}
                runtime={runtime}
                owner={owner}
                members={members}
                canTransferOwner={canTransferOwner}
                source={source}
                canSyncSource={canEdit}
                sourceSyncing={sourceSyncing}
                onSourceSync={onSourceSync}
                onTransferOwner={onTransferOwner}
              />
            </div>
          </div>
        )}

        {effectiveView === "work" && (
          <div className="flex min-h-[620px] flex-col">
            <ActorIssuesPanel actorType="agent" actorId={agent.id} />
          </div>
        )}

        {/* The admin scene API needs agent-manager rights; everyone else
            keeps read access to the inbound conversation history. */}
        {effectiveView === "scenes" &&
          (canEdit ? (
            <ScenesTab
              key={agent.id}
              agent={agent}
              canEdit={canEdit}
              tagManaged={tagRole === "employee"}
              returnPage={tagRole === "employee" ? sceneReturnPage : undefined}
              onUpdate={onUpdate}
              onDirtyChange={setActiveDirty}
            />
          ) : (
            <CoordinatorSessionsTab key={agent.id} agent={agent} />
          ))}

        {secondaryTabs.length > 0 && activeSecondaryTab && tagRole === "employee" && (
          <section className="min-h-full md:h-full md:overflow-y-auto">
            <div className="mx-auto w-full max-w-3xl p-4 sm:p-6">
              {renderTenantConfig?.({ agent, canEdit, onUpdate: (data) => onUpdate(agent.id, data) })}
            </div>
          </section>
        )}

        {secondaryTabs.length > 0 && activeSecondaryTab && tagRole !== "employee" && (
          <div className="flex min-h-full flex-col md:h-full md:flex-row">
            {controlled ? null : (
              <AgentConfigNav
                groups={visibleConfigGroups}
                activeView={effectiveView}
                onSelect={requestView}
              />
            )}

            <section className="min-w-0 flex-1 md:overflow-y-auto">
              <div className="mx-auto w-full max-w-3xl p-4 sm:p-6 md:p-8">
                {controlled ? null : (
                  <header>
                    <h2 className="text-title-sm font-medium text-balance">
                      {t(($) => $.tabs[activeSecondaryTab.labelKey])}
                    </h2>
                  </header>
                )}

                {tagRole === "template" && (effectiveView === "skills" || effectiveView === "mcp_config") ? (
                  <section
                    role="note"
                    className="mt-4 rounded-xl border border-dashed border-surface-border bg-muted/20 px-4 py-3"
                  >
                    <p className="text-body font-medium">{t(($) => $.tag_tenant.bundle_title)}</p>
                    <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.tag_tenant.bundle_hint)}</p>
                  </section>
                ) : null}

                <div className="mt-6">
                  {canEdit && source && effectiveView !== "publish" && <PackageBindingsPanel agentId={agent.id} expanded={false} onNavigate={(tab) => { const view = normalizeDetailView(tab); if (view) requestView(view); }} />}
                  {effectiveView === "digital_employee" && (
                    <DigitalEmployeeTab
                      agent={agent}
                      runtime={runtime}
                      members={members}
                      currentUserId={currentUserId ?? null}
                      canEdit={canEdit}
                      canOperateDingTalkBinding={canOperateDingTalkBinding}
                      dingTalkBindingPermissionLoading={
                        dingTalkBindingPermissionLoading
                      }
                      onUpdate={onUpdate}
                      onDirtyChange={setActiveDirty}
                    />
                  )}
                  {effectiveView === "instructions" && (
                    <InstructionsTab
                      agent={agent}
                      onSave={(patch) => onUpdate(agent.id, patch)}
                      onDirtyChange={setActiveDirty}
                      readOnly={!canEdit}
                      instructionsLocked={source != null}
                    />
                  )}
                  {effectiveView === "okr" && (
                    <OKRTab
                      agent={agent}
                      onDirtyChange={setActiveDirty}
                      readOnly={!canEdit}
                    />
                  )}
                  {effectiveView === "skills" && (
                    <SkillsTab
                      agent={agent}
                      runtime={runtime}
                      canEdit={canEdit}
                    />
                  )}
                  {effectiveView === "filesystem" && (
                    <DshHomeTab
                      workspaceId={wsId}
                      agentId={agent.id}
                      canEdit={canEdit}
                      includeSharedDisk
                    />
                  )}
                  {effectiveView === "dsh" && (
                    <DshConfigTab
                      key={agent.id}
                      workspaceId={wsId}
                      agent={agent}
                      runtime={runtime}
                      canEdit={canEdit}
                    />
                  )}
                  {effectiveView === "mcp_config" && (
                    <ConnectorsTab
                      agent={agent}
                      runtime={runtime}
                      supportsOwnMcpConfig={supportsOwnMcpConfig}
                      onSave={(updates) => onUpdate(agent.id, updates)}
                      onDirtyChange={setActiveDirty}
                      canEdit={canEdit}
                    />
                  )}
                  {effectiveView === "composio_mcp" && (
                    <AgentMcpTab agent={agent} />
                  )}
                  {effectiveView === "integrations" && (
                    <IntegrationsTab
                      agent={agent}
                      onUpdate={onUpdate}
                      canEdit={canEdit}
                    />
                  )}
                  {effectiveView === "mcp_access" && (
                    <AgentMCPAccessTab agent={agent} />
                  )}
                  {effectiveView === "general" && (
                    <AgentDetailInspector
                      agent={agent}
                      runtime={runtime}
                      runtimes={runtimes}
                      members={members}
                      currentUserId={currentUserId ?? null}
                      canEdit={canEdit}
                      sourceManaged={source != null}
                      onUpdate={onUpdate}
                    />
                  )}
                  {effectiveView === "runner" && (
                    <RunnerTab
                      agent={agent}
                      canBind={
                        !!currentUserId && agent.owner_id === currentUserId
                      }
                    />
                  )}
                  {effectiveView === "export" && <ExportTab agentId={agent.id} canEdit={canEdit} />}
                  {effectiveView === "publish" && (
                    <>
                      <PublishTab agentId={agent.id} key={`${agent.id}:${source?.ref}:${source?.synced_commit_sha}`} source={source} canEdit={canEdit} />
                      {canEdit && source && <details className="mt-6 space-y-3 rounded-md border p-4">
                        <summary className="cursor-pointer text-body font-medium">{t(($) => $.package_bindings.title)}</summary>
                        <PackageBindingsPanel agentId={agent.id} expanded onNavigate={(tab) => { const view = normalizeDetailView(tab); if (view) requestView(view); }} />
                      </details>}
                    </>
                  )}
                  {effectiveView === "access" && (
                    <AgentAccessSettings
                      agent={agent}
                      members={members}
                      currentUserId={currentUserId ?? null}
                      onDirtyChange={setActiveDirty}
                      onUpdate={onUpdate}
                    />
                  )}
                  {effectiveView === "env" && (
                    <EnvTab agent={agent} onDirtyChange={setActiveDirty} />
                  )}
                  {effectiveView === "custom_args" && (
                    <CustomArgsTab
                      agent={agent}
                      runtimeDevice={runtime ?? undefined}
                      onSave={(updates) => onUpdate(agent.id, updates)}
                      onDirtyChange={setActiveDirty}
                    />
                  )}
                  {effectiveView === "runtime_config" && (
                    <RuntimeConfigTab
                      agent={agent}
                      onSave={(updates) => onUpdate(agent.id, updates)}
                      onDirtyChange={setActiveDirty}
                    />
                  )}
                  {effectiveView === "llm_trace" && (
                    <LLMTraceTab
                      agent={agent}
                      onSave={(updates) => onUpdate(agent.id, updates)}
                      onDirtyChange={setActiveDirty}
                    />
                  )}
                  {effectiveView === "a2a" && <A2ATab agent={agent} />}
                </div>
              </div>
            </section>
          </div>
        )}
      </div>

      {pendingView !== null && (
        <AlertDialog
          open
          onOpenChange={(open) => {
            if (!open) setPendingView(null);
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t(($) => $.tabs.discard_dialog_title)}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t(($) => $.tabs.discard_dialog_description)}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>
                {t(($) => $.tabs.discard_keep)}
              </AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                onClick={commitViewChange}
              >
                {t(($) => $.tabs.discard_confirm)}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}
