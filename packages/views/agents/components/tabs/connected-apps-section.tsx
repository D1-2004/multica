"use client";

import { useQuery } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import {
  agentConnectedAppsOptions,
  agentContextCapabilitiesOptions,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";
import { APP_PARAM, useConnectReturnToast, useReplaceSearch } from "./connect-flow";
import { ConnectedAppDialog } from "./connected-app-dialog";
import { ConnectedAppStatusPill } from "./connected-app-labels";
import { ConfigureLink } from "./context-offers-section";
import { AppTile, AppTileGrid, ConnectorNotice, SectionHeading } from "./connectors-ui";

/**
 * Block B 「连接应用」: every official app (GitHub, Notion, ...) as a compact
 * tile with its status for this agent. A tile opens the app's configuration
 * in a dialog (`?app=<slug>`, deep-linkable), where the shared account, the
 * 对所有用户启用 grant, the offer that lets groups and people connect their
 * own accounts, the tools and removal live.
 */
export function ConnectedAppsSection({
  agent,
  wsId,
  canEdit,
}: {
  agent: Agent;
  wsId: string;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const navigation = useNavigation();
  const replaceSearch = useReplaceSearch();
  const openSlug = canEdit ? (navigation.searchParams.get(APP_PARAM) ?? "") : "";
  useConnectReturnToast();

  const setOpenSlug = (slug: string) =>
    replaceSearch((params) => {
      if (slug) params.set(APP_PARAM, slug);
      else params.delete(APP_PARAM);
    });

  return (
    <section className="space-y-4" aria-labelledby="connected-apps-title">
      <SectionHeading id="connected-apps-title" level={2} title={t(($) => $.tab_body.connected_apps.title)} />
      {canEdit ? (
        <>
          <ConnectedAppsGallery agent={agent} wsId={wsId} onOpen={setOpenSlug} />
          {/* The dialog reads can_admin from its own detail response. */}
          <ConnectedAppDialog agent={agent} wsId={wsId} slug={openSlug} onClose={() => setOpenSlug("")} />
        </>
      ) : (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.viewer_only)}</p>
      )}
    </section>
  );
}

function ConnectedAppsGallery({
  agent,
  wsId,
  onOpen,
}: {
  agent: Agent;
  wsId: string;
  onOpen: (slug: string) => void;
}) {
  const { t } = useT("agents");
  const list = useQuery(agentConnectedAppsOptions(wsId, agent.id));
  const caps = useQuery(agentContextCapabilitiesOptions(wsId, agent.id));
  const configureUrl = caps.data?.configureUrl ?? "";

  let body: React.ReactNode;
  if (list.isLoading) {
    body = <ConnectorNotice loading>{t(($) => $.tab_body.connected_apps.loading)}</ConnectorNotice>;
  } else if (list.isError || !list.data) {
    body = (
      <ConnectorNotice>
        <span className="flex-1">{t(($) => $.tab_body.connected_apps.load_failed)}</span>
        <Button variant="outline" size="sm" onClick={() => void list.refetch()}>
          {t(($) => $.tab_body.connectors.retry)}
        </Button>
      </ConnectorNotice>
    );
  } else if (list.data.apps.length === 0) {
    body = <ConnectorNotice>{t(($) => $.tab_body.connected_apps.empty)}</ConnectorNotice>;
  } else {
    body = (
      <AppTileGrid label={t(($) => $.tab_body.connected_apps.title)}>
        {list.data.apps.map((app) => (
          <AppTile
            key={app.slug}
            slug={app.slug}
            name={app.name}
            ariaLabel={t(($) => $.tab_body.connected_apps.card_aria, { name: app.name })}
            onOpen={() => onOpen(app.slug)}
          >
            <ConnectedAppStatusPill app={app} />
          </AppTile>
        ))}
      </AppTileGrid>
    );
  }

  return (
    <>
      {body}
      {configureUrl ? (
        <div className="space-y-1.5 pt-2">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.context_offers.configure_title)}
          </p>
          <ConfigureLink url={configureUrl} />
        </div>
      ) : null}
    </>
  );
}
