"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import { ApiError, errorCode } from "@multica/core/api";
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
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { AtlassianDomainNote } from "../../../common/atlassian-domain-note";
import { ConnectorLogo, connectorBrandName } from "../../../common/connector-logo";
import { useResetOnBackForwardRestore } from "../../../common/connector-credential";
import { useT } from "../../../i18n";
import { APP_PARAM, useDesktopConnectHandoff } from "./connect-flow";
import { ConnectedAppStatusPill, hasConnectedAccount } from "./connected-app-labels";
import {
  ConfirmDialog,
  ConnectorNotice,
  DialogSection,
  InstallLink,
  StatusPill,
  SwitchRow,
  TokenForm,
  errorMessage,
} from "./connectors-ui";

/**
 * One official app's configuration in a dialog, opened from its tile in the
 * 连接器 tab (`?view=mcp_config&app=<slug>`): 共享账号, 群聊和个人 and 工具,
 * with 添加 / 在工作区启用 and 从智能体移除. Every status comes from the
 * connected-app endpoint; actions reuse the connector library, grant, offer,
 * OAuth, credential and tool endpoints. Writes are workspace-admin-only (the
 * detail's `canAdmin`); everyone else reads.
 */
export function ConnectedAppDialog({
  agent,
  wsId,
  slug,
  onClose,
}: {
  agent: Agent;
  wsId: string;
  /** The open app; "" when closed. */
  slug: string;
  onClose: () => void;
}) {
  // Keep showing the last app while the dialog animates closed.
  const [shownSlug, setShownSlug] = useState(slug);
  if (slug && slug !== shownSlug) setShownSlug(slug);

  return (
    <Dialog
      open={slug !== ""}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-lg">
        {/* Keyed by app: a new app starts with fresh forms. */}
        {shownSlug ? <AppDialogBody key={shownSlug} agent={agent} wsId={wsId} slug={shownSlug} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function AppDialogBody({ agent, wsId, slug }: { agent: Agent; wsId: string; slug: string }) {
  const { t } = useT("agents");
  const query = useQuery(agentConnectedAppOptions(wsId, agent.id, slug));
  const app = query.data ?? null;

  if (!app) {
    let body: React.ReactNode;
    if (query.isLoading) {
      body = <ConnectorNotice loading>{t(($) => $.tab_body.connected_apps.loading)}</ConnectorNotice>;
    } else if (query.error instanceof ApiError && query.error.status === 404) {
      body = <ConnectorNotice>{t(($) => $.tab_body.connected_apps.not_found)}</ConnectorNotice>;
    } else {
      body = (
        <ConnectorNotice>
          <span className="flex-1">{t(($) => $.tab_body.connected_apps.load_failed)}</span>
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            {t(($) => $.tab_body.connectors.retry)}
          </Button>
        </ConnectorNotice>
      );
    }
    return (
      <div className="space-y-4 p-4">
        <DialogTitle className="flex items-center gap-2 pr-8 text-title font-semibold">
          <ConnectorLogo slug={slug} />
          {connectorBrandName(slug)}
        </DialogTitle>
        {body}
      </div>
    );
  }

  const connectorId = app.connectorId;
  return (
    <>
      <div className="flex items-center gap-3 border-b p-4 pr-12">
        <ConnectorLogo slug={app.slug} size="md" />
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
          <DialogTitle className="min-w-0 truncate text-title font-semibold">{app.name}</DialogTitle>
          <ConnectedAppStatusPill app={app} />
        </div>
        {app.canAdmin && app.added && connectorId !== null ? (
          <RemoveAppButton agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
        ) : null}
      </div>
      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4">
        {app.canAdmin ? (
          <SetupStep agent={agent} wsId={wsId} app={app} />
        ) : (
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.admin_only)}</p>
        )}
        {connectorId !== null ? (
          <>
            <SharedAccountSection agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
            <ScopedSection agent={agent} wsId={wsId} app={app} connectorId={connectorId} />
            <ToolsSection wsId={wsId} app={app} connectorId={connectorId} />
          </>
        ) : null}
      </div>
    </>
  );
}

/** The one step an admin still has to take before the app can be used, as
 * one row: add it to this agent, or turn its workspace connector back on. */
function SetupStep({ agent, wsId, app }: { agent: Agent; wsId: string; app: ConnectedAppDetail }) {
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
    text = t(($) => $.tab_body.connected_apps.add_hint);
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
    <div className="flex items-center gap-3 rounded-lg bg-muted/40 px-3 py-2">
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
        className="h-7 shrink-0 px-2 text-caption text-destructive hover:text-destructive"
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
// 共享账号
// ---------------------------------------------------------------------------

function SharedAccountSection({
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
  const savePat = useSetInternalConnectorCredential(wsId);
  const patch = usePatchInternalConnector(wsId);
  const handOff = useDesktopConnectHandoff();
  const [patOpen, setPatOpen] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  useResetOnBackForwardRestore(redirecting, () => setRedirecting(false));
  const canAdmin = app.canAdmin;
  const shared = app.sharedAccount;
  const returnPath = `${paths.agentDetail(agent.id)}?view=mcp_config&${APP_PARAM}=${encodeURIComponent(app.slug)}`;

  const connect = async () => {
    if (handOff(returnPath)) return;
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

  const saveToken = async (bearer: string) => {
    try {
      await savePat.mutateAsync({ connectorId, bearer });
      toast.success(t(($) => $.internal_mcp.catalog.pat_saved));
      setPatOpen(false);
    } finally {
      // Drop the submitted secret from the mutation state right away.
      savePat.reset();
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
  // remove it, so there is no disconnect.
  const fromEnvironment = shared.connected && shared.source === "environment";
  // A 通用能力 needs no shared account: groups and people may connect their
  // own.
  let globalNote: React.ReactNode = (
    <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.global_hint)}</p>
  );
  if (app.globalEnabled && !shared.connected) {
    globalNote = (
      <p className="text-caption text-warning">{t(($) => $.tab_body.connected_apps.global_no_account)}</p>
    );
  } else if (app.globalEnabled && app.tools.allowed === 0) {
    // The runtime mounts an app only with at least one allowed tool.
    globalNote = <p className="text-caption text-warning">{t(($) => $.tab_body.connected_apps.global_no_tools)}</p>;
  }

  const status = shared.connected
    ? shared.account
      ? t(($) => $.tab_body.connected_apps.shared_connected_as, { account: shared.account })
      : t(($) => $.tab_body.connected_apps.shared_connected)
    : t(($) => $.tab_body.connected_apps.shared_none);

  return (
    <DialogSection id={`app-shared-${app.slug}`} title={t(($) => $.tab_body.connected_apps.section_shared)}>
      {app.slug === "atlassian" ? (
        <AtlassianDomainNote
          body={t(($) => $.tab_body.connected_apps.atlassian_domain)}
          copyLabel={t(($) => $.tab_body.connected_apps.domain_copy)}
          copiedLabel={t(($) => $.tab_body.connected_apps.domain_copied)}
          docsLabel={t(($) => $.tab_body.connected_apps.atlassian_docs)}
        />
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <span className={shared.connected ? "text-body" : "text-body text-muted-foreground"}>{status}</span>
        {fromEnvironment ? (
          <span className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.shared_env)}</span>
        ) : null}
        {canAdmin ? (
          <span className="ml-auto flex flex-wrap gap-1.5">
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
          </span>
        ) : null}
      </div>
      {!app.oauthAvailable ? (
        <p className="text-caption text-muted-foreground">
          {app.allowsPat
            ? t(($) => $.tab_body.connected_apps.auth_pat_only, { name: app.name })
            : t(($) => $.tab_body.connected_apps.auth_unavailable_hint, { name: app.name })}
        </p>
      ) : null}
      {app.installUrl ? <InstallLink url={app.installUrl} /> : null}
      {app.slug === "github" && app.installUrl ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.internal_mcp.catalog.install_note)}</p>
      ) : null}
      {patOpen ? (
        <TokenForm
          inputId={`connector-pat-${connectorId}`}
          label={t(($) => $.internal_mcp.catalog.pat_label, { name: app.name })}
          placeholder={t(($) => $.internal_mcp.catalog.pat_placeholder)}
          pending={savePat.isPending}
          onSave={saveToken}
          onCancel={() => setPatOpen(false)}
        />
      ) : null}
      <SwitchRow
        id={`app-global-${app.slug}`}
        label={t(($) => $.tab_body.connected_apps.global_label)}
        note={globalNote}
        checked={app.globalEnabled}
        disabled={!canAdmin}
        pending={patch.isPending}
        onCheckedChange={(next) => void toggleGlobal(next)}
      />

      <ConfirmDialog
        open={confirmDisconnect}
        onOpenChange={setConfirmDisconnect}
        title={t(($) => $.tab_body.connected_apps.disconnect_title, { name: app.name })}
        description={t(($) => $.tab_body.connected_apps.disconnect_description, { name: app.name })}
        confirmLabel={t(($) => $.tab_body.connected_apps.disconnect)}
        pending={disconnect.isPending}
        onConfirm={() => void doDisconnect()}
      />
    </DialogSection>
  );
}

// ---------------------------------------------------------------------------
// 群聊和个人
// ---------------------------------------------------------------------------

function ScopedSection({
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
    <DialogSection id={`app-scoped-${app.slug}`} title={t(($) => $.tab_body.connected_apps.section_scoped)}>
      {app.globalEnabled ? (
        // A 通用能力 is on in every scope already; publishing it adds nothing.
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.offer_common)}</p>
      ) : (
        <SwitchRow
          id={`app-offer-${app.slug}`}
          label={t(($) => $.tab_body.connected_apps.offer_label)}
          note={
            app.offered && !app.oauthAvailable && !app.allowsPat ? (
              <p className="text-caption text-warning">{t(($) => $.tab_body.connected_apps.offer_no_auth)}</p>
            ) : (
              <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.offer_hint)}</p>
            )
          }
          checked={app.offered}
          disabled={!app.canAdmin}
          pending={setOffer.isPending}
          onCheckedChange={toggle}
        />
      )}
      {app.scenes.length === 0 && app.persons.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.usage_none)}</p>
      ) : (
        <>
          {app.scenes.length > 0 ? (
            <UsageList
              title={t(($) => $.tab_body.connected_apps.scenes_title)}
              items={app.scenes.map((scene) => ({ key: scene.sceneId, node: <SceneUsageRow scene={scene} /> }))}
            />
          ) : null}
          {app.persons.length > 0 ? (
            <UsageList
              title={t(($) => $.tab_body.connected_apps.persons_title)}
              items={app.persons.map((person) => ({
                key: person.scopeKey,
                node: <PersonUsageRow person={person} />,
              }))}
            />
          ) : null}
        </>
      )}

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
    </DialogSection>
  );
}

function UsageList({ title, items }: { title: string; items: { key: string; node: React.ReactNode }[] }) {
  return (
    <div className="space-y-1">
      <h4 className="text-caption text-muted-foreground">{title}</h4>
      <ul className="max-h-60 divide-y overflow-y-auto rounded-md border">
        {items.map((item) => (
          <li key={item.key} className="flex items-center gap-2 px-3 py-1.5">
            {item.node}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** 已开启 (an enabled binding) and 已连接 (a stored credential) are separate
 * facts and shown separately. */
function UsageBadges({ enabled, connected, account }: { enabled: boolean; connected: boolean; account: string }) {
  const { t } = useT("agents");
  return (
    <span className="flex shrink-0 flex-wrap items-center gap-1">
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
      <span className="min-w-0 flex-1 truncate text-body">
        <span className="text-muted-foreground">{kind} · </span>
        {title}
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
      {person.shareInGroups ? (
        <Badge variant="secondary" className="shrink-0 text-micro">
          {t(($) => $.tab_body.connected_apps.usage_share_in_groups)}
        </Badge>
      ) : null}
      <UsageBadges enabled={person.enabled} connected={person.connected} account={person.account} />
    </>
  );
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

function ToolsSection({ wsId, app, connectorId }: { wsId: string; app: ConnectedAppDetail; connectorId: string }) {
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
    <DialogSection id={`app-tools-${app.slug}`} title={t(($) => $.tab_body.connected_apps.section_tools)}>
      {app.toolList.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {hasConnectedAccount(app)
            ? t(($) => $.tab_body.connected_apps.tools_refresh_needed)
            : t(($) => $.tab_body.connected_apps.tools_pending)}
        </p>
      ) : (
        <ul
          className="flex max-h-48 flex-wrap gap-1 overflow-y-auto"
          aria-label={t(($) => $.tab_body.connected_apps.section_tools)}
        >
          {app.toolList.map((tool) => (
            <li key={tool.name} className="inline-flex max-w-full items-center gap-1 rounded-md border px-1.5 py-0.5">
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
        checked={app.writeEnabled}
        disabled={!app.canAdmin}
        pending={patch.isPending}
        onCheckedChange={(next) => void toggleWrite(next)}
      />
      <div className="flex flex-wrap items-center gap-2">
        {/* The tools and the write switch belong to the workspace connector,
            so they change for every agent that uses the app. */}
        <p className="min-w-0 flex-1 text-caption text-muted-foreground">
          {t(($) => $.tab_body.connected_apps.tools_scope_hint)}
        </p>
        {app.canAdmin ? (
          // The server lists tools with the shared account or, without one,
          // with any group's or person's connected account; it reports when
          // nobody is connected.
          <Button size="sm" variant="outline" disabled={refresh.isPending} onClick={() => void refreshTools()}>
            {refresh.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
            {refresh.isPending
              ? t(($) => $.internal_mcp.catalog.refreshing)
              : t(($) => $.internal_mcp.catalog.refresh_tools)}
          </Button>
        ) : null}
      </div>
    </DialogSection>
  );
}
