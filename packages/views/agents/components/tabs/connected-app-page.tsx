"use client";

import { useEffect, useRef, useState } from "react";
import { ArrowLeft, ExternalLink, Loader2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { ApiError, errorCode } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import {
  agentConnectedAppOptions,
  useAddConnectedApp,
  useRemoveAgentConnector,
  useSetAgentOffer,
  type ConnectedAppDetail,
  type ConnectedAppPersonUsage,
  type ConnectedAppSceneUsage,
} from "@multica/core/context-capabilities";
import {
  useDeleteInternalConnectorCredential,
  usePatchInternalConnector,
  useRefreshInternalConnectorTools,
  useSetInternalConnectorCredential,
  useStartInternalConnectorOAuth,
} from "@multica/core/internal-connectors";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { ConnectorLogo, connectorBrandName } from "../../../common/connector-logo";
import {
  MAX_BEARER_LENGTH,
  isValidBearer,
  useResetOnBackForwardRestore,
} from "../../../common/connector-credential";
import { openExternal } from "../../../platform/open-external";
import { isDesktopShell } from "../../../platform/local-directory";
import { useT } from "../../../i18n";
import { ConnectedAppStatusPill, hasConnectedAccount, useOfficialAppDescription } from "./connected-app-labels";
import { ConfirmDialog, ConnectorNotice, StatusPill, SwitchRow, errorMessage } from "./connectors-ui";

/**
 * One official app's configuration page inside the 连接器 tab
 * (`?view=mcp_config&app=<slug>`): 所有人共用（共享账号）, 群聊和个人自己连接
 * and 工具, with 添加 / 移除 in the header. Every status comes from the
 * connected-app endpoint; actions reuse the connector library, grant, offer,
 * OAuth, credential and tool endpoints. Writes are workspace-admin-only (the
 * detail's `canAdmin`); everyone else reads.
 */
export function ConnectedAppPage({
  agent,
  wsId,
  slug,
  onBack,
  onLoaded,
}: {
  agent: Agent;
  wsId: string;
  slug: string;
  onBack: () => void;
  /** Called once the app's configuration has rendered (not on refetches). */
  onLoaded?: () => void;
}) {
  const { t } = useT("agents");
  const query = useQuery(agentConnectedAppOptions(wsId, agent.id, slug));
  const app = query.data ?? null;
  const loaded = app !== null;
  const onLoadedRef = useRef(onLoaded);
  onLoadedRef.current = onLoaded;
  useEffect(() => {
    if (loaded) onLoadedRef.current?.();
  }, [loaded]);

  let body: React.ReactNode;
  if (query.isLoading) {
    body = <ConnectorNotice loading>{t(($) => $.tab_body.connected_apps.loading)}</ConnectorNotice>;
  } else if (query.error instanceof ApiError && query.error.status === 404) {
    body = <ConnectorNotice>{t(($) => $.tab_body.connected_apps.not_found)}</ConnectorNotice>;
  } else if (query.isError || !app) {
    body = (
      <ConnectorNotice>
        <span className="flex-1">{t(($) => $.tab_body.connected_apps.load_failed)}</span>
        <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
          {t(($) => $.tab_body.connectors.retry)}
        </Button>
      </ConnectorNotice>
    );
  } else {
    body = <AppConfiguration agent={agent} wsId={wsId} app={app} />;
  }

  return (
    <div className="space-y-4" aria-label={app?.name || connectorBrandName(slug)} role="region">
      <Button variant="ghost" size="sm" className="-ml-2" onClick={onBack}>
        <ArrowLeft className="size-4" aria-hidden="true" />
        {t(($) => $.tab_body.connected_apps.back)}
      </Button>
      {body}
    </div>
  );
}

function AppConfiguration({ agent, wsId, app }: { agent: Agent; wsId: string; app: ConnectedAppDetail }) {
  const { t } = useT("agents");
  const describe = useOfficialAppDescription();
  const connectorId = app.connectorId;

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-3">
        <ConnectorLogo slug={app.slug} size="md" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h3 className="min-w-0 truncate text-title font-semibold">{app.name}</h3>
            <ConnectedAppStatusPill app={app} />
          </div>
          <p className="text-caption text-muted-foreground">{describe(app.slug)}</p>
        </div>
        {app.canAdmin && app.added && connectorId !== null ? (
          <RemoveAppButton agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
        ) : null}
      </div>
      {app.canAdmin ? (
        <SetupNotice agent={agent} wsId={wsId} app={app} />
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.admin_only)}</p>
      )}
      {connectorId !== null ? (
        <>
          <SharedAccountCard agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
          <ScopedCard agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
          <ToolsCard wsId={wsId} app={app} connectorId={connectorId} />
        </>
      ) : null}
    </div>
  );
}

function Card({
  id,
  title,
  hint,
  children,
}: {
  id: string;
  title: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-4 rounded-lg border bg-card p-4" aria-labelledby={id}>
      <div>
        <h4 id={id} className="text-body font-medium">
          {title}
        </h4>
        {hint ? <p className="mt-1 text-caption text-muted-foreground">{hint}</p> : null}
      </div>
      {children}
    </section>
  );
}

/** The one step an admin still has to take before the app can be used:
 * add it to this agent, or turn its workspace connector back on. */
function SetupNotice({ agent, wsId, app }: { agent: Agent; wsId: string; app: ConnectedAppDetail }) {
  const { t } = useT("agents");
  const add = useAddConnectedApp(wsId, agent.id);
  const patch = usePatchInternalConnector(wsId);

  const addApp = async () => {
    try {
      await add.mutateAsync(app.slug);
      toast.success(t(($) => $.tab_body.connected_apps.added_toast, { name: app.name }));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.add_failed, { name: app.name })));
    }
  };

  const turnOn = async (connectorId: string) => {
    try {
      await patch.mutateAsync({ connectorId, enabled: true });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.turn_on_failed)));
    }
  };

  let text: string;
  let action: React.ReactNode;
  if (!app.added) {
    text = t(($) => $.tab_body.connected_apps.add_hint, { name: app.name });
    action = (
      <Button size="sm" onClick={() => void addApp()} disabled={add.isPending}>
        {add.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
        {add.isPending ? t(($) => $.tab_body.connected_apps.adding) : t(($) => $.tab_body.connected_apps.add)}
      </Button>
    );
  } else if (app.connectorId !== null && !app.enabledInWorkspace) {
    const connectorId = app.connectorId;
    text = t(($) => $.tab_body.connected_apps.workspace_disabled);
    action = (
      <Button size="sm" variant="outline" onClick={() => void turnOn(connectorId)} disabled={patch.isPending}>
        {patch.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
        {t(($) => $.tab_body.connected_apps.turn_on)}
      </Button>
    );
  } else {
    return null;
  }

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/40 px-4 py-3">
      <p className="min-w-0 flex-1 text-caption text-muted-foreground">{text}</p>
      {action}
    </div>
  );
}

function RemoveAppButton({
  agent,
  wsId,
  app,
  connectorId,
}: {
  agent: Agent;
  wsId: string;
  app: ConnectedAppDetail;
  connectorId: string;
}) {
  const { t } = useT("agents");
  const remove = useRemoveAgentConnector(wsId, agent.id);
  const [confirming, setConfirming] = useState(false);

  const confirm = async () => {
    try {
      await remove.mutateAsync(connectorId);
      setConfirming(false);
      toast.success(t(($) => $.tab_body.connected_apps.removed_toast, { name: app.name }));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.remove_failed)));
    }
  };

  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        className="shrink-0 text-muted-foreground hover:text-destructive"
        onClick={() => setConfirming(true)}
      >
        {t(($) => $.tab_body.connected_apps.remove_action)}
      </Button>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t(($) => $.tab_body.connected_apps.remove_title, { name: app.name })}
        description={t(($) => $.tab_body.connected_apps.remove_description, {
          scenes: app.usage.scenesEnabled,
          people: app.usage.personsEnabled,
        })}
        confirmLabel={t(($) => $.tab_body.connected_apps.remove_action)}
        pending={remove.isPending}
        onConfirm={() => void confirm()}
      />
    </>
  );
}

// ---------------------------------------------------------------------------
// 所有人共用（共享账号）
// ---------------------------------------------------------------------------

function SharedAccountCard({
  agent,
  wsId,
  app,
  connectorId,
}: {
  agent: Agent;
  wsId: string;
  app: ConnectedAppDetail;
  connectorId: string;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const startOAuth = useStartInternalConnectorOAuth(wsId);
  const disconnect = useDeleteInternalConnectorCredential(wsId);
  const patch = usePatchInternalConnector(wsId);
  const daemonAppUrl = useConfigStore((s) => s.daemonAppUrl);
  const [patOpen, setPatOpen] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));
  const canAdmin = app.canAdmin;
  const shared = app.sharedAccount;
  const returnPath = `${paths.agentDetail(agent.id)}?view=mcp_config&app=${encodeURIComponent(app.slug)}`;

  const connect = async () => {
    if (isDesktopShell()) {
      // The start response binds the sign-in to the browser that receives
      // it, and desktop API responses land in the app's own cookie jar. So
      // desktop never starts the connect: it opens this page on the web in
      // the system browser, where the admin connects (this page refetches on
      // focus).
      const appUrl = daemonAppUrl.trim().replace(/\/+$/, "");
      if (!appUrl) {
        toast.error(t(($) => $.internal_mcp.catalog.connect_on_web));
        return;
      }
      openExternal(`${appUrl}${returnPath}`);
      toast.info(t(($) => $.internal_mcp.catalog.continue_in_browser));
      return;
    }
    const failed = t(($) => $.internal_mcp.catalog.connect_failed, { name: app.name });
    try {
      const url = await startOAuth.mutateAsync({ connectorId, returnTo: returnPath });
      if (!url) {
        toast.error(failed);
        return;
      }
      setRedirecting(true);
      window.location.assign(url);
    } catch (error) {
      toast.error(
        errorCode(error) === "oauth_unavailable"
          ? t(($) => $.tab_body.connected_apps.auth_unavailable_hint, { name: app.name })
          : errorMessage(error, failed),
      );
    }
  };

  const doDisconnect = async () => {
    try {
      await disconnect.mutateAsync(connectorId);
      setConfirmDisconnect(false);
      toast.success(t(($) => $.tab_body.connected_apps.disconnected));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.disconnect_failed)));
    }
  };

  const toggleGlobal = async (next: boolean) => {
    try {
      await patch.mutateAsync({ connectorId, grant: { agentId: agent.id, granted: next } });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.global_failed)));
    }
  };

  const connecting = startOAuth.isPending || redirecting;
  // An operator-managed deployment credential: DELETE .../credential cannot
  // remove it, so the page offers no disconnect.
  const fromEnvironment = shared.connected && shared.source === "environment";
  // Turning it on needs a usable shared account; turning it off never does.
  const globalBlocked = !app.globalEnabled && !shared.connected;

  let globalNote: React.ReactNode = null;
  if (globalBlocked) {
    globalNote = (
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.global_needs_account)}</p>
    );
  } else if (app.globalEnabled && !shared.connected) {
    globalNote = (
      <p className="text-caption text-warning">
        {t(($) => $.tab_body.connected_apps.global_no_account, { name: app.name })}
      </p>
    );
  } else if (app.globalEnabled && app.tools.allowed === 0) {
    // The runtime mounts an app only with at least one allowed tool.
    globalNote = (
      <p className="text-caption text-warning">
        {t(($) => $.tab_body.connected_apps.global_no_tools, { name: app.name })}
      </p>
    );
  }

  return (
    <Card
      id={`app-shared-${app.slug}`}
      title={t(($) => $.tab_body.connected_apps.section_shared)}
      hint={t(($) => $.tab_body.connected_apps.shared_hint, { name: app.name })}
    >
      <div className="space-y-3">
        <p className="text-caption">
          {shared.connected ? (
            <span className="text-foreground">
              {shared.account
                ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account: shared.account })
                : t(($) => $.tab_body.connected_apps.shared_connected)}
            </span>
          ) : (
            <span className="text-muted-foreground">{t(($) => $.tab_body.connected_apps.shared_none)}</span>
          )}
        </p>
        {canAdmin ? (
          <div className="flex flex-wrap gap-2">
            {app.oauthAvailable ? (
              <Button
                size="sm"
                variant={shared.connected ? "outline" : "default"}
                disabled={connecting}
                onClick={() => void connect()}
              >
                {connecting && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
                {connecting
                  ? t(($) => $.internal_mcp.catalog.connecting)
                  : shared.connected
                    ? t(($) => $.tab_body.connected_apps.reconnect)
                    : t(($) => $.tab_body.connected_apps.connect)}
              </Button>
            ) : null}
            {app.allowsPat && !patOpen ? (
              <Button
                size="sm"
                variant={app.oauthAvailable || shared.connected ? "outline" : "default"}
                onClick={() => setPatOpen(true)}
              >
                {shared.connected
                  ? t(($) => $.tab_body.connected_apps.replace_pat)
                  : t(($) => $.internal_mcp.catalog.use_pat)}
              </Button>
            ) : null}
            {shared.connected && !fromEnvironment ? (
              <Button
                size="sm"
                variant="ghost"
                className="text-muted-foreground hover:text-destructive"
                onClick={() => setConfirmDisconnect(true)}
              >
                {t(($) => $.tab_body.connected_apps.disconnect)}
              </Button>
            ) : null}
          </div>
        ) : null}
        {fromEnvironment ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.shared_env)}</p>
        ) : null}
        {!app.oauthAvailable ? (
          <p className="text-caption text-muted-foreground">
            {app.allowsPat
              ? t(($) => $.tab_body.connected_apps.auth_pat_only, { name: app.name })
              : t(($) => $.tab_body.connected_apps.auth_unavailable_hint, { name: app.name })}
          </p>
        ) : null}
        {app.installUrl ? <InstallHint url={app.installUrl} /> : null}
        {patOpen ? (
          <SharedTokenForm
            wsId={wsId}
            connectorId={connectorId}
            appName={app.name}
            onClose={() => setPatOpen(false)}
          />
        ) : null}
      </div>

      <div className="border-t pt-4">
        <SwitchRow
          id={`app-global-${app.slug}`}
          label={t(($) => $.tab_body.connected_apps.global_label)}
          hint={t(($) => $.tab_body.connected_apps.global_hint, { name: app.name })}
          note={globalNote}
          checked={app.globalEnabled}
          disabled={!canAdmin || globalBlocked}
          pending={patch.isPending}
          onCheckedChange={(next) => void toggleGlobal(next)}
        />
      </div>

      <ConfirmDialog
        open={confirmDisconnect}
        onOpenChange={setConfirmDisconnect}
        title={t(($) => $.tab_body.connected_apps.disconnect_title, { name: app.name })}
        description={t(($) => $.tab_body.connected_apps.disconnect_description, { name: app.name })}
        confirmLabel={t(($) => $.tab_body.connected_apps.disconnect)}
        pending={disconnect.isPending}
        onConfirm={() => void doDisconnect()}
      />
    </Card>
  );
}

/** Provider page where users grant the app access to their resources
 * (GitHub App installation). */
function InstallHint({ url }: { url: string }) {
  const { t } = useT("agents");
  return (
    <p className="text-caption text-muted-foreground">
      {t(($) => $.internal_mcp.catalog.install_hint)}{" "}
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
        onClick={(event) => {
          if (!isDesktopShell()) return;
          event.preventDefault();
          openExternal(url);
        }}
      >
        {t(($) => $.internal_mcp.catalog.install_link)}
        <ExternalLink className="size-3" aria-hidden="true" />
      </a>
    </p>
  );
}

function SharedTokenForm({
  wsId,
  connectorId,
  appName,
  onClose,
}: {
  wsId: string;
  connectorId: string;
  appName: string;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const save = useSetInternalConnectorCredential(wsId);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const inputId = `connector-pat-${connectorId}`;

  async function submit() {
    const value = token.trim();
    if (!isValidBearer(value)) {
      setError(t(($) => $.internal_mcp.catalog.pat_invalid));
      return;
    }
    setError("");
    try {
      await save.mutateAsync({ connectorId, bearer: value });
      setToken("");
      toast.success(t(($) => $.internal_mcp.catalog.pat_saved));
      onClose();
    } catch (e) {
      setError(errorMessage(e, t(($) => $.internal_mcp.catalog.pat_failed)));
    } finally {
      // Drop the submitted secret from the mutation state right away.
      save.reset();
    }
  }

  return (
    <div className="space-y-2 rounded-lg border p-4">
      <label htmlFor={inputId} className="block text-label font-medium">
        {t(($) => $.internal_mcp.catalog.pat_label, { name: appName })}
      </label>
      <Input
        id={inputId}
        type="password"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        maxLength={MAX_BEARER_LENGTH}
        value={token}
        aria-invalid={error ? true : undefined}
        placeholder={t(($) => $.internal_mcp.catalog.pat_placeholder)}
        onChange={(event) => {
          setToken(event.target.value);
          setError("");
        }}
      />
      <p
        className={error ? "text-caption text-destructive" : "text-caption text-muted-foreground"}
        role={error ? "alert" : undefined}
      >
        {error || t(($) => $.internal_mcp.catalog.pat_hint)}
      </p>
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onClose} disabled={save.isPending}>
          {t(($) => $.tab_body.connectors.cancel)}
        </Button>
        <Button size="sm" onClick={() => void submit()} disabled={save.isPending || !token.trim()}>
          {save.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.internal_mcp.catalog.save)}
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 群聊和个人自己连接
// ---------------------------------------------------------------------------

function ScopedCard({
  agent,
  wsId,
  app,
  connectorId,
}: {
  agent: Agent;
  wsId: string;
  app: ConnectedAppDetail;
  connectorId: string;
}) {
  const { t } = useT("agents");
  const setOffer = useSetAgentOffer(wsId, agent.id);
  const [confirmOff, setConfirmOff] = useState(false);
  const inUse = app.usage.scenesEnabled + app.usage.personsEnabled > 0;

  const save = async (next: boolean) => {
    try {
      await setOffer.mutateAsync({ resourceType: "connector", resourceId: connectorId, offered: next });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connected_apps.offer_failed)));
    }
  };

  const toggle = (next: boolean) => {
    if (!next && inUse) {
      setConfirmOff(true);
      return;
    }
    void save(next);
  };

  return (
    <Card id={`app-scoped-${app.slug}`} title={t(($) => $.tab_body.connected_apps.section_scoped)}>
      <SwitchRow
        id={`app-offer-${app.slug}`}
        label={t(($) => $.tab_body.connected_apps.offer_label)}
        hint={t(($) => $.tab_body.connected_apps.offer_hint, { name: app.name })}
        note={
          app.offered && !app.oauthAvailable && !app.allowsPat ? (
            <p className="text-caption text-warning">
              {t(($) => $.tab_body.connected_apps.offer_no_auth, { name: app.name })}
            </p>
          ) : null
        }
        checked={app.offered}
        disabled={!app.canAdmin}
        pending={setOffer.isPending}
        onCheckedChange={toggle}
      />

      <UsageList
        title={t(($) => $.tab_body.connected_apps.scenes_title)}
        empty={t(($) => $.tab_body.connected_apps.scenes_empty, { name: app.name })}
        items={app.scenes.map((scene) => ({ key: scene.sceneKey, node: <SceneUsageRow scene={scene} /> }))}
      />
      <UsageList
        title={t(($) => $.tab_body.connected_apps.persons_title)}
        empty={t(($) => $.tab_body.connected_apps.persons_empty, { name: app.name })}
        items={app.persons.map((person) => ({ key: person.scopeKey, node: <PersonUsageRow person={person} /> }))}
      />

      <ConfirmDialog
        open={confirmOff}
        onOpenChange={setConfirmOff}
        title={t(($) => $.tab_body.connected_apps.offer_off_title, { name: app.name })}
        description={t(($) => $.tab_body.connected_apps.offer_off_description, {
          scenes: app.usage.scenesEnabled,
          people: app.usage.personsEnabled,
        })}
        confirmLabel={t(($) => $.tab_body.connectors.offer_off_confirm)}
        onConfirm={() => {
          setConfirmOff(false);
          void save(false);
        }}
      />
    </Card>
  );
}

function UsageList({
  title,
  empty,
  items,
}: {
  title: string;
  empty: string;
  items: { key: string; node: React.ReactNode }[];
}) {
  return (
    <div className="space-y-1.5">
      <h5 className="text-caption font-medium text-muted-foreground">{title}</h5>
      {items.length === 0 ? (
        <p className="rounded-md border border-dashed px-3 py-3 text-caption text-muted-foreground">{empty}</p>
      ) : (
        <ul className="max-h-80 divide-y overflow-y-auto rounded-md border">
          {items.map((item) => (
            <li key={item.key} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2.5">
              {item.node}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** 已开启 (an enabled binding) and 已连接 (a stored credential) are separate
 * facts and shown separately. */
function UsageBadges({
  enabled,
  connected,
  account,
}: {
  enabled: boolean;
  connected: boolean;
  account: string;
}) {
  const { t } = useT("agents");
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      <StatusPill tone={enabled ? "success" : "muted"}>
        {enabled
          ? t(($) => $.tab_body.connected_apps.usage_enabled)
          : t(($) => $.tab_body.connected_apps.usage_not_enabled)}
      </StatusPill>
      <StatusPill tone={connected ? "success" : "muted"}>
        {connected
          ? account
            ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account })
            : t(($) => $.tab_body.connected_apps.shared_connected)
          : t(($) => $.tab_body.connected_apps.shared_none)}
      </StatusPill>
    </span>
  );
}

function SceneUsageRow({ scene }: { scene: ConnectedAppSceneUsage }) {
  const { t } = useT("agents");
  const kind = scene.kind === "dm" ? t(($) => $.context_config.kind_dm) : t(($) => $.context_config.kind_group);
  const title =
    scene.title ||
    (scene.kind === "dm" ? t(($) => $.context_config.scene_untitled_dm) : t(($) => $.context_config.scene_untitled));
  return (
    <>
      <span className="flex min-w-0 flex-1 items-center gap-2">
        <Badge variant="outline" className="shrink-0 text-micro">
          {kind}
        </Badge>
        <span className="min-w-0 truncate text-body">{title}</span>
      </span>
      <UsageBadges enabled={scene.enabled} connected={scene.connected} account={scene.account} />
    </>
  );
}

function PersonUsageRow({ person }: { person: ConnectedAppPersonUsage }) {
  const { t } = useT("agents");
  return (
    <>
      <span className="min-w-0 flex-1 truncate text-body">
        {person.title || t(($) => $.tab_body.connected_apps.person_untitled)}
      </span>
      <span className="flex flex-wrap items-center gap-1.5">
        <UsageBadges enabled={person.enabled} connected={person.connected} account={person.account} />
        {person.shareInGroups ? (
          <Badge variant="secondary" className="text-micro">
            {t(($) => $.tab_body.connected_apps.usage_share_in_groups)}
          </Badge>
        ) : null}
      </span>
    </>
  );
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

function ToolsCard({ wsId, app, connectorId }: { wsId: string; app: ConnectedAppDetail; connectorId: string }) {
  const { t } = useT("agents");
  const refresh = useRefreshInternalConnectorTools(wsId);
  const patch = usePatchInternalConnector(wsId);

  const refreshTools = async () => {
    try {
      const result = await refresh.mutateAsync(connectorId);
      if (result) {
        toast.success(
          t(($) => $.internal_mcp.catalog.refresh_result, {
            discovered: result.discovered,
            allowed: result.allowedTools.length,
          }),
        );
      }
    } catch (error) {
      toast.error(
        errorCode(error) === "no_connected_account"
          ? t(($) => $.internal_mcp.catalog.refresh_no_account)
          : errorMessage(error, t(($) => $.internal_mcp.catalog.refresh_failed)),
      );
    }
  };

  const toggleWrite = async (next: boolean) => {
    try {
      await patch.mutateAsync({ connectorId, writeEnabled: next });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.internal_mcp.catalog.write_failed)));
    }
  };

  return (
    <Card
      id={`app-tools-${app.slug}`}
      title={t(($) => $.tab_body.connected_apps.section_tools)}
      // The tools and the write switch belong to the workspace connector, so
      // they change for every agent that uses the app.
      hint={t(($) => $.tab_body.connected_apps.tools_scope_hint, { name: app.name })}
    >
      {app.toolList.length === 0 ? (
        <p className="rounded-md border border-dashed px-3 py-3 text-caption text-muted-foreground">
          {hasConnectedAccount(app)
            ? t(($) => $.tab_body.connected_apps.tools_refresh_needed)
            : t(($) => $.tab_body.connected_apps.tools_pending)}
        </p>
      ) : (
        <ul className="flex max-h-72 flex-wrap gap-1.5 overflow-y-auto" aria-label={t(($) => $.tab_body.connected_apps.section_tools)}>
          {app.toolList.map((tool) => (
            <li key={tool.name} className="inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-1">
              <span
                className={
                  tool.allowed
                    ? "truncate font-mono text-micro text-foreground"
                    : "truncate font-mono text-micro text-muted-foreground line-through"
                }
              >
                {tool.name}
              </span>
              {tool.readOnly ? (
                <Badge variant="secondary" className="text-micro">
                  {t(($) => $.tab_body.connected_apps.read_only)}
                </Badge>
              ) : null}
              {!tool.allowed ? <span className="sr-only">{t(($) => $.tab_body.connected_apps.not_allowed)}</span> : null}
            </li>
          ))}
        </ul>
      )}
      <SwitchRow
        id={`app-write-${app.slug}`}
        label={t(($) => $.internal_mcp.catalog.write_label)}
        hint={t(($) => $.internal_mcp.catalog.write_hint)}
        checked={app.writeEnabled}
        disabled={!app.canAdmin}
        pending={patch.isPending}
        onCheckedChange={(next) => void toggleWrite(next)}
      />
      {app.canAdmin ? (
        <div>
          {/* The server lists tools with the shared account or, without one,
              with any group's or person's connected account; it reports when
              nobody is connected. */}
          <Button size="sm" variant="outline" disabled={refresh.isPending} onClick={() => void refreshTools()}>
            {refresh.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
            {refresh.isPending
              ? t(($) => $.internal_mcp.catalog.refreshing)
              : t(($) => $.internal_mcp.catalog.refresh_tools)}
          </Button>
        </div>
      ) : null}
    </Card>
  );
}
