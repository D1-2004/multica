"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { AlertCircle, CheckCircle2, Loader2, Plus } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  availableInternalConnectorsOptions,
  connectorCatalogOptions,
  internalConnectorListOptions,
  useAddAgentCatalogConnector,
  useSetInternalConnectorAgentGrant,
  useSetInternalConnectorEnabled,
  type ConnectorCatalogApp,
  type InternalConnector,
} from "@multica/core/internal-connectors";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { AppLink, useNavigation } from "../../../navigation";
import { ConnectorLogo, connectorBrandName } from "../../../common/connector-logo";
import { useT } from "../../../i18n";
import {
  OfficialAppConnectorCard,
  useOfficialAppDescription,
} from "./official-app-connector-card";

type ConnectReturn = { kind: "connected"; slug: string } | { kind: "error"; code: string };

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

/** The catalog app of a workspace connector: by connector id, else slug. */
function appFor(
  connector: InternalConnector,
  apps: readonly ConnectorCatalogApp[],
): ConnectorCatalogApp | null {
  return (
    apps.find((app) => app.connectorId === connector.id) ??
    apps.find((app) => app.slug === connector.catalogSlug) ??
    null
  );
}

/**
 * 「已为智能体开启」: the connectors this agent uses for every user and every
 * scene — official apps and Aone FaaS connectors granted to it — with the
 * workspace shared-account controls and the 添加连接器 dialog. Granting is a
 * workspace-admin action (the connector library and its grants are
 * admin-only); anyone else sees the member-visible list read-only.
 */
export function AgentConnectorsSection({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { role, isLoading: memberLoading } = useCurrentMember(wsId);
  const isAdmin = role === "owner" || role === "admin";

  return (
    <section className="space-y-3" aria-labelledby="agent-connectors-title">
      <ConnectReturnNotice />
      {memberLoading ? (
        <Notice loading text={t(($) => $.tab_body.connectors.loading)} />
      ) : isAdmin ? (
        <AdminConnectors agent={agent} wsId={wsId} />
      ) : (
        <MemberConnectors agent={agent} wsId={wsId} />
      )}
    </section>
  );
}

function SectionHeader({ action }: { action?: React.ReactNode }) {
  const { t } = useT("agents");
  return (
    <div className="flex items-start justify-between gap-4">
      <div>
        <h3 id="agent-connectors-title" className="text-body font-medium">
          {t(($) => $.tab_body.connectors.global_title)}
        </h3>
        <p className="mt-1 max-w-2xl text-caption leading-5 text-muted-foreground">
          {t(($) => $.tab_body.connectors.global_hint)}
        </p>
      </div>
      {action}
    </div>
  );
}

function AdminConnectors({ agent, wsId }: { agent: Agent; wsId: string }) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const list = useQuery({ ...internalConnectorListOptions(wsId), refetchOnWindowFocus: true });
  const catalog = useQuery({ ...connectorCatalogOptions(wsId), refetchOnWindowFocus: true });
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<InternalConnector | null>(null);
  const grant = useSetInternalConnectorAgentGrant(wsId);
  const setEnabled = useSetInternalConnectorEnabled(wsId);
  const connectors = useMemo(() => list.data ?? [], [list.data]);
  const apps = useMemo(() => catalog.data ?? [], [catalog.data]);
  const granted = connectors.filter((connector) => connector.agentIds.includes(agent.id));
  const returnPath = `${paths.agentDetail(agent.id)}?view=mcp_config`;

  const turnOn = async (connector: InternalConnector) => {
    try {
      await setEnabled.mutateAsync({ connector, enabled: true });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.turn_on_failed)));
    }
  };

  const remove = async (connector: InternalConnector) => {
    try {
      await grant.mutateAsync({ connector, agentId: agent.id, granted: false });
      setRemoving(null);
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.remove_failed)));
    }
  };

  const actionsFor = (connector: InternalConnector) => (
    <>
      {!connector.enabled && (
        <Button
          size="sm"
          variant="outline"
          disabled={setEnabled.isPending}
          onClick={() => void turnOn(connector)}
        >
          {t(($) => $.tab_body.connectors.turn_on)}
        </Button>
      )}
      <Button
        size="sm"
        variant="ghost"
        className="text-muted-foreground hover:text-destructive"
        aria-label={t(($) => $.tab_body.connectors.remove_aria, { name: connector.name })}
        onClick={() => setRemoving(connector)}
      >
        {t(($) => $.tab_body.connectors.remove)}
      </Button>
    </>
  );

  return (
    <>
      <SectionHeader
        action={
          <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={list.isLoading}>
            <Plus aria-hidden="true" />
            {t(($) => $.tab_body.connectors.add)}
          </Button>
        }
      />
      {list.isLoading ? (
        <Notice loading text={t(($) => $.tab_body.connectors.loading)} />
      ) : list.isError ? (
        <Notice text={t(($) => $.tab_body.connectors.load_failed)} />
      ) : granted.length === 0 ? (
        <Notice text={t(($) => $.tab_body.connectors.empty)} />
      ) : (
        <div className="space-y-3">
          {granted.map((connector) =>
            connector.catalogSlug ? (
              <OfficialAppConnectorCard
                key={connector.id}
                workspaceId={wsId}
                connector={connector}
                app={appFor(connector, apps)}
                returnPath={returnPath}
                actions={actionsFor(connector)}
              />
            ) : (
              <AoneConnectorRow key={connector.id} connector={connector} actions={actionsFor(connector)} />
            ),
          )}
        </div>
      )}
      {catalog.isError && granted.some((connector) => connector.catalogSlug) ? (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.internal_mcp.catalog.load_failed)}
        </p>
      ) : null}
      <AppLink
        href={paths.internalConnectors()}
        className="inline-block text-caption font-medium text-primary underline-offset-4 hover:underline"
      >
        {t(($) => $.tab_body.connectors.aone_manage)}
      </AppLink>

      <AddConnectorDialog
        open={adding}
        onOpenChange={setAdding}
        agent={agent}
        wsId={wsId}
        connectors={connectors}
        apps={apps}
        catalogLoading={catalog.isLoading}
        catalogFailed={catalog.isError}
      />

      <AlertDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open && !grant.isPending) setRemoving(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.tab_body.connectors.remove_title, { name: removing?.name ?? "" })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.tab_body.connectors.remove_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={grant.isPending}>
              {t(($) => $.tab_body.context_offers.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={grant.isPending}
              onClick={(event) => {
                event.preventDefault();
                if (removing) void remove(removing);
              }}
            >
              {grant.isPending && (
                <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              )}
              {t(($) => $.tab_body.connectors.remove)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function AoneConnectorRow({
  connector,
  actions,
}: {
  connector: InternalConnector;
  actions: React.ReactNode;
}) {
  const { t } = useT("agents");
  const credential =
    connector.authMode === "none"
      ? t(($) => $.internal_mcp.no_auth)
      : connector.credentialSource === "workspace"
        ? t(($) => $.internal_mcp.credential_workspace)
        : connector.credentialSource === "environment"
          ? t(($) => $.internal_mcp.credential_environment)
          : connector.credentialOptional
            ? t(($) => $.internal_mcp.scoped_credentials)
            : t(($) => $.internal_mcp.waiting_credential);
  let host = "";
  try {
    host = new URL(connector.upstreamUrl).host;
  } catch {
    host = "";
  }
  return (
    <article className="flex flex-col gap-3 rounded-lg border bg-card p-4" aria-label={connector.name}>
      <div className="flex items-start gap-3">
        <ConnectorLogo slug="" size="md" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h4 className="truncate text-body font-semibold">{connector.name}</h4>
            <Badge variant="outline">{t(($) => $.tab_body.connectors.kind_aone)}</Badge>
            {!connector.enabled && (
              <Badge variant="secondary">{t(($) => $.tab_body.connectors.disabled_in_workspace)}</Badge>
            )}
          </div>
          {host ? <p className="break-all text-caption text-muted-foreground">{host}</p> : null}
          <p className="text-caption text-foreground">
            {t(($) => $.tab_body.connectors.tools_count, { count: connector.allowedTools.length })}
            {" · "}
            {credential}
          </p>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">{actions}</div>
    </article>
  );
}

function AddConnectorDialog({
  open,
  onOpenChange,
  agent,
  wsId,
  connectors,
  apps,
  catalogLoading,
  catalogFailed,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agent: Agent;
  wsId: string;
  connectors: readonly InternalConnector[];
  apps: readonly ConnectorCatalogApp[];
  catalogLoading: boolean;
  catalogFailed: boolean;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const describe = useOfficialAppDescription();
  const [source, setSource] = useState<"official" | "aone">("official");
  const addApp = useAddAgentCatalogConnector(wsId);
  const grant = useSetInternalConnectorAgentGrant(wsId);
  const [busy, setBusy] = useState<string | null>(null);

  const grantedSlugs = new Set(
    connectors
      .filter((connector) => connector.catalogSlug && connector.agentIds.includes(agent.id))
      .map((connector) => connector.catalogSlug),
  );
  const aone = connectors.filter(
    (connector) => connector.catalogSlug === "" && !connector.agentIds.includes(agent.id),
  );

  const addOfficial = async (app: ConnectorCatalogApp) => {
    setBusy(`app:${app.slug}`);
    try {
      await addApp.mutateAsync({ slug: app.slug, agentId: agent.id });
      toast.success(t(($) => $.tab_body.connectors.added_toast, { name: app.name }));
      onOpenChange(false);
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.add_failed, { name: app.name })));
    } finally {
      setBusy(null);
    }
  };

  const addAone = async (connector: InternalConnector) => {
    setBusy(`aone:${connector.id}`);
    try {
      await grant.mutateAsync({ connector, agentId: agent.id, granted: true });
      toast.success(t(($) => $.tab_body.connectors.added_toast, { name: connector.name }));
      onOpenChange(false);
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.add_failed, { name: connector.name })));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col gap-4 sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tab_body.connectors.dialog_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tab_body.connectors.dialog_description)}</DialogDescription>
        </DialogHeader>
        <Tabs
          value={source}
          onValueChange={(value) => {
            if (value === "official" || value === "aone") setSource(value);
          }}
        >
          <TabsList className="w-full">
            <TabsTrigger value="official">{t(($) => $.tab_body.connectors.source_official)}</TabsTrigger>
            <TabsTrigger value="aone">{t(($) => $.tab_body.connectors.source_aone)}</TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {source === "official" ? (
            catalogFailed ? (
              <Notice text={t(($) => $.tab_body.connectors.official_load_failed)} />
            ) : catalogLoading ? (
              <Notice loading text={t(($) => $.internal_mcp.catalog.loading)} />
            ) : apps.length === 0 ? (
              <Notice text={t(($) => $.tab_body.connectors.official_empty)} />
            ) : (
              <ul className="divide-y rounded-lg border">
                {apps.map((app) => {
                  const added = grantedSlugs.has(app.slug);
                  const pending = busy === `app:${app.slug}`;
                  return (
                    <li key={app.slug} className="flex items-center gap-3 p-3">
                      <ConnectorLogo slug={app.slug} />
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-body font-medium">{app.name}</p>
                        <p className="truncate text-caption text-muted-foreground">{describe(app.slug)}</p>
                      </div>
                      {added ? (
                        <Badge variant="secondary">{t(($) => $.tab_body.connectors.dialog_added)}</Badge>
                      ) : (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy !== null}
                          aria-label={`${t(($) => $.tab_body.connectors.dialog_add)} ${app.name}`}
                          onClick={() => void addOfficial(app)}
                        >
                          {pending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
                          {pending
                            ? t(($) => $.tab_body.connectors.dialog_adding)
                            : t(($) => $.tab_body.connectors.dialog_add)}
                        </Button>
                      )}
                    </li>
                  );
                })}
              </ul>
            )
          ) : aone.length === 0 ? (
            <Notice text={t(($) => $.tab_body.connectors.aone_empty)} />
          ) : (
            <ul className="divide-y rounded-lg border">
              {aone.map((connector) => {
                const pending = busy === `aone:${connector.id}`;
                return (
                  <li key={connector.id} className="flex items-center gap-3 p-3">
                    <ConnectorLogo slug="" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-body font-medium">{connector.name}</p>
                      <p className="truncate text-caption text-muted-foreground">
                        {t(($) => $.tab_body.connectors.tools_count, { count: connector.allowedTools.length })}
                        {!connector.enabled ? ` · ${t(($) => $.tab_body.connectors.disabled_in_workspace)}` : ""}
                      </p>
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={busy !== null}
                      aria-label={`${t(($) => $.tab_body.connectors.dialog_add)} ${connector.name}`}
                      onClick={() => void addAone(connector)}
                    >
                      {pending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
                      {pending
                        ? t(($) => $.tab_body.connectors.dialog_adding)
                        : t(($) => $.tab_body.connectors.dialog_add)}
                    </Button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        {source === "aone" ? (
          <AppLink
            href={paths.internalConnectors()}
            className="text-caption font-medium text-primary underline-offset-4 hover:underline"
          >
            {t(($) => $.tab_body.connectors.aone_manage)}
          </AppLink>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

/** Read-only list for members who cannot manage the connector library. */
function MemberConnectors({ agent, wsId }: { agent: Agent; wsId: string }) {
  const { t } = useT("agents");
  const available = useQuery(availableInternalConnectorsOptions(wsId));
  const assigned = (available.data ?? []).filter((connector) => connector.agentId === agent.id);
  return (
    <>
      <SectionHeader />
      {available.isLoading ? (
        <Notice loading text={t(($) => $.tab_body.connectors.loading)} />
      ) : available.isError ? (
        <Notice text={t(($) => $.tab_body.mcp_config.workspace_connectors_failed)} />
      ) : assigned.length === 0 ? (
        <Notice text={t(($) => $.tab_body.mcp_config.workspace_connectors_empty)} />
      ) : (
        <div className="space-y-2">
          {assigned.map((connector) => (
            <div
              key={connector.id}
              className="flex items-center justify-between gap-3 rounded-lg border bg-card px-4 py-3"
            >
              <div className="min-w-0">
                <p className="truncate text-body font-medium">{connector.name}</p>
                <p className="mt-1 text-caption text-muted-foreground">
                  {t(($) => $.tab_body.mcp_config.workspace_connector_tools, {
                    count: connector.tools.length,
                  })}
                </p>
              </div>
              <Badge variant="secondary">{t(($) => $.tab_body.mcp_config.workspace_connector_assigned)}</Badge>
            </div>
          ))}
        </div>
      )}
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connectors.admin_only)}</p>
    </>
  );
}

/**
 * Reports the outcome of a shared-account sign-in the server redirected back
 * with (`?connected=<slug>` or `?connect_error=<code>`) once, and removes
 * those parameters so a reload does not repeat it.
 */
function ConnectReturnNotice() {
  const { t } = useT("agents");
  const navigation = useNavigation();
  const [result, setResult] = useState<ConnectReturn | null>(null);
  const consumed = useRef(false);

  useEffect(() => {
    if (consumed.current) return;
    const connected = navigation.searchParams.get("connected");
    const error = navigation.searchParams.get("connect_error");
    if (connected === null && error === null) return;
    consumed.current = true;
    setResult(error !== null ? { kind: "error", code: error } : { kind: "connected", slug: connected ?? "" });
    const params = new URLSearchParams(navigation.searchParams);
    params.delete("connected");
    params.delete("connect_error");
    const query = params.toString();
    navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
  }, [navigation]);

  if (!result) return null;
  if (result.kind === "connected") {
    return (
      <p role="status" className="flex items-start gap-2 rounded-lg border bg-muted/40 px-4 py-3 text-body">
        <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span>
          {t(($) => $.internal_mcp.catalog.returned_connected, { name: connectorBrandName(result.slug) })}
        </span>
      </p>
    );
  }
  return (
    <p role="alert" className="flex items-start gap-2 rounded-lg border bg-muted/40 px-4 py-3 text-body text-destructive">
      <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>
        {result.code === "access_denied"
          ? t(($) => $.internal_mcp.catalog.returned_denied)
          : result.code === "browser_mismatch"
            ? t(($) => $.internal_mcp.catalog.returned_browser_mismatch)
            : t(($) => $.internal_mcp.catalog.returned_error)}
      </span>
    </p>
  );
}

function Notice({ text, loading = false }: { text: string; loading?: boolean }) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      {loading ? (
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      ) : null}
      {text}
    </div>
  );
}
