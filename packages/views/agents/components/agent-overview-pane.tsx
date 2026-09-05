"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { McpConfigTab } from "./tabs/mcp-config-tab";
import { AgentMcpTab } from "./tabs/agent-mcp-tab";
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
import { SceneMemoryTab } from "./tabs/scene-memory-tab";
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
  { id: "inbound", labelKey: "inbound" },
  { id: "memory", labelKey: "memory" },
  { id: "configuration", labelKey: "configuration" },
];

interface AgentOverviewPaneProps {
  agent: Agent;
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
 * Conversations lists short-loop channel transcripts, Memory holds scene
 * text and issue links, and Configuration groups identity, capabilities,
 * connections, execution, and management without changing persistence or
 * permission semantics.
 */
export function AgentOverviewPane({
  agent,
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
  const urlView = navigation.searchParams.get("view");
  const composioMCPAppsEnabled = useFeatureEnabled(
    COMPOSIO_MCP_APPS_FLAG,
    false,
  );
  const initialView = normalizeDetailView(urlView) ?? "overview";
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

  const visibleConfigGroups = useMemo<AgentConfigGroup[]>(() => {
    const showMcp = runtime
      ? runtimeSupportsMcpConfig(runtime.provider, runtime.metadata)
      : true;
    const showComposioMcp =
      composioMCPAppsEnabled && isAgentOwner;

    return AGENT_CONFIG_GROUPS.map((group) => ({
      ...group,
      items: group.items.filter((item) => {
        if (item.id === "mcp_config") return showMcp;
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
  ]);

  const visibleViews = useMemo(
    () =>
      new Set<DetailTab>([
        "overview",
        "work",
        "inbound",
        ...(canEdit ? (["memory"] as const) : []),
        ...visibleConfigGroups.flatMap((group) =>
          group.items.map((item) => item.id),
        ),
      ]),
    [canEdit, visibleConfigGroups],
  );

  const defaultConfigView = visibleConfigGroups[0]?.items[0]?.id;
  const effectiveView = visibleViews.has(activeView)
    ? activeView
    : isConfigView(activeView) && defaultConfigView
      ? defaultConfigView
      : "overview";
  const activeSection = sectionForView(effectiveView);

  const commitView = useCallback(
    (next: DetailTab) => {
      if (isConfigView(next)) lastConfigViewRef.current = next;
      setActiveView(next);
      const params = new URLSearchParams(navigation.searchParams);
      if (next === "overview") params.delete("view");
      else params.set("view", next);
      const query = params.toString();
      navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
    },
    [navigation],
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
    if (
      section === "overview" ||
      section === "work" ||
      section === "inbound" ||
      section === "memory"
    ) {
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
    if (urlView === lastUrlViewRef.current) return;
    lastUrlViewRef.current = urlView;
    const nextView =
      urlView === null ? "overview" : normalizeDetailView(urlView);
    if (!nextView || !visibleViews.has(nextView)) return;

    if (activeDirty && nextView !== effectiveView) {
      setPendingView(nextView);
      const params = new URLSearchParams(navigation.searchParams);
      if (effectiveView === "overview") params.delete("view");
      else params.set("view", effectiveView);
      const query = params.toString();
      navigation.replace(
        `${navigation.pathname}${query ? `?${query}` : ""}`,
      );
      return;
    }
    if (isConfigView(nextView)) lastConfigViewRef.current = nextView;
    setActiveView(nextView);
  }, [activeDirty, effectiveView, navigation, urlView, visibleViews]);

  useEffect(() => {
    if (urlView !== "identity") return;
    const params = new URLSearchParams(navigation.searchParams);
    params.set("view", "digital_employee");
    navigation.replace(`${navigation.pathname}?${params.toString()}`);
  }, [navigation, urlView]);

  useEffect(() => {
    if (navIntent == null) return;
    if (visibleViews.has(navIntent)) requestView(navIntent);
    onNavIntentHandled?.();
  }, [navIntent, onNavIntentHandled, requestView, visibleViews]);

  useEffect(() => {
    if (!activeDirty) return;
    const preventUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", preventUnload);
    return () => window.removeEventListener("beforeunload", preventUnload);
  }, [activeDirty]);

  const secondaryTabs =
    activeSection === "configuration"
      ? visibleConfigGroups.flatMap((group) => group.items)
      : [];
  const activeSecondaryTab = secondaryTabs.find(
    (tab) => tab.id === effectiveView,
  );
  const isSecondaryLayout =
    secondaryTabs.length > 0 && activeSecondaryTab != null;

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-background">
      <div
        className="shrink-0 overflow-x-auto border-b px-4 sm:px-6"
        role="tablist"
        aria-label={t(($) => $.tabs.page_navigation_aria)}
      >
        <div className="mx-auto flex max-w-[1440px] items-center gap-6">
          {(canEdit
            ? TOP_TABS
            : TOP_TABS.filter((tab) => tab.id !== "memory")
          ).map((tab) => (
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
              {t(($) => $.tabs[tab.labelKey])}
            </button>
          ))}
        </div>
      </div>

      {/* Overview/Work scroll as one page. Sidebar views split scrolling on
          md+ (nav rail pinned, content pane scrolls) like settings-page.tsx;
          below md the rail is a horizontal strip and the page scrolls whole. */}
      <div
        className={cn(
          "min-h-0 flex-1",
          isSecondaryLayout
            ? "overflow-y-auto md:overflow-hidden"
            : effectiveView === "inbound" || effectiveView === "memory"
              ? "overflow-hidden"
              : "overflow-y-auto",
        )}
      >
        {effectiveView === "overview" && (
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

        {effectiveView === "inbound" && (
          <div className="flex h-full min-h-0 flex-1 flex-col">
            <CoordinatorSessionsTab agent={agent} />
          </div>
        )}

        {effectiveView === "memory" && (
          <div className="flex h-full min-h-0 flex-1 flex-col">
            <SceneMemoryTab
              agent={agent}
              canEdit={canEdit}
              onUpdate={onUpdate}
            />
          </div>
        )}

        {secondaryTabs.length > 0 && activeSecondaryTab && (
          <div className="flex min-h-full flex-col md:h-full md:flex-row">
            <AgentConfigNav
              groups={visibleConfigGroups}
              activeView={effectiveView}
              onSelect={requestView}
            />

            <section className="min-w-0 flex-1 md:overflow-y-auto">
              <div className="mx-auto w-full max-w-3xl p-4 sm:p-6 md:p-8">
                <header>
                  <h2 className="text-title-sm font-medium text-balance">
                    {t(($) => $.tabs[activeSecondaryTab.labelKey])}
                  </h2>
                </header>

                <div className="mt-6">
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
                  {effectiveView === "mcp_config" && (
                    <McpConfigTab
                      agent={agent}
                      runtime={runtime}
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
