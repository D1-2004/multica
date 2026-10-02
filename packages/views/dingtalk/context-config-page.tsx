"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  ArrowLeftRight,
  Building2,
  CalendarClock,
  CheckCircle2,
  Loader2,
  MessageCircle,
  SlidersHorizontal,
  User,
  Users,
  type LucideIcon,
} from "lucide-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError } from "@multica/core/api";
import {
  contextConfigAgentOptions,
  contextConfigAgentsOptions,
  contextConfigSceneOptions,
  useRedeemContextConfigLink,
  useResolveContextConfigScene,
  useSetContextCapabilityBinding,
  type ContextCapabilityBinding,
  type ContextConfigAgentDetail,
  type ContextConfigAgentSummary,
  type ContextConfigScopeContent,
  type ContextConnectorCredential,
  type ContextSceneKind,
  type ContextScopeType,
  type ContextSkillItem,
  type ContextWriteScopeType,
} from "@multica/core/context-capabilities";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { connectorBrandName } from "../common/connector-logo";
import { SkillIcon } from "../skills/lib/skill-icon";
import { useT } from "../i18n";
import { ConnectPlumbingContext, ConnectorsTiles, orgField, type OpenAuthorizeUrl } from "./context-config-apps";
import { ScopeMcpServers } from "./context-config-mcp";
import { ScopePrompts } from "./context-config-prompts";
import { ScopeRoutines } from "./context-config-routines";
import { ItemGroup, SlotHeading, ToggleControl } from "./context-config-ui";

/** One configurable scope of one agent: a chat, a person, or the
 * enterprise level of a tenant (its key is the OrgId). */
export interface ContextConfigScopeRef {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant (DingTalk org) of the scope; omitted or "" lets the server
   * choose. */
  orgId?: string;
}

/** The one scope a page opened from a configuration link shows: a chat
 * (`scene`, key = its scene_id; a group chat or a 1:1 chat) or a person
 * (`person`, key = staffId). The page then offers no way to browse or
 * switch to another agent, tenant or scope. */
export interface ContextConfigBinding {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
  /** Tenant of the scope; "" for the agent's own org. */
  orgId: string;
}

/** Where a connector OAuth round trip was started from. */
export interface ContextConfigConnectTarget extends ContextConfigScopeRef {
  agentId: string;
}

/** Outcome of a connector OAuth round trip, read by the platform from the
 * provider callback's redirect (`?connected=<slug>` / `?connect_error=<code>`). */
export type ContextConfigConnectResult =
  | { kind: "connected"; slug: string }
  | { kind: "error"; code: string };

export interface ContextConfigPickedGroup {
  /** Required by the server: only a picked chatId proves group membership. */
  chatId?: string;
  /** Optional cross-check; never enough on its own. */
  openConversationId?: string;
}

export interface ContextConfigPageProps {
  /** Agent-issued link token (`?link=`). Redeemed once on mount; a link to
   * a scope binds the page to it. */
  linkToken?: string;
  /** Preselected agent (`?agent=`), e.g. from the admin configure link. */
  initialAgentId?: string;
  /** The scope the page is bound to (kept in the URL after a link was
   * redeemed). A redeemed link's scope wins. */
  binding?: ContextConfigBinding | null;
  /** Called when a redeemed link binds the page, so the platform can keep
   * the binding across reloads and sign-in round trips. */
  onBind?: (binding: ContextConfigBinding) => void;
  /** Tab to open first (`?tab=`); an unknown id opens the default tab. */
  initialTab?: string;
  /** Called when the caller switches tabs, so the platform can keep it. */
  onTabChange?: (tab: ContextConfigTabId) => void;
  /**
   * DingTalk JSAPI group picker. Omitted outside the DingTalk client. It
   * resolves to null when the user cancels, and rejects with an error whose
   * `reloadRequired` is `true` when the picker cannot work again until the
   * page is reloaded (DingTalk applies `dd.config` once per page).
   */
  pickGroup?: () => Promise<ContextConfigPickedGroup | null>;
  /** Called when the API rejects the session; the platform signs in again. */
  onAuthRequired: () => void;
  /**
   * Sends the current page to a provider's authorization URL to connect an
   * OAuth connector (official apps). `target` is where the connection was
   * started, so the platform can reopen that scope when the provider
   * redirects back. Omitted → OAuth connectors offer no connect action.
   */
  openAuthorizeUrl?: OpenAuthorizeUrl;
  /** Path on the app origin the provider sign-in returns to (the bound
   * page's URL); omitted → the server's default page. */
  connectReturnTo?: string;
  /** Scope to open first, e.g. the one an OAuth round trip started from.
   * A redeemed link's scope wins. */
  initialScope?: ContextConfigScopeRef;
  /** Outcome of a connector OAuth round trip, shown once as a banner. */
  connectResult?: ContextConfigConnectResult | null;
}

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

/** What a top-level tab renders with. */
export interface ContextConfigTabProps {
  detail: ContextConfigAgentDetail;
  /** The bound scope; null while browsing. */
  binding: ContextConfigBinding | null;
  browse: ContextConfigBrowseState;
  reportError: (error: unknown) => boolean;
}

export interface ContextConfigTab {
  id: string;
  label: (t: AgentsT) => string;
  icon: LucideIcon;
  render: (props: ContextConfigTabProps) => React.ReactNode;
}

/**
 * The page's top-level tabs, in order; the first is the default. `?tab=<id>`
 * opens one directly (an id this page no longer has opens the default). A
 * new tab is one more entry here.
 */
export const CONTEXT_CONFIG_TABS = [
  {
    id: "scope",
    label: (t: AgentsT) => t(($) => $.context_config.tab_scope),
    icon: SlidersHorizontal,
    render: (props: ContextConfigTabProps) => <ScopeTab {...props} />,
  },
  {
    id: "routines",
    label: (t: AgentsT) => t(($) => $.context_config.tab_routines),
    icon: CalendarClock,
    render: (props: ContextConfigTabProps) => <RoutinesTab {...props} />,
  },
] as const satisfies readonly ContextConfigTab[];

export type ContextConfigTabId = (typeof CONTEXT_CONFIG_TABS)[number]["id"];

/** The tab `value` names, or the default tab. */
export function contextConfigTabId(value: string | null | undefined): ContextConfigTabId {
  return CONTEXT_CONFIG_TABS.find((tab) => tab.id === value)?.id ?? CONTEXT_CONFIG_TABS[0].id;
}

function isReloadRequired(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { reloadRequired?: unknown }).reloadRequired === true
  );
}

type RedeemStatus = "idle" | "pending" | "done" | "expired" | "taken" | "failed";

/** The page's levels: 企业能力, 当前会话 and 个人能力. */
type ConfigLevel = "org" | ContextScopeType;

type PreferredScope = ContextConfigScopeRef;

/** Browse mode's state (the open level is kept for a bound page too): the
 * open level, where to start, the group picker. */
export interface ContextConfigBrowseState {
  /** The open level; null until the caller picks one. */
  level: ConfigLevel | null;
  onLevelChange: (level: ConfigLevel) => void;
  /** The chosen scene; "" until the caller picks one. */
  sceneKey: string;
  onSceneKeyChange: (sceneKey: string) => void;
  preferredScope: PreferredScope | null;
  pickGroup?: () => Promise<ContextConfigPickedGroup | null>;
}

export function isContextConfigAuthError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  if (error.status === 401) return true;
  // RequireDingTalkHumanActor rejects non-DingTalk sessions with 403; any
  // other 403 is a grant decision and must not restart sign-in.
  return (
    error.status === 403 &&
    /dingtalk user authentication/i.test(error.message)
  );
}

function statusOf(error: unknown): number | null {
  return error instanceof ApiError ? error.status : null;
}

/**
 * Configuration page (DingTalk H5, also usable in a desktop browser). It
 * shows no page title or agent header, only the tabs: 场域能力 and 例行任务.
 * 场域能力 lists its levels as vertical tabs. A page opened from a
 * configuration link is bound to that link's scope: 当前会话 (a group chat
 * or a 1:1 chat, each its own scene), and for a personal link also 个人能力,
 * plus 企业能力 for the agent's managers. Opened without a scope (the admin
 * `?agent=` link) it browses the same levels over every chat. Each level
 * shows three slots: 指令, Skills and 连接器和插件. Platform-free: the web
 * route injects sign-in, the URL plumbing and the JSAPI group picker.
 */
export function ContextConfigPage({
  linkToken,
  initialAgentId,
  binding,
  onBind,
  initialTab,
  onTabChange,
  pickGroup,
  onAuthRequired,
  openAuthorizeUrl,
  connectReturnTo,
  initialScope,
  connectResult,
}: ContextConfigPageProps) {
  const { t } = useT("agents");
  const redeem = useRedeemContextConfigLink();
  const [redeemStatus, setRedeemStatus] = useState<RedeemStatus>(
    linkToken ? "pending" : "idle",
  );
  const [selectedAgentId, setSelectedAgentId] = useState(initialAgentId ?? "");
  const [bound, setBound] = useState<ContextConfigBinding | null>(binding ?? null);
  const [preferredScope, setPreferredScope] = useState<PreferredScope | null>(
    initialScope ?? null,
  );
  const [tab, setTab] = useState<ContextConfigTabId>(() => contextConfigTabId(initialTab));
  const redeemedToken = useRef<string | null>(null);
  const authRequested = useRef(false);

  const requireAuth = useRef(onAuthRequired);
  requireAuth.current = onAuthRequired;
  const bindRef = useRef(onBind);
  bindRef.current = onBind;
  const tabChangeRef = useRef(onTabChange);
  tabChangeRef.current = onTabChange;
  const reportError = (error: unknown): boolean => {
    if (!isContextConfigAuthError(error)) return false;
    if (!authRequested.current) {
      authRequested.current = true;
      requireAuth.current();
    }
    return true;
  };
  const reportErrorRef = useRef(reportError);
  reportErrorRef.current = reportError;

  const { mutateAsync: redeemLink } = redeem;
  useEffect(() => {
    if (!linkToken || redeemedToken.current === linkToken) return;
    redeemedToken.current = linkToken;
    setRedeemStatus("pending");
    redeemLink(linkToken)
      .then((result) => {
        setRedeemStatus("done");
        if (result.agentId && result.scopeType && result.scopeKey) {
          // A link to a scope binds the page to it.
          const next: ContextConfigBinding = {
            agentId: result.agentId,
            scopeType: result.scopeType,
            scopeKey: result.scopeKey,
            orgId: result.orgId || "",
          };
          setBound(next);
          bindRef.current?.(next);
          return;
        }
        if (result.agentId) setSelectedAgentId(result.agentId);
      })
      .catch((error: unknown) => {
        if (reportErrorRef.current(error)) {
          // Sign-in restarts the page; allow the same token to be redeemed
          // after it comes back.
          redeemedToken.current = null;
          return;
        }
        const status = statusOf(error);
        setRedeemStatus(status === 410 ? "expired" : status === 409 ? "taken" : "failed");
      });
  }, [linkToken, redeemLink]);

  const agentsQuery = useQuery({
    ...contextConfigAgentsOptions(),
    // A bound page never lists or switches agents.
    enabled: redeemStatus !== "pending" && bound === null,
  });

  useEffect(() => {
    if (agentsQuery.error) reportErrorRef.current(agentsQuery.error);
  }, [agentsQuery.error]);

  const agents = agentsQuery.data ?? [];
  const effectiveAgentId =
    bound?.agentId || selectedAgentId || (agents.length === 1 ? (agents[0]?.id ?? "") : "");

  const pickAgent = (agentId: string) => {
    setSelectedAgentId(agentId);
    setPreferredScope(null);
  };

  const changeTab = (next: ContextConfigTabId) => {
    setTab(next);
    tabChangeRef.current?.(next);
  };

  let body: React.ReactNode;
  if (redeemStatus === "pending" || (agentsQuery.isLoading && !effectiveAgentId)) {
    body = (
      <Status
        text={
          redeemStatus === "pending"
            ? t(($) => $.context_config.redeeming)
            : t(($) => $.context_config.loading)
        }
      />
    );
  } else if (effectiveAgentId) {
    body = (
      <AgentConfig
        key={effectiveAgentId}
        agentId={effectiveAgentId}
        binding={bound}
        // A preselected `?agent=` may be one the caller has no grant for, so
        // switching is offered whenever any other granted agent exists.
        canSwitchAgent={bound === null && agents.some((agent) => agent.id !== effectiveAgentId)}
        onSwitchAgent={() => pickAgent("")}
        preferredScope={preferredScope}
        pickGroup={pickGroup}
        tab={tab}
        onTabChange={changeTab}
        reportError={reportError}
      />
    );
  } else if (agentsQuery.isError) {
    body = (
      <ErrorState
        text={t(($) => $.context_config.load_failed)}
        onRetry={() => void agentsQuery.refetch()}
      />
    );
  } else if (agents.length === 0) {
    body = <NoAccessState />;
  } else {
    body = <AgentPicker agents={agents} onPick={pickAgent} />;
  }

  return (
    <main className="min-h-dvh bg-background px-4 py-4 text-foreground sm:px-6 sm:py-8">
      <div className="mx-auto flex w-full max-w-md flex-col gap-4 sm:max-w-2xl">
        {redeemStatus === "expired" && (
          <Banner>{t(($) => $.context_config.link_expired)}</Banner>
        )}
        {redeemStatus === "taken" && (
          <Banner>{t(($) => $.context_config.link_taken)}</Banner>
        )}
        {redeemStatus === "failed" && (
          <Banner>{t(($) => $.context_config.link_failed)}</Banner>
        )}
        {connectResult && <ConnectResultBanner result={connectResult} />}
        <ConnectPlumbingContext.Provider value={{ open: openAuthorizeUrl, returnTo: connectReturnTo }}>
          {body}
        </ConnectPlumbingContext.Provider>
      </div>
    </main>
  );
}

function ConnectResultBanner({ result }: { result: ContextConfigConnectResult }) {
  const { t } = useT("agents");
  if (result.kind === "connected") {
    return (
      <div
        className="flex items-start gap-2 rounded-lg border px-3 py-2.5 text-caption"
        role="status"
      >
        <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        <span>
          {t(($) => $.context_config.returned_connected, {
            name: connectorBrandName(result.slug),
          })}
        </span>
      </div>
    );
  }
  const message = (() => {
    switch (result.code) {
      case "access_denied":
        return t(($) => $.context_config.returned_denied);
      case "browser_mismatch":
        return t(($) => $.context_config.returned_browser_mismatch);
      default:
        return t(($) => $.context_config.returned_error);
    }
  })();
  return <Banner>{message}</Banner>;
}

function AgentPicker({
  agents,
  onPick,
}: {
  agents: ContextConfigAgentSummary[];
  onPick: (agentId: string) => void;
}) {
  const { t } = useT("agents");
  return (
    <section className="space-y-3" aria-labelledby="context-config-agents">
      <h2 id="context-config-agents" className="text-body font-medium">
        {t(($) => $.context_config.agents_title)}
      </h2>
      <ul className="space-y-2">
        {agents.map((agent) => (
          <li key={agent.id}>
            <button
              type="button"
              onClick={() => onPick(agent.id)}
              className="flex w-full items-center gap-3 rounded-lg border bg-card px-3 py-3 text-left transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <AgentAvatar name={agent.name} avatarUrl={agent.avatarUrl} />
              <span className="min-w-0 flex-1">
                <span className="flex min-w-0 items-center gap-1.5">
                  <span className="truncate text-body font-medium">{agent.name}</span>
                  {agent.access === "manager" && (
                    <Badge variant="secondary" className="shrink-0 text-micro">
                      {t(($) => $.context_config.manager_badge)}
                    </Badge>
                  )}
                </span>
                <span className="block truncate text-caption text-muted-foreground">
                  {[
                    ...(agent.access === "manager"
                      ? [t(($) => $.context_config.manager_all_scenes)]
                      : []),
                    ...agent.scopes
                      // A manager already reaches every scene; list the
                      // personal grant only.
                      .filter((scope) => agent.access !== "manager" || scope.scopeType === "person")
                      .map((scope) =>
                        scope.scopeType === "person"
                          ? t(($) => $.context_config.tab_person)
                          : scope.scopeTitle || t(($) => $.context_config.scene_untitled),
                      ),
                  ].join(" · ")}
                </span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function AgentConfig({
  agentId,
  binding,
  canSwitchAgent,
  onSwitchAgent,
  preferredScope,
  pickGroup,
  tab,
  onTabChange,
  reportError,
}: {
  agentId: string;
  binding: ContextConfigBinding | null;
  canSwitchAgent: boolean;
  onSwitchAgent: () => void;
  preferredScope: PreferredScope | null;
  pickGroup?: () => Promise<ContextConfigPickedGroup | null>;
  tab: ContextConfigTabId;
  onTabChange: (tab: ContextConfigTabId) => void;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  // The tenant the page shows: the bound scope's or a redeemed link's, else
  // the server's choice until the caller picks another one.
  const [orgId, setOrgId] = useState(binding ? binding.orgId : (preferredScope?.orgId ?? ""));
  // Browse state lives here, so it survives switching the top-level tab.
  const [level, setLevel] = useState<ConfigLevel | null>(null);
  const [sceneKey, setSceneKey] = useState("");
  const detailQuery = useQuery({
    ...contextConfigAgentOptions(agentId, orgId),
    // Switching the tenant keeps the page (and the open level) in place.
    placeholderData: keepPreviousData,
  });
  const detail = detailQuery.data ?? null;
  const reportErrorRef = useRef(reportError);
  reportErrorRef.current = reportError;

  useEffect(() => {
    if (detailQuery.error) reportErrorRef.current(detailQuery.error);
  }, [detailQuery.error]);

  // A tenant that is gone since the link was made: fall back to the
  // server's choice.
  useEffect(() => {
    if (orgId && statusOf(detailQuery.error) === 404) setOrgId("");
  }, [detailQuery.error, orgId]);

  if (detailQuery.isLoading) {
    return <Status text={t(($) => $.context_config.loading)} />;
  }
  if (detailQuery.isError || !detail) {
    const status = statusOf(detailQuery.error);
    if (status === 403 || status === 404) {
      return (
        <div className="space-y-3">
          <NoAccessState />
          {canSwitchAgent && <SwitchAgentButton onClick={onSwitchAgent} />}
        </div>
      );
    }
    return (
      <ErrorState
        text={t(($) => $.context_config.load_failed)}
        onRetry={() => void detailQuery.refetch()}
      />
    );
  }
  return (
    <AgentView
      detail={detail}
      binding={binding}
      canSwitchAgent={canSwitchAgent}
      onSwitchAgent={onSwitchAgent}
      onSelectTenant={setOrgId}
      browse={{
        level,
        onLevelChange: setLevel,
        sceneKey,
        onSceneKeyChange: setSceneKey,
        preferredScope,
        pickGroup,
      }}
      tab={tab}
      onTabChange={onTabChange}
      reportError={reportError}
    />
  );
}

/** The top-level tabs; while browsing, the tenant and agent switch above
 * them. The page shows no agent header. */
function AgentView({
  detail,
  binding,
  canSwitchAgent,
  onSwitchAgent,
  onSelectTenant,
  browse,
  tab,
  onTabChange,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  binding: ContextConfigBinding | null;
  canSwitchAgent: boolean;
  onSwitchAgent: () => void;
  onSelectTenant: (orgId: string) => void;
  browse: ContextConfigBrowseState;
  tab: ContextConfigTabId;
  onTabChange: (tab: ContextConfigTabId) => void;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const tenantOptions =
    detail.tenant && !detail.tenants.some((tenant) => tenant.orgId === detail.tenant?.orgId)
      ? [detail.tenant, ...detail.tenants]
      : detail.tenants;
  const active = CONTEXT_CONFIG_TABS.find((entry) => entry.id === tab) ?? CONTEXT_CONFIG_TABS[0];
  const pickTenant = binding === null && detail.tenant && tenantOptions.length > 1;

  return (
    <div className="space-y-4">
      {binding === null && (pickTenant || canSwitchAgent) ? (
        <div className="flex items-end gap-2">
          {pickTenant && detail.tenant ? (
            <label className="block min-w-0 flex-1 space-y-1.5">
              <span className="text-caption font-medium text-muted-foreground">
                {t(($) => $.context_config.tab_org)}
              </span>
              <NativeSelect
                className="w-full"
                value={detail.tenant.orgId}
                onChange={(event) => onSelectTenant(event.target.value)}
              >
                {tenantOptions.map((tenant) => (
                  <NativeSelectOption key={tenant.orgId} value={tenant.orgId}>
                    {tenant.name || tenant.orgId}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </label>
          ) : (
            <span className="flex-1" />
          )}
          {canSwitchAgent && <SwitchAgentButton onClick={onSwitchAgent} />}
        </div>
      ) : null}

      <Tabs
        value={active.id}
        onValueChange={(value) => {
          const next = CONTEXT_CONFIG_TABS.find((entry) => entry.id === value);
          if (next) onTabChange(next.id);
        }}
      >
        <TabsList variant="line" className="w-full" aria-label={t(($) => $.context_config.page_title)}>
          {CONTEXT_CONFIG_TABS.map((entry) => (
            <TabsTrigger key={entry.id} value={entry.id}>
              <entry.icon className="size-4" />
              {entry.label(t)}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      {active.render({ detail, binding, browse, reportError })}
    </div>
  );
}

/** 例行任务: the routines of the bound chat (for a personal link, the 1:1
 * chat it came from), or of the chat picked while browsing (the same pick as
 * 场域能力). A person or enterprise level has none. Who may change them
 * comes from the scene's rights. */
function RoutinesTab({ detail, binding, browse, reportError }: ContextConfigTabProps) {
  const { t } = useT("agents");
  const sceneKindLabel = useSceneKindLabel();
  const sceneUntitled = useSceneUntitled();
  const pageOrg = detail.tenant?.orgId ?? "";
  const boundSceneKey =
    binding?.scopeType === "scene"
      ? binding.scopeKey
      : binding?.scopeType === "person" && detail.person?.scopeKey === binding.scopeKey
        ? (boundDMScene(detail)?.scopeKey ?? "")
        : "";
  const wantedSceneKey = binding
    ? boundSceneKey
    : browse.sceneKey || (browse.preferredScope?.scopeType === "scene" ? browse.preferredScope.scopeKey : "");
  const scene =
    detail.scenes.find((entry) => entry.scopeKey === wantedSceneKey) ??
    (binding === null ? detail.scenes[0] : undefined);
  const sceneKey = binding ? boundSceneKey : (scene?.scopeKey ?? "");
  const orgId = scene?.orgId || pageOrg || (binding?.orgId ?? "");
  const sceneDetail = useQuery({
    ...contextConfigSceneOptions(detail.agent.id, sceneKey, orgId),
    enabled: Boolean(sceneKey),
  });

  if (!sceneKey) {
    return (
      <EmptyState
        icon={<CalendarClock className="size-6" />}
        title={t(($) => $.context_config.routines.not_scene_title)}
        hint={t(($) => $.context_config.routines.not_scene_hint)}
      />
    );
  }
  return (
    <div className="space-y-4">
      {binding === null && detail.scenes.length > 1 ? (
        <label className="block space-y-1.5">
          <span className="text-caption font-medium text-muted-foreground">
            {t(($) => $.context_config.scene_label)}
          </span>
          <NativeSelect
            className="w-full"
            value={sceneKey}
            onChange={(event) => browse.onSceneKeyChange(event.target.value)}
          >
            {detail.scenes.map((entry) => (
              <NativeSelectOption key={entry.scopeKey} value={entry.scopeKey}>
                {`${sceneKindLabel(entry.kind)} · ${entry.scopeTitle || sceneUntitled(entry.kind)}`}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </label>
      ) : null}
      <ScopeRoutines
        key={sceneKey}
        target={{ kind: "config", agentId: detail.agent.id, sceneId: sceneKey, orgId }}
        canEdit={sceneDetail.data?.rights?.editRoutines === true}
        reportError={reportError}
      />
    </div>
  );
}

/** One level of 场域能力 as a vertical tab. */
interface LevelEntry {
  id: ConfigLevel;
  label: string;
  icon: LucideIcon;
  render: () => React.ReactNode;
}

/** The level shown first: the one asked for (e.g. where an OAuth round trip
 * started), else 当前会话, else the first. */
function defaultLevel(levels: LevelEntry[], wanted: ConfigLevel | null | undefined): LevelEntry | undefined {
  return (
    levels.find((level) => level.id === wanted) ??
    levels.find((level) => level.id === "scene") ??
    levels[0]
  );
}

/** 场域能力's levels as vertical tabs: a compact rail (icon over label on a
 * phone) beside the open level. A single level shows without the rail. */
function LevelTabs({
  levels,
  value,
  onChange,
}: {
  levels: LevelEntry[];
  value: ConfigLevel | null;
  onChange: (level: ConfigLevel) => void;
}) {
  const { t } = useT("agents");
  const active = levels.find((level) => level.id === value) ?? levels[0];
  if (!active) return null;
  if (levels.length === 1) return <>{active.render()}</>;
  return (
    <Tabs
      orientation="vertical"
      value={active.id}
      onValueChange={(next) => {
        const level = levels.find((entry) => entry.id === next);
        if (level) onChange(level.id);
      }}
      className="items-start gap-3 sm:gap-5"
    >
      <TabsList
        aria-label={t(($) => $.context_config.levels_aria)}
        className="sticky top-3 w-[4.75rem] shrink-0 gap-1 sm:w-28"
      >
        {levels.map((level) => (
          <TabsTrigger
            key={level.id}
            value={level.id}
            className="h-auto w-full flex-col gap-1 px-1 py-2 text-caption whitespace-normal group-data-vertical/tabs:justify-center sm:flex-row sm:px-2 sm:text-body sm:group-data-vertical/tabs:justify-start"
          >
            <level.icon className="size-4" />
            <span className="min-w-0 break-words text-center leading-tight sm:text-left">{level.label}</span>
          </TabsTrigger>
        ))}
      </TabsList>
      <TabsContent value={active.id} className="min-w-0">
        {active.render()}
      </TabsContent>
    </Tabs>
  );
}

/** 场域能力: the bound scope's levels, or the browsable ones. */
function ScopeTab({ detail, binding, browse, reportError }: ContextConfigTabProps) {
  return binding ? (
    <BoundScope detail={detail} binding={binding} browse={browse} reportError={reportError} />
  ) : (
    <BrowseScopes detail={detail} browse={browse} reportError={reportError} />
  );
}

/** The 1:1 chat a personal link was minted in: redeeming the link also
 * granted that chat's scene, listed as the page's dm scene. */
function boundDMScene(detail: ContextConfigAgentDetail) {
  return detail.scenes.find((entry) => entry.kind === "dm");
}

/** The enterprise level as a vertical tab, when the server sends it (to the
 * agent's managers). A bound page also checks the caller manages the agent. */
function useOrgLevel(
  detail: ContextConfigAgentDetail,
  reportError: (error: unknown) => boolean,
  managersOnly: boolean,
): LevelEntry | null {
  const { t } = useT("agents");
  const org = detail.org;
  if (!org || (managersOnly && detail.access !== "manager")) return null;
  return {
    id: "org",
    label: t(($) => $.context_config.level_org),
    icon: Building2,
    render: () => <OrgScope detail={detail} org={org} reportError={reportError} />,
  };
}

/** A bound page: 当前会话 (the bound chat, or the 1:1 chat a personal link
 * came from), 个人能力 for a personal link, and 企业能力 for the agent's
 * managers. */
function BoundScope({
  detail,
  binding,
  browse,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  binding: ContextConfigBinding;
  browse: ContextConfigBrowseState;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const pageOrg = detail.tenant?.orgId ?? "";
  const orgLevel = useOrgLevel(detail, reportError, true);
  const levels: LevelEntry[] = orgLevel ? [orgLevel] : [];
  const sceneLabel = t(($) => $.context_config.level_scene);
  if (binding.scopeType === "scene") {
    const scene = detail.scenes.find((entry) => entry.scopeKey === binding.scopeKey);
    levels.push({
      id: "scene",
      label: sceneLabel,
      icon: MessageCircle,
      render: () => (
        <SceneScope
          agentId={detail.agent.id}
          sceneKey={binding.scopeKey}
          sceneKind={scene?.kind ?? "group"}
          // The detail's tenant follows the binding unless that tenant is
          // gone (the page then shows the server's choice).
          orgId={scene?.orgId || pageOrg || binding.orgId}
          detail={detail}
          reportError={reportError}
        />
      ),
    });
  } else if (detail.person && detail.person.scopeKey === binding.scopeKey) {
    const person = detail.person;
    const dm = boundDMScene(detail);
    if (dm) {
      levels.push({
        id: "scene",
        label: sceneLabel,
        icon: MessageCircle,
        render: () => (
          <SceneScope
            agentId={detail.agent.id}
            sceneKey={dm.scopeKey}
            sceneKind="dm"
            orgId={dm.orgId || pageOrg || binding.orgId}
            detail={detail}
            reportError={reportError}
          />
        ),
      });
    }
    levels.push({
      id: "person",
      label: t(($) => $.context_config.level_person),
      icon: User,
      render: () => <PersonScope detail={detail} person={person} orgId={pageOrg} reportError={reportError} />,
    });
  } else {
    levels.push({
      id: binding.scopeType,
      label: binding.scopeType === "person" ? t(($) => $.context_config.level_person) : sceneLabel,
      icon: binding.scopeType === "person" ? User : MessageCircle,
      render: () => (
        <EmptyState icon={<User className="size-6" />} title={t(($) => $.context_config.scene_no_access)} />
      ),
    });
  }
  const active = defaultLevel(levels, browse.level ?? browse.preferredScope?.scopeType);
  return <LevelTabs levels={levels} value={active?.id ?? null} onChange={browse.onLevelChange} />;
}

function OrgScope({
  detail,
  org,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  org: NonNullable<ContextConfigAgentDetail["org"]>;
  reportError: (error: unknown) => boolean;
}) {
  return (
    <ScopeEditor
      // Another tenant starts with fresh rows.
      key={org.scopeKey}
      agentId={detail.agent.id}
      scopeType="org"
      scopeKey={org.scopeKey}
      orgId={org.scopeKey}
      title={org.scopeTitle || detail.tenant?.name || org.scopeKey}
      expiresAt=""
      detail={detail}
      bindings={org.bindings}
      credentials={org.credentials}
      content={org}
      readOnly={org.canEdit !== true}
      reportError={reportError}
    />
  );
}

function PersonScope({
  detail,
  person,
  orgId,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  person: NonNullable<ContextConfigAgentDetail["person"]>;
  orgId: string;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  return (
    <ScopeEditor
      agentId={detail.agent.id}
      scopeType="person"
      scopeKey={person.scopeKey}
      orgId={orgId}
      title={person.scopeTitle || t(($) => $.context_config.level_person)}
      expiresAt={person.expiresAt}
      detail={detail}
      bindings={person.bindings}
      credentials={person.credentials}
      content={person}
      reportError={reportError}
    />
  );
}

/** A page without a bound scope: 企业能力 (managers), 当前会话 over the
 * scene list and the DingTalk group picker, and 个人能力. */
function BrowseScopes({
  detail,
  browse,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  browse: ContextConfigBrowseState;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const { level, onLevelChange, preferredScope, pickGroup, sceneKey, onSceneKeyChange: setSceneKey } = browse;
  const agentId = detail.agent.id;
  const isManager = detail.access === "manager";
  // Every scope on the page lives in this tenant ("" when the server names
  // none: the agent's own org).
  const pageOrg = detail.tenant?.orgId ?? "";
  // The picked scene, else the preferred one, else the first.
  const wantedSceneKey =
    sceneKey || (preferredScope?.scopeType === "scene" ? preferredScope.scopeKey : "");
  const activeSceneKey = detail.scenes.some((scene) => scene.scopeKey === wantedSceneKey)
    ? wantedSceneKey
    : (detail.scenes[0]?.scopeKey ?? "");

  const resolveScene = useResolveContextConfigScene(agentId);
  const sceneKindLabel = useSceneKindLabel();
  const sceneUntitled = useSceneUntitled();
  const orgLevel = useOrgLevel(detail, reportError, false);
  const [picking, setPicking] = useState(false);
  const canPickGroup =
    pickGroup !== undefined && detail.jsapiAvailable === true && detail.person !== null;

  const chooseGroup = async () => {
    if (!pickGroup) return;
    setPicking(true);
    try {
      const picked = await pickGroup();
      if (!picked?.chatId) return;
      const scene = await resolveScene.mutateAsync(pageOrg ? { ...picked, orgId: pageOrg } : picked);
      if (scene) {
        setSceneKey(scene.scopeKey);
        onLevelChange("scene");
      }
    } catch (error) {
      if (reportError(error)) return;
      const status = statusOf(error);
      toast.error(
        isReloadRequired(error)
          ? t(($) => $.context_config.pick_group_reload)
          : status === 403
            ? t(($) => $.context_config.pick_group_unknown)
            : status === 503
              ? t(($) => $.context_config.pick_group_unavailable)
              : t(($) => $.context_config.pick_group_failed),
      );
    } finally {
      setPicking(false);
    }
  };

  const pickGroupButton = canPickGroup ? (
    <Button
      variant="outline"
      className="h-10 w-full"
      disabled={picking}
      onClick={() => void chooseGroup()}
    >
      {picking ? (
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
      ) : (
        <Users className="size-4" />
      )}
      {t(($) => $.context_config.pick_group)}
    </Button>
  ) : null;

  const scenePanel =
    detail.scenes.length === 0 ? (
      <EmptyState
        icon={<Users className="size-6" />}
        title={
          isManager
            ? t(($) => $.context_config.scene_empty_manager_title)
            : t(($) => $.context_config.scene_empty_title)
        }
        hint={
          isManager
            ? t(($) => $.context_config.scene_empty_manager_hint)
            : t(($) => $.context_config.scene_empty_hint)
        }
      >
        {pickGroupButton}
      </EmptyState>
    ) : (
      <div className="space-y-4">
        {detail.scenes.length > 1 ? (
          <label className="block space-y-1.5">
            <span className="text-caption font-medium text-muted-foreground">
              {t(($) => $.context_config.scene_label)}
            </span>
            <NativeSelect
              className="w-full"
              value={activeSceneKey}
              onChange={(event) => setSceneKey(event.target.value)}
            >
              {detail.scenes.map((scene) => (
                <NativeSelectOption key={scene.scopeKey} value={scene.scopeKey}>
                  {`${sceneKindLabel(scene.kind)} · ${scene.scopeTitle || sceneUntitled(scene.kind)}`}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </label>
        ) : null}
        <SceneScope
          key={activeSceneKey}
          agentId={agentId}
          sceneKey={activeSceneKey}
          sceneKind={detail.scenes.find((scene) => scene.scopeKey === activeSceneKey)?.kind ?? "group"}
          orgId={detail.scenes.find((scene) => scene.scopeKey === activeSceneKey)?.orgId || pageOrg}
          detail={detail}
          reportError={reportError}
        />
        {pickGroupButton}
      </div>
    );

  const personPanel = detail.person ? (
    <PersonScope detail={detail} person={detail.person} orgId={pageOrg} reportError={reportError} />
  ) : (
    <EmptyState
      icon={<MessageCircle className="size-6" />}
      title={t(($) => $.context_config.person_empty_title)}
      hint={t(($) => $.context_config.person_empty_hint)}
    >
      {/* Managing the agent covers its scenes, 1:1 chats included, never
          another person's personal level or accounts. */}
      {isManager && (
        <p className="text-caption text-muted-foreground text-pretty">
          {t(($) => $.context_config.person_manager_note)}
        </p>
      )}
    </EmptyState>
  );

  const levels: LevelEntry[] = [
    ...(orgLevel ? [orgLevel] : []),
    { id: "scene", label: t(($) => $.context_config.level_scene), icon: MessageCircle, render: () => scenePanel },
    { id: "person", label: t(($) => $.context_config.level_person), icon: User, render: () => personPanel },
  ];
  const wanted: ConfigLevel | undefined =
    level ??
    preferredScope?.scopeType ??
    (detail.scenes.length === 0 && detail.person ? "person" : "scene");
  return <LevelTabs levels={levels} value={defaultLevel(levels, wanted)?.id ?? null} onChange={onLevelChange} />;
}

function SceneScope({
  agentId,
  sceneKey,
  sceneKind,
  orgId,
  detail,
  reportError,
}: {
  agentId: string;
  sceneKey: string;
  /** Kind from the agent detail; the scene detail's own kind wins. */
  sceneKind: ContextSceneKind;
  /** Tenant of the scene; "" is the agent's own org. */
  orgId: string;
  detail: ContextConfigAgentDetail;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const sceneUntitled = useSceneUntitled();
  const sceneQuery = useQuery(contextConfigSceneOptions(agentId, sceneKey, orgId));
  const reportErrorRef = useRef(reportError);
  reportErrorRef.current = reportError;

  useEffect(() => {
    if (sceneQuery.error) reportErrorRef.current(sceneQuery.error);
  }, [sceneQuery.error]);

  if (sceneQuery.isLoading) {
    return <Status text={t(($) => $.context_config.loading)} />;
  }
  const scene = sceneQuery.data ?? null;
  if (sceneQuery.isError || !scene) {
    const status = statusOf(sceneQuery.error);
    return (
      <ErrorState
        text={
          status === 403 || status === 404
            ? t(($) => $.context_config.scene_no_access)
            : t(($) => $.context_config.load_failed)
        }
        onRetry={() => void sceneQuery.refetch()}
      />
    );
  }
  // An older scene detail omits the kind (parsed as "group"); the agent
  // detail may already know the chat is a 1:1 chat.
  const kind: ContextSceneKind = scene.scene.kind === "dm" || sceneKind === "dm" ? "dm" : "group";
  // A group chat and a 1:1 chat alike are their own scene: the scene_id is
  // the scope of every write here, and the server's rights say what the
  // caller may change.
  return (
    <ScopeEditor
      agentId={agentId}
      scopeType="scene"
      scopeKey={scene.scene.scopeKey}
      orgId={scene.scene.orgId || orgId}
      sceneKind={kind}
      title={scene.scene.scopeTitle || sceneUntitled(kind)}
      expiresAt={scene.scene.expiresAt}
      detail={detail}
      bindings={scene.bindings}
      credentials={scene.credentials}
      content={scene}
      // An older backend without rights: its can_connect alone hides
      // connecting.
      ownerOnly={scene.canConnect === false}
      reportError={reportError}
    />
  );
}

/**
 * One level (企业能力, 当前会话 or 个人能力): its name, then three slots in
 * order. 指令 are the level's prompt components; Skills are the agent's own
 * (on by default) and the ones the level can add; 连接器和插件 are the apps
 * and connectors (added first, then authorized) and the level's own MCP
 * servers. Which bundle an item comes from is not shown.
 */
function ScopeEditor({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind = "group",
  title,
  expiresAt,
  detail,
  bindings,
  credentials,
  content,
  ownerOnly = false,
  readOnly = false,
  reportError,
}: {
  agentId: string;
  scopeType: ConfigLevel;
  scopeKey: string;
  /** Tenant of the scope; "" is the agent's own org. */
  orgId: string;
  /** Scene scopes only: a group chat or a 1:1 chat. */
  sceneKind?: ContextSceneKind;
  title: string;
  expiresAt: string;
  detail: ContextConfigAgentDetail;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
  /** The scope's rights, prompts and MCP servers. */
  content: ContextConfigScopeContent;
  /** Older backends (no `rights`): the caller may switch things here but
   * not connect accounts or store tokens (the server's can_connect). */
  ownerOnly?: boolean;
  /** Older backends (no `rights`): nothing can be changed here (the
   * enterprise level for a member). */
  readOnly?: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const sceneKindLabel = useSceneKindLabel();
  const setBinding = useSetContextCapabilityBinding(agentId);
  // The server's rights decide; an older backend sends none and the page
  // keeps its own reading of who may change what.
  const rights = content.rights;
  const canToggle = rights ? rights.toggle : !readOnly;
  const canConnect = rights ? rights.connect : !readOnly && !ownerOnly;
  // Accounts are shown, but who connects them is said only when the rest of
  // the scope stays editable (switching allowed, connecting not).
  const credentialReadOnly = !canConnect && !canToggle;
  const credentialOwnerOnly = !canConnect && canToggle;
  const scopeInput = { scopeType, scopeKey, ...orgField(orgId) };
  // One entry per row with a write in flight, so overlapping toggles of
  // different rows never clear each other's pending state.
  const [busyKeys, setBusyKeys] = useState<ReadonlySet<string>>(() => new Set());

  const enabledKeys = useMemo(
    () =>
      new Set(
        bindings
          .filter((binding) => binding.enabled === true)
          .map((binding) => `${binding.resourceType}:${binding.resourceId}`),
      ),
    [bindings],
  );
  // Skills the enterprise level turned on: they apply here whatever this
  // level's switch says (switches add up across levels).
  const orgEnabledKeys = useMemo(
    () => new Set(scopeType === "org" ? [] : (detail.orgEffect?.skillIds ?? []).map((id) => `skill:${id}`)),
    [detail.orgEffect, scopeType],
  );

  const markBusy = (key: string, busy: boolean) =>
    setBusyKeys((current) => {
      const next = new Set(current);
      if (busy) next.add(key);
      else next.delete(key);
      return next;
    });

  const toggle = async (resourceType: "connector" | "skill", resourceId: string, enabled: boolean) => {
    const key = `${resourceType}:${resourceId}`;
    if (!canToggle || !resourceId || busyKeys.has(key)) return;
    markBusy(key, true);
    try {
      await setBinding.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), resourceType, resourceId, enabled });
    } catch (error) {
      if (!reportError(error)) {
        toast.error(t(($) => $.context_config.toggle_failed));
      }
    } finally {
      markBusy(key, false);
    }
  };

  // Person connectors only: may this connector also serve group chats the
  // person triggers? The binding stays enabled either way.
  const toggleShare = async (resourceId: string, shareInGroups: boolean) => {
    const key = `share:${resourceId}`;
    if (!resourceId || busyKeys.has(key)) return;
    markBusy(key, true);
    try {
      await setBinding.mutateAsync({
        scopeType: "person",
        scopeKey,
        ...orgField(orgId),
        resourceType: "connector",
        resourceId,
        enabled: true,
        shareInGroups,
      });
    } catch (error) {
      if (!reportError(error)) {
        toast.error(t(($) => $.context_config.share_in_groups_failed));
      }
    } finally {
      markBusy(key, false);
    }
  };

  // The agent's own skills first (on everywhere), then the ones this level
  // can add.
  const defaultSkillIds = new Set(detail.global.skills.map((skill) => skill.id));
  const skills: { skill: ContextSkillItem; defaultOn: boolean }[] = [
    ...detail.global.skills.map((skill) => ({ skill, defaultOn: true })),
    ...detail.offers.skills
      .filter((skill) => !defaultSkillIds.has(skill.id))
      .map((skill) => ({ skill, defaultOn: false })),
  ];
  const expiry = formatDate(expiresAt);
  const hint =
    scopeType === "org"
      ? !canToggle
        ? t(($) => $.context_config.org_read_only)
        : t(($) => $.context_config.org_scope_hint)
      : rights && !canToggle
        ? scopeType === "scene"
          ? sceneKind === "dm"
            ? t(($) => $.context_config.scene_read_only_dm)
            : t(($) => $.context_config.scene_read_only)
          : t(($) => $.context_config.person_read_only)
        : scopeType === "scene"
          ? sceneKind === "dm"
            ? t(($) => $.context_config.scene_scope_hint_dm)
            : t(($) => $.context_config.scene_scope_hint)
          : t(($) => $.context_config.person_scope_hint);

  return (
    <section className="space-y-6" aria-label={title}>
      <div className="space-y-1">
        <div className="flex min-w-0 items-center gap-2">
          <p className="truncate text-body font-medium">{title}</p>
          {scopeType === "scene" && (
            <Badge variant="outline" className="shrink-0">
              {sceneKindLabel(sceneKind)}
            </Badge>
          )}
        </div>
        <p className="text-caption text-muted-foreground text-pretty">{hint}</p>
        {expiry && (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.context_config.access_until, { date: expiry })}
          </p>
        )}
      </div>

      {/* Prompts need a backend that reports rights. */}
      {rights ? (
        <ScopePrompts
          agentId={agentId}
          scope={scopeInput}
          prompts={content.prompts}
          canEdit={rights.editPrompts}
          reportError={reportError}
        />
      ) : null}

      <ItemGroup label={t(($) => $.context_config.skills_title)} size="slot" empty={t(($) => $.context_config.none)}>
        {skills.map(({ skill, defaultOn }) => (
          <SkillRow
            key={skill.id}
            skill={skill}
            enabled={enabledKeys.has(`skill:${skill.id}`)}
            alwaysOn={defaultOn}
            byOrg={orgEnabledKeys.has(`skill:${skill.id}`)}
            busy={busyKeys.has(`skill:${skill.id}`)}
            readOnly={!canToggle}
            onToggle={(enabled) => void toggle("skill", skill.id, enabled)}
          />
        ))}
      </ItemGroup>

      <section className="space-y-2" aria-label={t(($) => $.context_config.slot_connectors)}>
        <SlotHeading label={t(($) => $.context_config.slot_connectors)} />
        <ConnectorsTiles
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          sceneKind={sceneKind}
          detail={detail}
          bindings={bindings}
          credentials={credentials}
          canToggle={canToggle}
          ownerOnly={credentialOwnerOnly}
          credentialReadOnly={credentialReadOnly}
          isBusy={(key) => busyKeys.has(key)}
          onToggle={(connectorId, enabled) => void toggle("connector", connectorId, enabled)}
          onToggleShare={(connectorId, shareInGroups) => void toggleShare(connectorId, shareInGroups)}
          reportError={reportError}
        />
        {/* MCP servers need a backend that reports rights. */}
        {rights ? (
          <ScopeMcpServers
            agentId={agentId}
            scope={scopeInput}
            mcpConfig={content.mcpConfig}
            redacted={content.mcpConfigRedacted}
            canEdit={rights.editMcp}
            reportError={reportError}
          />
        ) : null}
      </section>
    </section>
  );
}

function SkillRow({
  skill,
  enabled,
  alwaysOn,
  byOrg,
  busy,
  readOnly,
  onToggle,
}: {
  skill: ContextSkillItem;
  enabled: boolean;
  alwaysOn: boolean;
  /** Turned on at the enterprise level. */
  byOrg: boolean;
  busy: boolean;
  readOnly: boolean;
  onToggle: (enabled: boolean) => void;
}) {
  const { t } = useT("agents");
  return (
    <li className="flex items-center gap-3 p-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
        <SkillIcon className="size-4" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate text-body font-medium">{skill.name}</span>
          {alwaysOn && (
            <Badge variant="secondary" className="text-micro">
              {t(($) => $.context_config.always_on)}
            </Badge>
          )}
          {byOrg && !alwaysOn && (
            <Badge variant="secondary" className="text-micro">
              {t(($) => $.context_config.on_for_org)}
            </Badge>
          )}
        </div>
        {skill.description ? (
          <p className="line-clamp-2 text-caption text-muted-foreground">{skill.description}</p>
        ) : null}
      </div>
      {!alwaysOn && (
        <ToggleControl
          busy={busy}
          checked={enabled}
          disabled={readOnly}
          label={t(($) => $.context_config.toggle_aria, { name: skill.name })}
          onToggle={onToggle}
        />
      )}
    </li>
  );
}

function NoAccessState() {
  const { t } = useT("agents");
  return (
    <EmptyState
      icon={<MessageCircle className="size-6" />}
      title={t(($) => $.context_config.no_access_title)}
      hint={t(($) => $.context_config.no_access_hint)}
    >
      <ul className="w-full space-y-1.5 text-left text-caption text-muted-foreground">
        <li className="flex gap-2">
          <Users className="mt-0.5 size-3.5 shrink-0" />
          <span>{t(($) => $.context_config.scene_empty_hint)}</span>
        </li>
        <li className="flex gap-2">
          <User className="mt-0.5 size-3.5 shrink-0" />
          <span>{t(($) => $.context_config.person_empty_hint)}</span>
        </li>
      </ul>
    </EmptyState>
  );
}

function SwitchAgentButton({ onClick }: { onClick: () => void }) {
  const { t } = useT("agents");
  return (
    <Button variant="ghost" size="sm" onClick={onClick}>
      <ArrowLeftRight className="size-3.5" />
      {t(($) => $.context_config.switch_agent)}
    </Button>
  );
}

function AgentAvatar({ name, avatarUrl }: { name: string; avatarUrl: string | null }) {
  const [failed, setFailed] = useState(false);
  const initial = name.trim().charAt(0).toUpperCase();
  return (
    <span className="flex size-10 shrink-0 items-center justify-center overflow-hidden rounded-full bg-muted text-body font-medium text-muted-foreground">
      {avatarUrl && !failed ? (
        <img
          src={avatarUrl}
          alt=""
          className="size-full object-cover"
          onError={() => setFailed(true)}
        />
      ) : (
        initial
      )}
    </span>
  );
}

function EmptyState({
  icon,
  title,
  hint,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  hint?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-8 text-center">
      <span className="text-faint-foreground">{icon}</span>
      <div className="space-y-1">
        <p className="text-body font-medium">{title}</p>
        {hint ? <p className="text-caption text-muted-foreground text-pretty">{hint}</p> : null}
      </div>
      {children}
    </div>
  );
}

function Status({ text }: { text: string }) {
  return (
    <div className="flex flex-col items-center gap-3 py-10 text-center" role="status">
      <Loader2 className="size-6 animate-spin text-muted-foreground motion-reduce:animate-none" />
      <p className="text-body text-muted-foreground">{text}</p>
    </div>
  );
}

function ErrorState({ text, onRetry }: { text: string; onRetry: () => void }) {
  const { t } = useT("agents");
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-8 text-center">
      <AlertCircle className="size-6 text-destructive" />
      <p className="text-body text-muted-foreground">{text}</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        {t(($) => $.context_config.retry)}
      </Button>
    </div>
  );
}

function Banner({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border px-3 py-2.5 text-caption text-muted-foreground" role="alert">
      <AlertCircle className="mt-0.5 size-3.5 shrink-0" />
      <span>{children}</span>
    </div>
  );
}

function useSceneKindLabel(): (kind: ContextSceneKind) => string {
  const { t } = useT("agents");
  return (kind) =>
    kind === "dm" ? t(($) => $.context_config.kind_dm) : t(($) => $.context_config.kind_group);
}

function useSceneUntitled(): (kind: ContextSceneKind) => string {
  const { t } = useT("agents");
  return (kind) =>
    kind === "dm"
      ? t(($) => $.context_config.scene_untitled_dm)
      : t(($) => $.context_config.scene_untitled);
}

function formatDate(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString();
}
