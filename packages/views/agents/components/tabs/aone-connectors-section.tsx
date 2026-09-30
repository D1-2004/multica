"use client";

import { useMemo, useState } from "react";
import { ExternalLink, Loader2, Plus, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Agent } from "@multica/core/types";
import {
  agentContextCapabilitiesOptions,
  useRemoveAgentConnector,
  useSetAgentOffer,
} from "@multica/core/context-capabilities";
import {
  availableInternalConnectorsOptions,
  internalConnectorListOptions,
  usePatchInternalConnector,
  type InternalConnector,
} from "@multica/core/internal-connectors";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Switch } from "@multica/ui/components/ui/switch";
import { AppLink } from "../../../navigation";
import { ConnectorLogo } from "../../../common/connector-logo";
import { useT } from "../../../i18n";
import { NO_USAGE, offerUsageByResource, usageKey, type OfferUsage } from "./context-offers-section";
import {
  ConfirmDialog,
  ConnectorNotice,
  SectionHeading,
  StatusPill,
  errorMessage,
  type StatusTone,
} from "./connectors-ui";

/** One Aone FaaS connector as the agent tab shows it. */
interface AoneRow {
  id: string;
  name: string;
  /** Upstream host, tool count and auth mode; what the caller may see. */
  meta: string;
  offered: boolean;
  /** null when the caller cannot read the library state. */
  status: { tone: StatusTone; label: string } | null;
}

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

/**
 * 「Aone FaaS 连接器」 inside 「MCP（由 Multica 管理）」: the workspace Aone
 * FaaS connectors this agent uses. Workspace admins grant (添加) and revoke
 * (移除) them and switch the offer that lets groups and people turn one on
 * for themselves; everyone else sees the same rows read only, because the
 * library and its grants are workspace-admin-only.
 */
export function AoneConnectorsSection({
  agent,
  wsId,
  canEdit,
  isAdmin,
}: {
  agent: Agent;
  wsId: string;
  canEdit: boolean;
  isAdmin: boolean;
}) {
  const { t } = useT("agents");
  return (
    <section className="space-y-3" aria-labelledby="aone-connectors-title">
      {isAdmin ? (
        <AdminAoneConnectors agent={agent} wsId={wsId} />
      ) : (
        <>
          <SectionHeading id="aone-connectors-title" level={3} title={t(($) => $.tab_body.connectors.aone_title)} />
          <ReadOnlyAoneConnectors agent={agent} wsId={wsId} canReadOffers={canEdit} />
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connectors.aone_admin_only)}</p>
        </>
      )}
    </section>
  );
}

function AdminAoneConnectors({ agent, wsId }: { agent: Agent; wsId: string }) {
  const { t } = useT("agents");
  // "always": the shared staleTime is Infinity, under which `true` never
  // refetches; a credential saved on the library page shows on return.
  const list = useQuery({ ...internalConnectorListOptions(wsId), refetchOnWindowFocus: "always" });
  const caps = useQuery(agentContextCapabilitiesOptions(wsId, agent.id));
  const setOffer = useSetAgentOffer(wsId, agent.id);
  const remove = useRemoveAgentConnector(wsId, agent.id);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<AoneRow | null>(null);
  const [offerOff, setOfferOff] = useState<{ row: AoneRow; usage: OfferUsage } | null>(null);

  const offered = useMemo(() => new Set(caps.data?.offers.connectorIds ?? []), [caps.data]);
  const usage = useMemo(
    () => (caps.data ? offerUsageByResource(caps.data) : new Map<string, OfferUsage>()),
    [caps.data],
  );
  const aone = useMemo(
    () => (list.data ?? []).filter((connector) => connector.catalogSlug === ""),
    [list.data],
  );
  const statusOf = useAoneStatus();
  const authLabelOf = useAuthLabel();
  // Offered-only connectors (no grant) are listed too, so no offer stays
  // invisible on this tab.
  const rows: AoneRow[] = aone
    .filter((connector) => connector.agentIds.includes(agent.id) || offered.has(connector.id))
    .map((connector) => ({
      id: connector.id,
      name: connector.name,
      meta: [
        hostOf(connector.upstreamUrl),
        t(($) => $.tab_body.connectors.tools_count, { count: connector.allowedTools.length }),
        authLabelOf(connector.authMode),
      ]
        .filter(Boolean)
        .join(" · "),
      offered: offered.has(connector.id),
      status: statusOf(connector, connector.agentIds.includes(agent.id)),
    }));
  const addable = aone.filter((connector) => !connector.agentIds.includes(agent.id));

  const saveOffer = async (row: AoneRow, next: boolean) => {
    try {
      await setOffer.mutateAsync({ resourceType: "connector", resourceId: row.id, offered: next });
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.offer_failed)));
    }
  };

  const toggleOffer = (row: AoneRow, next: boolean) => {
    if (!next) {
      const used = usage.get(usageKey("connector", row.id)) ?? NO_USAGE;
      if (used.scenes + used.people > 0) {
        setOfferOff({ row, usage: used });
        return;
      }
    }
    void saveOffer(row, next);
  };

  const confirmRemove = async (row: AoneRow) => {
    try {
      await remove.mutateAsync(row.id);
      setRemoving(null);
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.connectors.remove_failed)));
    }
  };

  return (
    <>
      <SectionHeading
        id="aone-connectors-title"
        level={3}
        title={t(($) => $.tab_body.connectors.aone_title)}
        action={
          <>
            <ManageLibraryLink />
            <Button size="sm" variant="outline" onClick={() => setAdding(true)} disabled={list.isLoading || list.isError}>
              <Plus aria-hidden="true" />
              {t(($) => $.tab_body.connectors.aone_add)}
            </Button>
          </>
        }
      />
      {list.isLoading ? (
        <ConnectorNotice loading>{t(($) => $.tab_body.connectors.loading)}</ConnectorNotice>
      ) : list.isError ? (
        <ConnectorNotice>
          <span className="flex-1">{t(($) => $.tab_body.connectors.load_failed)}</span>
          <Button variant="outline" size="sm" onClick={() => void list.refetch()}>
            {t(($) => $.tab_body.connectors.retry)}
          </Button>
        </ConnectorNotice>
      ) : rows.length === 0 ? (
        <ConnectorNotice>{t(($) => $.tab_body.connectors.aone_empty)}</ConnectorNotice>
      ) : (
        <ul className="divide-y rounded-lg border bg-card">
          {rows.map((row) => (
            <AoneConnectorRow
              key={row.id}
              row={row}
              // The offer state comes from the context capabilities; it
              // cannot be changed before it is known.
              offerDisabled={!caps.data || setOffer.isPending}
              onOfferChange={(next) => toggleOffer(row, next)}
              action={
                <Button
                  size="icon-sm"
                  variant="ghost"
                  className="text-muted-foreground hover:text-destructive"
                  aria-label={t(($) => $.tab_body.connectors.remove_aria, { name: row.name })}
                  onClick={() => setRemoving(row)}
                >
                  <Trash2 aria-hidden="true" />
                </Button>
              }
            />
          ))}
        </ul>
      )}
      {caps.isError ? (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.tab_body.connectors.offers_load_failed)}
        </p>
      ) : null}

      <AddAoneConnectorDialog
        open={adding}
        onOpenChange={setAdding}
        agentId={agent.id}
        wsId={wsId}
        connectors={addable}
      />

      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => {
          if (!open) setRemoving(null);
        }}
        title={t(($) => $.tab_body.connectors.remove_title, { name: removing?.name ?? "" })}
        description={t(($) => $.tab_body.connectors.remove_description)}
        confirmLabel={t(($) => $.tab_body.connectors.remove)}
        pending={remove.isPending}
        onConfirm={() => {
          if (removing) void confirmRemove(removing);
        }}
      />

      <ConfirmDialog
        open={offerOff !== null}
        onOpenChange={(open) => {
          if (!open) setOfferOff(null);
        }}
        title={t(($) => $.tab_body.connectors.offer_off_title, { name: offerOff?.row.name ?? "" })}
        description={t(($) => $.tab_body.connectors.offer_off_description, {
          scenes: offerOff?.usage.scenes ?? 0,
          people: offerOff?.usage.people ?? 0,
        })}
        confirmLabel={t(($) => $.tab_body.connectors.offer_off_confirm)}
        onConfirm={() => {
          const target = offerOff;
          setOfferOff(null);
          if (target) void saveOffer(target.row, false);
        }}
      />
    </>
  );
}

/** Small ghost link to the workspace Aone FaaS library page. */
function ManageLibraryLink() {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  return (
    <AppLink
      href={paths.internalConnectors()}
      className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-caption font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      {t(($) => $.tab_body.connectors.aone_manage)}
      <ExternalLink className="size-3" aria-hidden="true" />
    </AppLink>
  );
}

function AoneConnectorRow({
  row,
  offerDisabled,
  onOfferChange,
  action,
}: {
  row: AoneRow;
  offerDisabled: boolean;
  onOfferChange?: (offered: boolean) => void;
  action?: React.ReactNode;
}) {
  const { t } = useT("agents");
  return (
    <li className="flex items-center gap-3 px-3 py-2.5" aria-label={row.name}>
      <ConnectorLogo slug="" />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 truncate text-body font-medium">{row.name}</span>
          {row.status ? <StatusPill tone={row.status.tone}>{row.status.label}</StatusPill> : null}
        </div>
        {row.meta ? <p className="truncate text-caption text-muted-foreground">{row.meta}</p> : null}
      </div>
      <span className="flex shrink-0 items-center gap-1.5 text-caption text-muted-foreground">
        <span aria-hidden="true">{t(($) => $.tab_body.connectors.offer_switch)}</span>
        <Switch
          size="sm"
          checked={row.offered}
          disabled={offerDisabled}
          onCheckedChange={onOfferChange}
          aria-label={t(($) => $.tab_body.connectors.offer_switch_aria, { name: row.name })}
        />
      </span>
      {action ? <div className="flex shrink-0 items-center">{action}</div> : null}
    </li>
  );
}

function AddAoneConnectorDialog({
  open,
  onOpenChange,
  agentId,
  wsId,
  connectors,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agentId: string;
  wsId: string;
  connectors: readonly InternalConnector[];
}) {
  const { t } = useT("agents");
  const patch = usePatchInternalConnector(wsId);
  const [busy, setBusy] = useState<string | null>(null);

  const add = async (connector: InternalConnector) => {
    setBusy(connector.id);
    try {
      await patch.mutateAsync({ connectorId: connector.id, grant: { agentId, granted: true } });
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
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {connectors.length === 0 ? (
            <ConnectorNotice>{t(($) => $.tab_body.connectors.dialog_empty)}</ConnectorNotice>
          ) : (
            <ul className="divide-y rounded-lg border">
              {connectors.map((connector) => {
                const pending = busy === connector.id;
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
                      aria-label={t(($) => $.tab_body.connectors.dialog_add_aria, { name: connector.name })}
                      onClick={() => void add(connector)}
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
        <div>
          <ManageLibraryLink />
        </div>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Callers who are not workspace admins: the Aone FaaS rows read only. The
 * workspace library is admin-only, so the rows come from the member-visible
 * grant list plus, for agent managers (who can read the offer catalog), the
 * offered Aone FaaS connectors without a grant.
 */
function ReadOnlyAoneConnectors({
  agent,
  wsId,
  canReadOffers,
}: {
  agent: Agent;
  wsId: string;
  canReadOffers: boolean;
}) {
  const { t } = useT("agents");
  const statusOf = useAoneStatus();
  const available = useQuery(availableInternalConnectorsOptions(wsId));
  const caps = useQuery({ ...agentContextCapabilitiesOptions(wsId, agent.id), enabled: canReadOffers });

  if (available.isLoading || (canReadOffers && caps.isLoading)) {
    return <ConnectorNotice loading>{t(($) => $.tab_body.connectors.loading)}</ConnectorNotice>;
  }
  if (available.isError || (canReadOffers && caps.isError)) {
    return <ConnectorNotice>{t(($) => $.tab_body.connectors.load_failed)}</ConnectorNotice>;
  }

  const offered = new Set(caps.data?.offers.connectorIds ?? []);
  const rows = new Map<string, AoneRow>();
  for (const connector of available.data ?? []) {
    if (connector.agentId !== agent.id || connector.catalogSlug !== "") continue;
    rows.set(connector.id, {
      id: connector.id,
      name: connector.name,
      meta: t(($) => $.tab_body.connectors.tools_count, { count: connector.tools.length }),
      offered: offered.has(connector.id),
      status: null,
    });
  }
  for (const connector of caps.data?.library.connectors ?? []) {
    if (connector.catalogSlug !== "" || !offered.has(connector.id) || rows.has(connector.id)) continue;
    rows.set(connector.id, {
      id: connector.id,
      name: connector.name,
      meta: "",
      offered: true,
      status: statusOf(null, false),
    });
  }

  if (rows.size === 0) {
    return <ConnectorNotice>{t(($) => $.tab_body.connectors.aone_empty)}</ConnectorNotice>;
  }
  return (
    <ul className="divide-y rounded-lg border bg-card">
      {[...rows.values()].map((row) => (
        <AoneConnectorRow key={row.id} row={row} offerDisabled />
      ))}
    </ul>
  );
}

/** State of an Aone FaaS connector for this agent: 工作区已停用 wins; a
 * connector only offered (no grant) is 仅群聊 / 个人开启 and runs on the
 * group's or person's own credential; a granted one needs a usable
 * workspace credential unless it uses no auth. `connector` is null when the
 * caller cannot read the library (only the grant is known). */
function useAoneStatus(): (
  connector: InternalConnector | null,
  granted: boolean,
) => { tone: StatusTone; label: string } {
  const { t } = useT("agents");
  return (connector, granted) => {
    if (connector && connector.enabled !== true) {
      return { tone: "muted", label: t(($) => $.tab_body.connectors.disabled_in_workspace) };
    }
    if (!granted) return { tone: "muted", label: t(($) => $.tab_body.connectors.status_offer_only) };
    if (connector && connector.authMode !== "none" && connector.credentialReady !== true) {
      return { tone: "warning", label: t(($) => $.tab_body.connectors.status_missing_credential) };
    }
    return { tone: "success", label: t(($) => $.tab_body.connectors.status_enabled) };
  };
}

function useAuthLabel(): (mode: InternalConnector["authMode"]) => string {
  const { t } = useT("agents");
  return (mode) => {
    switch (mode) {
      case "none":
        return t(($) => $.tab_body.connectors.auth_none);
      case "bearer":
        return t(($) => $.tab_body.connectors.auth_bearer);
      case "oauth":
        return t(($) => $.tab_body.connectors.auth_oauth);
      default:
        return "";
    }
  };
}
