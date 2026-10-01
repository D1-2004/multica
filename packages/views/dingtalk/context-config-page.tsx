"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  ArrowLeftRight,
  Building2,
  CheckCircle2,
  ExternalLink,
  Globe,
  KeyRound,
  Link2,
  Loader2,
  MessageCircle,
  Plug,
  SlidersHorizontal,
  User,
  Users,
  type LucideIcon,
} from "lucide-react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError, errorCode } from "@multica/core/api";
import {
  contextConfigAgentOptions,
  contextConfigAgentsOptions,
  contextConfigSceneOptions,
  useDeleteContextConnectorCredential,
  useRedeemContextConfigLink,
  useResolveContextConfigScene,
  useSetContextCapabilityBinding,
  useSetContextConnectorCredential,
  useStartContextConnectorConnection,
  type ContextCapabilityBinding,
  type ContextConfigAgentDetail,
  type ContextConfigAgentSummary,
  type ContextConfigScopeContent,
  type ContextConnectorCredential,
  type ContextOfferedConnector,
  type ContextSceneKind,
  type ContextScopeType,
  type ContextSkillItem,
  type ContextWriteScopeType,
} from "@multica/core/context-capabilities";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { cn } from "@multica/ui/lib/utils";
import { ConnectorLogo, connectorBrandName } from "../common/connector-logo";
import { MAX_BEARER_LENGTH, isValidBearer, useResetOnBackForwardRestore } from "../common/connector-credential";
import { SkillIcon } from "../skills/lib/skill-icon";
import { useT } from "../i18n";
import { ScopeMcpServers } from "./context-config-mcp";
import { ScopePrompts } from "./context-config-prompts";
import { PublicCapabilities } from "./context-config-public";
import { ItemGroup, ToggleControl } from "./context-config-ui";

/** One configurable scope of one agent: a chat, a person, or the
 * enterprise level of a tenant (its key is the OrgId). */
export interface ContextConfigScopeRef {
  scopeType: ContextWriteScopeType;
  scopeKey: string;
  /** Tenant (DingTalk org) of the scope; omitted or "" lets the server
   * choose. */
  orgId?: string;
}

/** The one scope a page opened from a configuration link shows: a group
 * chat (`scene`) or a person (`person`; a 1:1 chat is its person). The
 * page then offers no way to browse or switch to another agent, tenant or
 * scope. */
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

type OpenAuthorizeUrl = (url: string, target: ContextConfigConnectTarget) => void;

interface ConnectPlumbing {
  open?: OpenAuthorizeUrl;
  /** Where the provider sign-in returns; undefined → the server's default. */
  returnTo?: string;
}

// Platform plumbing the connect buttons use. Kept in context so the scope
// editors do not drill it through every level.
const ConnectPlumbingContext = createContext<ConnectPlumbing>({});

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
 * opens one directly. A new tab (例行任务, ...) is one more entry here.
 */
export const CONTEXT_CONFIG_TABS = [
  {
    id: "scope",
    label: (t: AgentsT) => t(($) => $.context_config.tab_scope),
    icon: SlidersHorizontal,
    render: (props: ContextConfigTabProps) => <ScopeTab {...props} />,
  },
  {
    id: "public",
    label: (t: AgentsT) => t(($) => $.context_config.tab_public),
    icon: Globe,
    render: ({ detail }: ContextConfigTabProps) => <PublicCapabilities detail={detail} />,
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

/** The page's levels: 企业, 本会话 and 我的. */
type ConfigLevel = "org" | ContextScopeType;

/** `org_id` of a scope request: only for a tenant other than the agent's
 * own org, which is the server's default. */
function orgField(orgId: string): { orgId?: string } {
  return orgId ? { orgId } : {};
}

type PreferredScope = ContextConfigScopeRef;

/** Browse mode's state: the open level, where to start, the group picker. */
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
 * Configuration page (DingTalk H5, also usable in a desktop browser). A page
 * opened from a configuration link is bound to that link's scope: a group
 * chat, or a person (a 1:1 chat is its person), plus the enterprise level
 * for the agent's managers. Opened without a scope (the admin `?agent=`
 * link) it browses three levels: 企业 (the tenant: edited by the agent's
 * managers), 本会话 (a group chat or a 1:1 chat) and 我的. Platform-free: the
 * web route injects sign-in, the URL plumbing and the JSAPI group picker.
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
    <main className="min-h-dvh bg-background px-4 py-6 text-foreground sm:px-6 sm:py-10">
      <div className="mx-auto flex w-full max-w-md flex-col gap-5 sm:max-w-2xl">
        <header>
          <h1 className="text-title font-semibold text-balance">
            {t(($) => $.context_config.page_title)}
          </h1>
        </header>
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

/** The agent's header (and, while browsing, its tenant) above the
 * top-level tabs. */
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
  // The detail says whether the caller manages this agent (every scene is
  // then configurable), so the hint does not wait for, or depend on, the
  // agent list.
  const isManager = detail.access === "manager";
  const tenantOptions =
    detail.tenant && !detail.tenants.some((tenant) => tenant.orgId === detail.tenant?.orgId)
      ? [detail.tenant, ...detail.tenants]
      : detail.tenants;
  const active = CONTEXT_CONFIG_TABS.find((entry) => entry.id === tab) ?? CONTEXT_CONFIG_TABS[0];

  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <div className="flex items-center gap-3">
          <AgentAvatar name={detail.agent.name} avatarUrl={detail.agent.avatarUrl} />
          <div className="flex min-w-0 flex-1 items-center gap-2">
            <p className="truncate text-body-lg font-medium">{detail.agent.name}</p>
            {isManager && (
              <Badge variant="secondary" className="shrink-0">
                {t(($) => $.context_config.manager_badge)}
              </Badge>
            )}
          </div>
          {canSwitchAgent && <SwitchAgentButton onClick={onSwitchAgent} />}
        </div>
        {isManager && binding === null && (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.context_config.manager_hint)}
          </p>
        )}
        {binding === null && detail.tenant && tenantOptions.length > 1 ? (
          <label className="block space-y-1.5">
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
        ) : null}
      </div>

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

/** 场域能力: the bound scope, or the three browsable levels. */
function ScopeTab({ detail, binding, browse, reportError }: ContextConfigTabProps) {
  return binding ? (
    <BoundScope detail={detail} binding={binding} reportError={reportError} />
  ) : (
    <BrowseScopes detail={detail} browse={browse} reportError={reportError} />
  );
}

/** A bound page: only the bound group chat or person, and the enterprise
 * level for the agent's managers. */
function BoundScope({
  detail,
  binding,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  binding: ContextConfigBinding;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const pageOrg = detail.tenant?.orgId ?? "";
  let scope: React.ReactNode;
  if (binding.scopeType === "scene") {
    const scene = detail.scenes.find((entry) => entry.scopeKey === binding.scopeKey);
    scope = (
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
    );
  } else if (detail.person && detail.person.scopeKey === binding.scopeKey) {
    scope = <PersonScope detail={detail} person={detail.person} orgId={pageOrg} reportError={reportError} />;
  } else {
    scope = (
      <EmptyState icon={<User className="size-6" />} title={t(($) => $.context_config.scene_no_access)} />
    );
  }
  return (
    <div className="space-y-8">
      {scope}
      {detail.access === "manager" && detail.org ? (
        <section className="space-y-3" aria-labelledby="context-config-org-section">
          <h2 id="context-config-org-section" className="text-body font-semibold">
            {t(($) => $.context_config.org_section_title)}
          </h2>
          <OrgScope detail={detail} org={detail.org} reportError={reportError} />
        </section>
      ) : null}
    </div>
  );
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
      title={person.scopeTitle || t(($) => $.context_config.tab_person)}
      expiresAt={person.expiresAt}
      detail={detail}
      bindings={person.bindings}
      credentials={person.credentials}
      content={person}
      reportError={reportError}
    />
  );
}

/** A page without a bound scope: 企业 / 本会话 / 我的, the scene list and
 * the DingTalk group picker. */
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
  const tab: ConfigLevel =
    level ??
    preferredScope?.scopeType ??
    (detail.scenes.length === 0 && detail.person ? "person" : "scene");
  const setTab = onLevelChange;
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
        setTab("scene");
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

  return (
    <div className="space-y-5">
      <Tabs
        value={tab}
        onValueChange={(value) => {
          if (value === "org" || value === "scene" || value === "person") setTab(value);
        }}
        className="gap-4"
      >
        <TabsList className="!h-10 w-full">
          <TabsTrigger value="org">
            <Building2 className="size-4" />
            {t(($) => $.context_config.tab_org)}
          </TabsTrigger>
          <TabsTrigger value="scene">
            <Users className="size-4" />
            {t(($) => $.context_config.tab_scene)}
          </TabsTrigger>
          <TabsTrigger value="person">
            <User className="size-4" />
            {t(($) => $.context_config.tab_person)}
          </TabsTrigger>
        </TabsList>
      </Tabs>

      {tab === "org" ? (
        detail.org ? (
          <OrgScope detail={detail} org={detail.org} reportError={reportError} />
        ) : (
          <EmptyState
            icon={<Building2 className="size-6" />}
            title={t(($) => $.context_config.org_unavailable)}
          />
        )
      ) : tab === "scene" ? (
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
                      {`${sceneKindLabel(scene.kind)} · ${
                        scene.scopeTitle || sceneUntitled(scene.kind)
                      }`}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </label>
            ) : null}
            <SceneScope
              key={activeSceneKey}
              agentId={agentId}
              sceneKey={activeSceneKey}
              sceneKind={
                detail.scenes.find((scene) => scene.scopeKey === activeSceneKey)?.kind ?? "group"
              }
              orgId={detail.scenes.find((scene) => scene.scopeKey === activeSceneKey)?.orgId || pageOrg}
              detail={detail}
              reportError={reportError}
            />
            {pickGroupButton}
          </div>
        )
      ) : detail.person ? (
        <PersonScope detail={detail} person={detail.person} orgId={pageOrg} reportError={reportError} />
      ) : (
        <EmptyState
          icon={<MessageCircle className="size-6" />}
          title={t(($) => $.context_config.person_empty_title)}
          hint={t(($) => $.context_config.person_empty_hint)}
        >
          {/* Managing the agent covers its scenes (a 1:1 chat's switches
              are its person's), never another person's accounts. */}
          {isManager && (
            <p className="text-caption text-muted-foreground text-pretty">
              {t(($) => $.context_config.person_manager_note)}
            </p>
          )}
        </EmptyState>
      )}
    </div>
  );
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
  // A 1:1 chat's configuration is its person's configuration; the server
  // cannot always tell who that is yet.
  if (scene.scope === null) {
    return (
      <EmptyState
        icon={<MessageCircle className="size-6" />}
        title={t(($) => $.context_config.dm_person_unknown)}
      />
    );
  }
  const person = scene.scope?.type === "person" ? scene.scope : null;
  // A manager may switch things on in someone's 1:1 chat but never store or
  // connect that person's account (the server answers 403); the person's
  // own grant (their 1:1 chat link, or their personal link) may. Either the
  // server's can_connect or the manager-in-a-1:1-chat shape hides connecting.
  const ownerOnly =
    scene.canConnect === false ||
    (kind === "dm" &&
      scene.scene.source === "manager" &&
      !(person !== null && detail.person?.scopeKey === person.key));
  return (
    <ScopeEditor
      agentId={agentId}
      scopeType="scene"
      scopeKey={scene.scene.scopeKey}
      orgId={scene.scene.orgId || orgId}
      sceneKind={kind}
      title={person?.title || scene.scene.scopeTitle || sceneUntitled(kind)}
      expiresAt={scene.scene.expiresAt}
      detail={detail}
      bindings={scene.bindings}
      credentials={scene.credentials}
      content={scene}
      ownerOnly={ownerOnly}
      reportError={reportError}
    />
  );
}

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
  /** Older backends (no `rights`): only the person connects accounts and
   * tokens here (a manager viewing someone's 1:1 chat). */
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
  // the scope stays editable (a manager in someone's 1:1 chat).
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
  const sharedConnectorIds = useMemo(
    () =>
      new Set(
        bindings
          .filter(
            (binding) =>
              binding.resourceType === "connector" &&
              binding.enabled === true &&
              binding.shareInGroups === true,
          )
          .map((binding) => binding.resourceId),
      ),
    [bindings],
  );
  const globalIds = useMemo(
    () =>
      new Set([
        ...detail.global.connectors.map((connector) => connector.id),
        ...detail.global.skills.map((skill) => skill.id),
      ]),
    [detail.global.connectors, detail.global.skills],
  );
  const credentialByConnector = useMemo(
    () => new Map(credentials.map((credential) => [credential.connectorId, credential])),
    [credentials],
  );
  // Items the enterprise level turned on: they apply here whatever this
  // level's switch says (switches add up across levels).
  const orgEnabledKeys = useMemo(
    () =>
      new Set(
        scopeType === "org"
          ? []
          : (detail.org?.bindings ?? [])
              .filter((binding) => binding.enabled === true)
              .map((binding) => `${binding.resourceType}:${binding.resourceId}`),
      ),
    [detail.org, scopeType],
  );

  const toggle = async (
    resourceType: "connector" | "skill",
    resourceId: string,
    enabled: boolean,
  ) => {
    const key = `${resourceType}:${resourceId}`;
    if (!canToggle || busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
    try {
      await setBinding.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), resourceType, resourceId, enabled });
    } catch (error) {
      if (!reportError(error)) {
        toast.error(t(($) => $.context_config.toggle_failed));
      }
    } finally {
      setBusyKeys((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    }
  };

  // Person connectors only: may this connector also serve group chats the
  // person triggers? The binding stays enabled either way.
  const toggleShare = async (resourceId: string, shareInGroups: boolean) => {
    const key = `share:${resourceId}`;
    if (busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
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
      setBusyKeys((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    }
  };

  const offers = detail.offers;
  const expiry = formatDate(expiresAt);

  return (
    <section className="space-y-4" aria-label={title}>
      <div className="space-y-1">
        <div className="flex min-w-0 items-center gap-2">
          <p className="truncate text-body font-medium">{title}</p>
          {scopeType === "scene" && (
            <Badge variant="outline" className="shrink-0">
              {sceneKindLabel(sceneKind)}
            </Badge>
          )}
        </div>
        <p className="text-caption text-muted-foreground">
          {scopeType === "org"
            ? !canToggle
              ? t(($) => $.context_config.org_read_only)
              : t(($) => $.context_config.org_scope_hint)
            : rights && !canToggle
              ? scopeType === "scene" && sceneKind !== "dm"
                ? t(($) => $.context_config.scene_read_only)
                : t(($) => $.context_config.person_read_only)
              : scopeType === "scene"
                ? sceneKind === "dm"
                  ? t(($) => $.context_config.scene_scope_hint_dm)
                  : t(($) => $.context_config.scene_scope_hint)
                : t(($) => $.context_config.person_scope_hint)}
        </p>
        {expiry && (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.context_config.access_until, { date: expiry })}
          </p>
        )}
      </div>

      {offers.connectors.length === 0 && offers.skills.length === 0 ? (
        <EmptyState
          icon={<Plug className="size-6" />}
          title={t(($) => $.context_config.offers_empty_title)}
          hint={t(($) => $.context_config.offers_empty_hint)}
        />
      ) : (
        <>
          {offers.connectors.length > 0 && (
            <ItemGroup label={t(($) => $.context_config.connectors_title)}>
              {offers.connectors.map((connector) => (
                <ConnectorRow
                  key={connector.id}
                  agentId={agentId}
                  scopeType={scopeType}
                  scopeKey={scopeKey}
                  orgId={orgId}
                  sceneKind={sceneKind}
                  connector={connector}
                  enabled={enabledKeys.has(`connector:${connector.id}`)}
                  alwaysOn={globalIds.has(connector.id)}
                  byOrg={orgEnabledKeys.has(`connector:${connector.id}`)}
                  busy={busyKeys.has(`connector:${connector.id}`)}
                  credential={credentialByConnector.get(connector.id) ?? null}
                  ownerOnly={credentialOwnerOnly}
                  readOnly={!canToggle}
                  credentialReadOnly={credentialReadOnly}
                  onToggle={(enabled) => void toggle("connector", connector.id, enabled)}
                  share={
                    scopeType === "person" && canToggle
                      ? {
                          checked: sharedConnectorIds.has(connector.id),
                          busy: busyKeys.has(`share:${connector.id}`),
                          onToggle: (next) => void toggleShare(connector.id, next),
                        }
                      : undefined
                  }
                  reportError={reportError}
                />
              ))}
            </ItemGroup>
          )}
          {offers.skills.length > 0 && (
            <ItemGroup label={t(($) => $.context_config.skills_title)}>
              {offers.skills.map((skill) => (
                <SkillRow
                  key={skill.id}
                  skill={skill}
                  enabled={enabledKeys.has(`skill:${skill.id}`)}
                  alwaysOn={globalIds.has(skill.id)}
                  byOrg={orgEnabledKeys.has(`skill:${skill.id}`)}
                  busy={busyKeys.has(`skill:${skill.id}`)}
                  readOnly={!canToggle}
                  onToggle={(enabled) => void toggle("skill", skill.id, enabled)}
                />
              ))}
            </ItemGroup>
          )}
        </>
      )}
      {/* Prompts and MCP servers need a backend that reports rights. */}
      {rights ? (
        <>
          <ScopePrompts
            agentId={agentId}
            scope={scopeInput}
            prompts={content.prompts}
            canEdit={rights.editPrompts}
            reportError={reportError}
          />
          <ScopeMcpServers
            agentId={agentId}
            scope={scopeInput}
            mcpConfig={content.mcpConfig}
            redacted={content.mcpConfigRedacted}
            canEdit={rights.editMcp}
            reportError={reportError}
          />
        </>
      ) : null}
    </section>
  );
}

interface ShareInGroupsControl {
  checked: boolean;
  busy: boolean;
  onToggle: (next: boolean) => void;
}

function ConnectorRow({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind,
  connector,
  enabled,
  alwaysOn,
  byOrg,
  busy,
  credential,
  ownerOnly,
  readOnly,
  credentialReadOnly,
  onToggle,
  share,
  reportError,
}: {
  agentId: string;
  scopeType: ConfigLevel;
  scopeKey: string;
  orgId: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  enabled: boolean;
  alwaysOn: boolean;
  /** Turned on at the enterprise level. */
  byOrg: boolean;
  busy: boolean;
  credential: ContextConnectorCredential | null;
  /** Accounts are connected by the person only (shown as a note). */
  ownerOnly: boolean;
  /** The switch cannot be changed. */
  readOnly: boolean;
  /** Accounts and tokens cannot be changed (no note). */
  credentialReadOnly: boolean;
  onToggle: (enabled: boolean) => void;
  /** Person scope only: 「在群聊中由我触发时也可用」. */
  share?: ShareInGroupsControl;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const visibleTools = connector.tools.slice(0, 6);
  const hiddenToolCount = connector.tools.length - visibleTools.length;
  return (
    <li className="space-y-3 p-3">
      <div className="flex items-start gap-3">
        <ConnectorLogo slug={connector.catalogSlug} className="mt-0.5" />
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <span className="truncate text-body font-medium">{connector.name}</span>
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
          {visibleTools.length > 0 && (
            <div
              className="flex flex-wrap gap-1"
              aria-label={t(($) => $.context_config.tools_label)}
            >
              {visibleTools.map((tool) => (
                <span
                  key={tool}
                  className="max-w-full truncate rounded bg-muted px-1.5 py-0.5 font-mono text-micro text-muted-foreground"
                >
                  {tool}
                </span>
              ))}
              {hiddenToolCount > 0 && (
                <span className="px-1 py-0.5 text-micro text-muted-foreground">
                  {t(($) => $.context_config.more_tools, { count: hiddenToolCount })}
                </span>
              )}
            </div>
          )}
        </div>
        {!alwaysOn && (
          <ToggleControl
            busy={busy}
            checked={enabled}
            disabled={readOnly}
            label={t(($) => $.context_config.toggle_aria, { name: connector.name })}
            onToggle={onToggle}
          />
        )}
      </div>
      {share && enabled && !alwaysOn && (
        <ShareInGroupsSwitch connector={connector} control={share} />
      )}
      {connector.authMode === "oauth" ? (
        <OAuthConnectionControl
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          sceneKind={sceneKind}
          connector={connector}
          credential={credential}
          ownerOnly={ownerOnly}
          readOnly={credentialReadOnly}
          reportError={reportError}
        />
      ) : connector.acceptsCredential ? (
        <CredentialControl
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          sceneKind={sceneKind}
          connector={connector}
          credential={credential}
          ownerOnly={ownerOnly}
          readOnly={credentialReadOnly}
          reportError={reportError}
        />
      ) : null}
    </li>
  );
}

/** Whether a personal connector may also serve group chats the person
 * triggers. Off by default. The runtime does not read it yet: personal
 * connectors still apply in every run the person triggers, group runs
 * included (docs/context-capabilities.md §1.2), so the switch says it is
 * only saved. */
function ShareInGroupsSwitch({
  connector,
  control,
}: {
  connector: ContextOfferedConnector;
  control: ShareInGroupsControl;
}) {
  const { t } = useT("agents");
  const switchId = `context-share-${connector.id}`;
  return (
    <div className="flex items-start gap-3 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="min-w-0 flex-1 space-y-0.5">
        <label htmlFor={switchId} className="block text-caption font-medium">
          {t(($) => $.context_config.share_in_groups)}
        </label>
        <p id={`${switchId}-hint`} className="text-caption text-muted-foreground">
          {t(($) => $.context_config.share_in_groups_hint)}
        </p>
        <p id={`${switchId}-pending`} className="text-caption text-muted-foreground">
          {t(($) => $.context_config.share_in_groups_pending_note)}
        </p>
      </div>
      <span className="flex h-6 w-10 shrink-0 items-center justify-end">
        {control.busy ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
        ) : (
          <Switch
            id={switchId}
            size="sm"
            checked={control.checked}
            onCheckedChange={(next) => control.onToggle(next)}
            aria-describedby={`${switchId}-hint ${switchId}-pending`}
          />
        )}
      </span>
    </div>
  );
}

/**
 * Official app connected through the provider's own sign-in: 连接 starts the
 * OAuth flow for this scope and leaves the page; on return the stored
 * connection shows its account hint and can be disconnected. GitHub also
 * accepts a Personal Access Token as a secondary option.
 */
function OAuthConnectionControl({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind,
  connector,
  credential,
  ownerOnly,
  readOnly,
  reportError,
}: {
  agentId: string;
  scopeType: ConfigLevel;
  scopeKey: string;
  orgId: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  credential: ContextConnectorCredential | null;
  ownerOnly: boolean;
  readOnly: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const credentialNote = useCredentialNote(scopeType, sceneKind);
  const { open: openAuthorizeUrl, returnTo } = useContext(ConnectPlumbingContext);
  const start = useStartContextConnectorConnection(agentId);
  const deleteCredential = useDeleteContextConnectorCredential(agentId);
  const [confirmingRemove, setConfirmingRemove] = useState(false);
  const [patOpen, setPatOpen] = useState(false);
  // Stays true after handing the page to the provider, so the button cannot
  // start a second flow while the WebView navigates away.
  const [redirecting, setRedirecting] = useState(false);
  const connecting = start.isPending || redirecting;
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));

  // The server says whether it can run this app's sign-in (GitHub needs the
  // GitHub App client credentials); without it a Personal Access Token is
  // the only way to connect.
  const oauthReady = Boolean(openAuthorizeUrl) && connector.oauthAvailable;

  const connect = async () => {
    if (!openAuthorizeUrl || connecting) return;
    try {
      const url = await start.mutateAsync({
        scopeType,
        scopeKey,
        ...orgField(orgId),
        connectorId: connector.id,
        ...(returnTo ? { returnTo } : {}),
      });
      if (!url) {
        toast.error(t(($) => $.context_config.connect_failed));
        return;
      }
      setRedirecting(true);
      // The platform reopens this level (and its tenant) when the provider
      // returns.
      openAuthorizeUrl(url, { agentId, scopeType, scopeKey, ...orgField(orgId) });
    } catch (error) {
      if (reportError(error)) return;
      // Retrying cannot fix these two, so they are not reported as "try again".
      switch (errorCode(error)) {
        case "oauth_unavailable":
          toast.error(
            connector.acceptsPat
              ? t(($) => $.context_config.connect_unavailable_pat, { name: connector.name })
              : t(($) => $.context_config.connect_unavailable, { name: connector.name }),
          );
          break;
        case "forbidden":
          toast.error(t(($) => $.context_config.connect_forbidden));
          break;
        default:
          toast.error(t(($) => $.context_config.connect_failed));
      }
    }
  };

  const disconnect = async () => {
    try {
      await deleteCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId: connector.id });
      setConfirmingRemove(false);
      toast.success(t(($) => $.context_config.disconnected));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.disconnect_failed));
    }
  };

  // OAuth hints are "@<account>" when the provider reported one, otherwise a
  // generic "OAuth"; a Personal Access Token keeps its masked "••••" hint.
  const status = credential
    ? credential.kind === "oauth" || credential.kind === "unknown"
      ? credential.hint.startsWith("@")
        ? t(($) => $.context_config.connected_as, { account: credential.hint })
        : t(($) => $.context_config.connected)
      : t(($) => $.context_config.pat_set, { hint: credential.hint || "••••" })
    : connector.credentialRequired
      ? t(($) => $.context_config.connect_required)
      : t(($) => $.context_config.connect_optional);
  const missing = !credential && connector.credentialRequired;

  return (
    <div className="space-y-2 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="flex items-start gap-2">
        {credential ? (
          <Link2 className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        ) : (
          <KeyRound
            className={cn(
              "mt-0.5 size-3.5 shrink-0",
              missing ? "text-destructive" : "text-muted-foreground",
            )}
          />
        )}
        <p
          className={cn(
            "min-w-0 flex-1 break-words text-caption",
            missing ? "text-destructive" : credential ? "text-foreground" : "text-muted-foreground",
          )}
        >
          {status}
        </p>
      </div>

      {readOnly ? null : ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : patOpen ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          connectorId={connector.id}
          inputId={`context-pat-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.pat_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.pat_placeholder)}
          note={credentialNote}
          onClose={() => setPatOpen(false)}
          reportError={reportError}
        />
      ) : confirmingRemove ? (
        <div className="flex gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="flex-1"
            onClick={() => setConfirmingRemove(false)}
            disabled={deleteCredential.isPending}
          >
            {t(($) => $.context_config.cancel)}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            className="flex-1"
            onClick={() => void disconnect()}
            disabled={deleteCredential.isPending}
          >
            {deleteCredential.isPending && (
              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
            )}
            {t(($) => $.context_config.confirm_disconnect)}
          </Button>
        </div>
      ) : credential ? (
        <div className="flex flex-wrap gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground hover:text-destructive"
            onClick={() => setConfirmingRemove(true)}
          >
            {t(($) => $.context_config.disconnect)}
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            {oauthReady && (
              <Button size="sm" onClick={() => void connect()} disabled={connecting}>
                {connecting && (
                  <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
                )}
                {connecting
                  ? t(($) => $.context_config.connecting)
                  : t(($) => $.context_config.connect)}
              </Button>
            )}
            {connector.acceptsPat && (
              <Button
                variant={oauthReady ? "ghost" : "default"}
                size="sm"
                onClick={() => setPatOpen(true)}
                disabled={connecting}
              >
                {oauthReady
                  ? t(($) => $.context_config.use_pat)
                  : t(($) => $.context_config.pat_connect)}
              </Button>
            )}
          </div>
          {!connector.oauthAvailable && (
            <p className="text-caption text-muted-foreground">
              {connector.acceptsPat
                ? t(($) => $.context_config.connect_unavailable_pat, { name: connector.name })
                : t(($) => $.context_config.connect_unavailable, { name: connector.name })}
            </p>
          )}
          <p className="text-caption text-muted-foreground">
            {scopeType === "org"
              ? t(($) => $.context_config.connect_org_note)
              : scopeType === "scene"
                ? sceneKind === "dm"
                  ? t(($) => $.context_config.connect_dm_note, { name: connector.name })
                  : t(($) => $.context_config.connect_scene_note, { name: connector.name })
                : t(($) => $.context_config.connect_person_note)}
          </p>
        </div>
      )}

      {connector.catalogSlug === "github" && !readOnly && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.context_config.install_hint)}
          {connector.installUrl && (
            <>
              {" "}
              <a
                href={connector.installUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
              >
                {t(($) => $.context_config.install_link)}
                <ExternalLink className="size-3" />
              </a>
            </>
          )}
        </p>
      )}
    </div>
  );
}

function CredentialControl({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  sceneKind,
  connector,
  credential,
  ownerOnly,
  readOnly,
  reportError,
}: {
  agentId: string;
  scopeType: ConfigLevel;
  scopeKey: string;
  orgId: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  credential: ContextConnectorCredential | null;
  ownerOnly: boolean;
  readOnly: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const credentialNote = useCredentialNote(scopeType, sceneKind);
  const deleteCredential = useDeleteContextConnectorCredential(agentId);
  const [editing, setEditing] = useState(false);
  const [confirmingRemove, setConfirmingRemove] = useState(false);

  const remove = async () => {
    try {
      await deleteCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId: connector.id });
      setConfirmingRemove(false);
      toast.success(t(($) => $.context_config.credential_removed));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.credential_remove_failed));
    }
  };

  const status = credential
    ? t(($) => $.context_config.credential_set, { hint: credential.hint || "••••" })
    : connector.credentialRequired
      ? t(($) => $.context_config.credential_required)
      : t(($) => $.context_config.credential_optional);

  return (
    <div className="space-y-2 rounded-md bg-muted/40 px-3 py-2.5">
      <div className="flex items-start gap-2">
        <KeyRound
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            !credential && connector.credentialRequired
              ? "text-destructive"
              : "text-muted-foreground",
          )}
        />
        <p
          className={cn(
            "min-w-0 flex-1 text-caption",
            !credential && connector.credentialRequired
              ? "text-destructive"
              : "text-muted-foreground",
          )}
        >
          {status}
        </p>
      </div>

      {readOnly ? null : ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : editing ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          orgId={orgId}
          connectorId={connector.id}
          inputId={`context-credential-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.credential_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.credential_placeholder)}
          note={credentialNote}
          onClose={() => setEditing(false)}
          reportError={reportError}
        />
      ) : confirmingRemove ? (
        <div className="flex gap-2">
          <Button
            variant="ghost"
            size="sm"
            className="flex-1"
            onClick={() => setConfirmingRemove(false)}
            disabled={deleteCredential.isPending}
          >
            {t(($) => $.context_config.cancel)}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            className="flex-1"
            onClick={() => void remove()}
            disabled={deleteCredential.isPending}
          >
            {deleteCredential.isPending && (
              <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
            )}
            {t(($) => $.context_config.confirm_remove)}
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            {credential
              ? t(($) => $.context_config.replace_credential)
              : t(($) => $.context_config.set_credential)}
          </Button>
          {credential && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => setConfirmingRemove(true)}
            >
              {t(($) => $.context_config.remove_credential)}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

/** Note under a token input: who the stored token serves. */
function useCredentialNote(scopeType: ConfigLevel, sceneKind: ContextSceneKind): string {
  const { t } = useT("agents");
  switch (scopeType) {
    case "org":
      return t(($) => $.context_config.credential_org_note);
    case "scene":
      return sceneKind === "dm"
        ? t(($) => $.context_config.credential_dm_note)
        : t(($) => $.context_config.credential_scene_note);
    default:
      return t(($) => $.context_config.credential_person_note);
  }
}

/** Write-only token input for an enterprise, scene or personal credential
 * (Bearer, or a Personal Access Token for an OAuth connector that accepts
 * one). */
function BearerForm({
  agentId,
  scopeType,
  scopeKey,
  orgId,
  connectorId,
  inputId,
  inputLabel,
  placeholder,
  note,
  onClose,
  reportError,
}: {
  agentId: string;
  scopeType: ConfigLevel;
  scopeKey: string;
  orgId: string;
  connectorId: string;
  inputId: string;
  inputLabel: string;
  placeholder: string;
  note: string;
  onClose: () => void;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const setCredential = useSetContextConnectorCredential(agentId);
  const [bearer, setBearer] = useState("");
  const [invalid, setInvalid] = useState(false);

  const save = async () => {
    const value = bearer.trim();
    if (!isValidBearer(value)) {
      setInvalid(true);
      return;
    }
    try {
      await setCredential.mutateAsync({ scopeType, scopeKey, ...orgField(orgId), connectorId, bearer: value });
      setBearer("");
      onClose();
      toast.success(t(($) => $.context_config.credential_saved));
    } catch (error) {
      if (!reportError(error)) toast.error(t(($) => $.context_config.credential_failed));
    } finally {
      // Drop the submitted secret from the mutation state right away.
      setCredential.reset();
    }
  };

  return (
    <div className="space-y-2">
      <label htmlFor={inputId} className="sr-only">
        {inputLabel}
      </label>
      <Input
        id={inputId}
        type="password"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        value={bearer}
        maxLength={MAX_BEARER_LENGTH}
        aria-invalid={invalid || undefined}
        placeholder={placeholder}
        onChange={(event) => {
          setBearer(event.target.value);
          setInvalid(false);
        }}
        className="h-10"
      />
      <p className={cn("text-caption", invalid ? "text-destructive" : "text-muted-foreground")}>
        {invalid ? t(($) => $.context_config.credential_invalid) : note}
      </p>
      <div className="flex gap-2">
        <Button variant="ghost" className="h-9 flex-1" onClick={onClose} disabled={setCredential.isPending}>
          {t(($) => $.context_config.cancel)}
        </Button>
        <Button
          className="h-9 flex-1"
          onClick={() => void save()}
          disabled={setCredential.isPending || !bearer.trim()}
        >
          {setCredential.isPending && (
            <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />
          )}
          {t(($) => $.context_config.save)}
        </Button>
      </div>
    </div>
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
