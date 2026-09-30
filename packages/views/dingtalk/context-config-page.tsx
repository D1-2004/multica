"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  ArrowLeftRight,
  CheckCircle2,
  ExternalLink,
  KeyRound,
  Link2,
  Loader2,
  MessageCircle,
  Plug,
  User,
  Users,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
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
  type ContextConnectorCredential,
  type ContextOfferedConnector,
  type ContextSceneKind,
  type ContextScopeType,
  type ContextSkillItem,
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
import { ConnectorLogo, ConnectorMark, connectorBrandName } from "../common/connector-logo";
import { MAX_BEARER_LENGTH, isValidBearer, useResetOnBackForwardRestore } from "../common/connector-credential";
import { SkillIcon } from "../skills/lib/skill-icon";
import { useT } from "../i18n";

/** One configurable scope of one agent. */
export interface ContextConfigScopeRef {
  scopeType: ContextScopeType;
  scopeKey: string;
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

// Platform plumbing: the navigation callback the connect buttons use. Kept
// in context so the scope editors do not drill it through every level.
const OpenAuthorizeUrlContext = createContext<OpenAuthorizeUrl | undefined>(undefined);

export interface ContextConfigPickedGroup {
  /** Required by the server: only a picked chatId proves group membership. */
  chatId?: string;
  /** Optional cross-check; never enough on its own. */
  openConversationId?: string;
}

export interface ContextConfigPageProps {
  /** Agent-issued link token (`?link=`). Redeemed once on mount. */
  linkToken?: string;
  /** Preselected agent (`?agent=`), e.g. from the admin configure link. */
  initialAgentId?: string;
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
  /** Scope to open first, e.g. the one an OAuth round trip started from.
   * A redeemed link's scope wins. */
  initialScope?: ContextConfigScopeRef;
  /** Outcome of a connector OAuth round trip, shown once as a banner. */
  connectResult?: ContextConfigConnectResult | null;
}

function isReloadRequired(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { reloadRequired?: unknown }).reloadRequired === true
  );
}

type RedeemStatus = "idle" | "pending" | "done" | "expired" | "taken" | "failed";

type PreferredScope = ContextConfigScopeRef;

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
 * Configuration page (DingTalk H5, also usable in a desktop browser) where
 * the members of a chat configure the connectors and skills an agent may use
 * in that chat (本会话: a group chat or a 1:1 chat, which is a scene just like
 * a group), and where a person configures the ones used for their own
 * messages (我的). Platform-free: the web route injects sign-in and the
 * JSAPI group picker.
 */
export function ContextConfigPage({
  linkToken,
  initialAgentId,
  pickGroup,
  onAuthRequired,
  openAuthorizeUrl,
  initialScope,
  connectResult,
}: ContextConfigPageProps) {
  const { t } = useT("agents");
  const redeem = useRedeemContextConfigLink();
  const [redeemStatus, setRedeemStatus] = useState<RedeemStatus>(
    linkToken ? "pending" : "idle",
  );
  const [selectedAgentId, setSelectedAgentId] = useState(initialAgentId ?? "");
  const [preferredScope, setPreferredScope] = useState<PreferredScope | null>(
    initialScope ?? null,
  );
  const redeemedToken = useRef<string | null>(null);
  const authRequested = useRef(false);

  const requireAuth = useRef(onAuthRequired);
  requireAuth.current = onAuthRequired;
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
        if (result.agentId) setSelectedAgentId(result.agentId);
        if (result.scopeType && result.scopeKey) {
          setPreferredScope({ scopeType: result.scopeType, scopeKey: result.scopeKey });
        }
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
    enabled: redeemStatus !== "pending",
  });

  useEffect(() => {
    if (agentsQuery.error) reportErrorRef.current(agentsQuery.error);
  }, [agentsQuery.error]);

  const agents = agentsQuery.data ?? [];
  const effectiveAgentId =
    selectedAgentId || (agents.length === 1 ? (agents[0]?.id ?? "") : "");

  const pickAgent = (agentId: string) => {
    setSelectedAgentId(agentId);
    setPreferredScope(null);
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
        // A preselected `?agent=` may be one the caller has no grant for, so
        // switching is offered whenever any other granted agent exists.
        canSwitchAgent={agents.some((agent) => agent.id !== effectiveAgentId)}
        onSwitchAgent={() => pickAgent("")}
        preferredScope={preferredScope}
        pickGroup={pickGroup}
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
        <header className="space-y-1">
          <h1 className="text-title font-semibold text-balance">
            {t(($) => $.context_config.page_title)}
          </h1>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.context_config.page_description)}
          </p>
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
        <OpenAuthorizeUrlContext.Provider value={openAuthorizeUrl}>
          {body}
        </OpenAuthorizeUrlContext.Provider>
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
  canSwitchAgent,
  onSwitchAgent,
  preferredScope,
  pickGroup,
  reportError,
}: {
  agentId: string;
  canSwitchAgent: boolean;
  onSwitchAgent: () => void;
  preferredScope: PreferredScope | null;
  pickGroup?: () => Promise<ContextConfigPickedGroup | null>;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const detailQuery = useQuery(contextConfigAgentOptions(agentId));
  const detail = detailQuery.data ?? null;
  const reportErrorRef = useRef(reportError);
  reportErrorRef.current = reportError;

  useEffect(() => {
    if (detailQuery.error) reportErrorRef.current(detailQuery.error);
  }, [detailQuery.error]);

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
    <AgentScopes
      detail={detail}
      canSwitchAgent={canSwitchAgent}
      onSwitchAgent={onSwitchAgent}
      preferredScope={preferredScope}
      pickGroup={pickGroup}
      reportError={reportError}
    />
  );
}

function AgentScopes({
  detail,
  canSwitchAgent,
  onSwitchAgent,
  preferredScope,
  pickGroup,
  reportError,
}: {
  detail: ContextConfigAgentDetail;
  canSwitchAgent: boolean;
  onSwitchAgent: () => void;
  preferredScope: PreferredScope | null;
  pickGroup?: () => Promise<ContextConfigPickedGroup | null>;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const agentId = detail.agent.id;
  // The detail says whether the caller manages this agent (every scene is
  // then configurable), so the hint does not wait for, or depend on, the
  // agent list.
  const isManager = detail.access === "manager";
  const [tab, setTab] = useState<ContextScopeType>(() => {
    if (preferredScope) return preferredScope.scopeType;
    if (detail.scenes.length === 0 && detail.person) return "person";
    return "scene";
  });
  const [sceneKey, setSceneKey] = useState<string>(() => {
    if (
      preferredScope?.scopeType === "scene" &&
      detail.scenes.some((scene) => scene.scopeKey === preferredScope.scopeKey)
    ) {
      return preferredScope.scopeKey;
    }
    return detail.scenes[0]?.scopeKey ?? "";
  });
  const activeSceneKey = detail.scenes.some((scene) => scene.scopeKey === sceneKey)
    ? sceneKey
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
      const scene = await resolveScene.mutateAsync(picked);
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
        {isManager && (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.context_config.manager_hint)}
          </p>
        )}
      </div>

      <Tabs
        value={tab}
        onValueChange={(value) => {
          if (value === "scene" || value === "person") setTab(value);
        }}
        className="gap-4"
      >
        <TabsList className="!h-10 w-full">
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

      {tab === "scene" ? (
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
              detail={detail}
              reportError={reportError}
            />
            {pickGroupButton}
          </div>
        )
      ) : detail.person ? (
        <ScopeEditor
          agentId={agentId}
          scopeType="person"
          scopeKey={detail.person.scopeKey}
          title={detail.person.scopeTitle || t(($) => $.context_config.tab_person)}
          expiresAt={detail.person.expiresAt}
          detail={detail}
          bindings={detail.person.bindings}
          credentials={detail.person.credentials}
          reportError={reportError}
        />
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

      <DefaultCapabilities detail={detail} />
    </div>
  );
}

function SceneScope({
  agentId,
  sceneKey,
  sceneKind,
  detail,
  reportError,
}: {
  agentId: string;
  sceneKey: string;
  /** Kind from the agent detail; the scene detail's own kind wins. */
  sceneKind: ContextSceneKind;
  detail: ContextConfigAgentDetail;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const sceneUntitled = useSceneUntitled();
  const sceneQuery = useQuery(contextConfigSceneOptions(agentId, sceneKey));
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
      sceneKind={kind}
      title={person?.title || scene.scene.scopeTitle || sceneUntitled(kind)}
      expiresAt={scene.scene.expiresAt}
      detail={detail}
      bindings={scene.bindings}
      credentials={scene.credentials}
      ownerOnly={ownerOnly}
      reportError={reportError}
    />
  );
}

function ScopeEditor({
  agentId,
  scopeType,
  scopeKey,
  sceneKind = "group",
  title,
  expiresAt,
  detail,
  bindings,
  credentials,
  ownerOnly = false,
  reportError,
}: {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
  /** Scene scopes only: a group chat or a 1:1 chat. */
  sceneKind?: ContextSceneKind;
  title: string;
  expiresAt: string;
  detail: ContextConfigAgentDetail;
  bindings: ContextCapabilityBinding[];
  credentials: ContextConnectorCredential[];
  /** Only the person connects accounts and tokens here (a manager viewing
   * someone's 1:1 chat). */
  ownerOnly?: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const sceneKindLabel = useSceneKindLabel();
  const setBinding = useSetContextCapabilityBinding(agentId);
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

  const toggle = async (
    resourceType: "connector" | "skill",
    resourceId: string,
    enabled: boolean,
  ) => {
    const key = `${resourceType}:${resourceId}`;
    if (busyKeys.has(key)) return;
    setBusyKeys((current) => new Set(current).add(key));
    try {
      await setBinding.mutateAsync({ scopeType, scopeKey, resourceType, resourceId, enabled });
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
          {scopeType === "scene"
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
                  sceneKind={sceneKind}
                  connector={connector}
                  enabled={enabledKeys.has(`connector:${connector.id}`)}
                  alwaysOn={globalIds.has(connector.id)}
                  busy={busyKeys.has(`connector:${connector.id}`)}
                  credential={credentialByConnector.get(connector.id) ?? null}
                  ownerOnly={ownerOnly}
                  onToggle={(enabled) => void toggle("connector", connector.id, enabled)}
                  share={
                    scopeType === "person"
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
                  busy={busyKeys.has(`skill:${skill.id}`)}
                  onToggle={(enabled) => void toggle("skill", skill.id, enabled)}
                />
              ))}
            </ItemGroup>
          )}
        </>
      )}
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
  sceneKind,
  connector,
  enabled,
  alwaysOn,
  busy,
  credential,
  ownerOnly,
  onToggle,
  share,
  reportError,
}: {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  enabled: boolean;
  alwaysOn: boolean;
  busy: boolean;
  credential: ContextConnectorCredential | null;
  ownerOnly: boolean;
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
          sceneKind={sceneKind}
          connector={connector}
          credential={credential}
          ownerOnly={ownerOnly}
          reportError={reportError}
        />
      ) : connector.acceptsCredential ? (
        <CredentialControl
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          sceneKind={sceneKind}
          connector={connector}
          credential={credential}
          ownerOnly={ownerOnly}
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
  sceneKind,
  connector,
  credential,
  ownerOnly,
  reportError,
}: {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  credential: ContextConnectorCredential | null;
  ownerOnly: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const openAuthorizeUrl = useContext(OpenAuthorizeUrlContext);
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
      const url = await start.mutateAsync({ scopeType, scopeKey, connectorId: connector.id });
      if (!url) {
        toast.error(t(($) => $.context_config.connect_failed));
        return;
      }
      setRedirecting(true);
      openAuthorizeUrl(url, { agentId, scopeType, scopeKey });
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
      await deleteCredential.mutateAsync({ scopeType, scopeKey, connectorId: connector.id });
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

      {ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : patOpen ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          connectorId={connector.id}
          inputId={`context-pat-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.pat_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.pat_placeholder)}
          note={
            scopeType === "scene"
              ? sceneKind === "dm"
                ? t(($) => $.context_config.credential_dm_note)
                : t(($) => $.context_config.credential_scene_note)
              : t(($) => $.context_config.credential_person_note)
          }
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
            {scopeType === "scene"
              ? sceneKind === "dm"
                ? t(($) => $.context_config.connect_dm_note, { name: connector.name })
                : t(($) => $.context_config.connect_scene_note, { name: connector.name })
              : t(($) => $.context_config.connect_person_note)}
          </p>
        </div>
      )}

      {connector.catalogSlug === "github" && (
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
  sceneKind,
  connector,
  credential,
  ownerOnly,
  reportError,
}: {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
  sceneKind: ContextSceneKind;
  connector: ContextOfferedConnector;
  credential: ContextConnectorCredential | null;
  ownerOnly: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const deleteCredential = useDeleteContextConnectorCredential(agentId);
  const [editing, setEditing] = useState(false);
  const [confirmingRemove, setConfirmingRemove] = useState(false);

  const remove = async () => {
    try {
      await deleteCredential.mutateAsync({ scopeType, scopeKey, connectorId: connector.id });
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

      {ownerOnly ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.owner_connects)}</p>
      ) : editing ? (
        <BearerForm
          agentId={agentId}
          scopeType={scopeType}
          scopeKey={scopeKey}
          connectorId={connector.id}
          inputId={`context-credential-${scopeType}-${connector.id}`}
          inputLabel={t(($) => $.context_config.credential_input_label, { name: connector.name })}
          placeholder={t(($) => $.context_config.credential_placeholder)}
          note={
            scopeType === "scene"
              ? sceneKind === "dm"
                ? t(($) => $.context_config.credential_dm_note)
                : t(($) => $.context_config.credential_scene_note)
              : t(($) => $.context_config.credential_person_note)
          }
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

/** Write-only token input for a scene or personal credential (Bearer, or a
 * Personal Access Token for an OAuth connector that accepts one). */
function BearerForm({
  agentId,
  scopeType,
  scopeKey,
  connectorId,
  inputId,
  inputLabel,
  placeholder,
  note,
  onClose,
  reportError,
}: {
  agentId: string;
  scopeType: ContextScopeType;
  scopeKey: string;
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
      await setCredential.mutateAsync({ scopeType, scopeKey, connectorId, bearer: value });
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
  busy,
  onToggle,
}: {
  skill: ContextSkillItem;
  enabled: boolean;
  alwaysOn: boolean;
  busy: boolean;
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
        </div>
        {skill.description ? (
          <p className="line-clamp-2 text-caption text-muted-foreground">{skill.description}</p>
        ) : null}
      </div>
      {!alwaysOn && (
        <ToggleControl
          busy={busy}
          checked={enabled}
          label={t(($) => $.context_config.toggle_aria, { name: skill.name })}
          onToggle={onToggle}
        />
      )}
    </li>
  );
}

function ToggleControl({
  busy,
  checked,
  label,
  onToggle,
}: {
  busy: boolean;
  checked: boolean;
  label: string;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <span className="flex h-8 w-10 shrink-0 items-center justify-end">
      {busy ? (
        <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
      ) : (
        <Switch checked={checked} onCheckedChange={(next) => onToggle(next)} aria-label={label} />
      )}
    </span>
  );
}

function DefaultCapabilities({ detail }: { detail: ContextConfigAgentDetail }) {
  const { t } = useT("agents");
  const { connectors, skills } = detail.global;
  if (connectors.length === 0 && skills.length === 0) return null;
  return (
    <section className="space-y-2" aria-labelledby="context-config-defaults">
      <div>
        <h2 id="context-config-defaults" className="text-body font-medium">
          {t(($) => $.context_config.defaults_title)}
        </h2>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.context_config.defaults_hint)}
        </p>
      </div>
      <div className="flex flex-wrap gap-1.5">
        {connectors.map((connector) => (
          <Badge key={`c:${connector.id}`} variant="outline" className="max-w-full">
            <ConnectorMark slug={connector.catalogSlug} className="size-3" />
            <span className="truncate">{connector.name}</span>
          </Badge>
        ))}
        {skills.map((skill) => (
          <Badge key={`s:${skill.id}`} variant="outline" className="max-w-full">
            <SkillIcon className="size-3" />
            <span className="truncate">{skill.name}</span>
          </Badge>
        ))}
      </div>
    </section>
  );
}

function ItemGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <h3 className="text-caption font-medium text-muted-foreground">{label}</h3>
      <ul className="divide-y rounded-lg border bg-card">{children}</ul>
    </div>
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
