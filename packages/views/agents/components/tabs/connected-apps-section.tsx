"use client";

import { useEffect, useRef, useState } from "react";
import { AlertCircle, CheckCircle2, ChevronRight } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import {
  agentConnectedAppsOptions,
  agentContextCapabilitiesOptions,
  type ConnectedApp,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../../../navigation";
import { ConnectorLogo, connectorBrandName } from "../../../common/connector-logo";
import { useT } from "../../../i18n";
import { ConnectedAppPage } from "./connected-app-page";
import { ConfigureLink } from "./context-offers-section";
import { ConnectedAppStatusPill, useOfficialAppDescription } from "./connected-app-labels";
import { ConnectorNotice, SectionHeading } from "./connectors-ui";

/** URL parameter of the open app configuration page (deep-linkable). */
const CONNECTED_APP_PARAM = "app";

type ConnectReturn = { kind: "connected"; slug: string } | { kind: "error"; code: string };

/**
 * Block B 「连接应用」: every official app (GitHub, Notion, ...) as a card
 * with its status for this agent. A card opens that app's configuration
 * page in place (`?app=<slug>`), where the shared account, the 对所有用户
 * 启用 grant, the offer that lets groups and people connect their own
 * accounts, the tools and removal live.
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
  const openSlug = navigation.searchParams.get(CONNECTED_APP_PARAM) ?? "";
  const connectReturn = useConnectReturn();
  const sectionRef = useRef<HTMLElement>(null);

  // The section sits at the bottom of the tab: bring it into view when a
  // sign-in result is reported and when an app page (a click, a deep link,
  // the return from a provider sign-in) has rendered; before that the tab
  // may be too short to scroll the section up.
  const scrollIntoView = () => sectionRef.current?.scrollIntoView({ block: "start" });
  useEffect(() => {
    if (connectReturn) sectionRef.current?.scrollIntoView({ block: "start" });
  }, [connectReturn]);

  const writeApp = (slug: string) => {
    const params = new URLSearchParams(navigation.searchParams);
    if (slug) params.set(CONNECTED_APP_PARAM, slug);
    else params.delete(CONNECTED_APP_PARAM);
    const search = params.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  };

  return (
    <section ref={sectionRef} className="scroll-mt-4 space-y-4" aria-labelledby="connected-apps-title">
      <SectionHeading
        id="connected-apps-title"
        level={2}
        title={t(($) => $.tab_body.connected_apps.title)}
        hint={t(($) => $.tab_body.connected_apps.hint)}
      />
      {connectReturn ? <ConnectReturnNotice result={connectReturn} /> : null}
      {!canEdit ? (
        <ConnectorNotice>{t(($) => $.tab_body.connected_apps.viewer_only)}</ConnectorNotice>
      ) : openSlug ? (
        // The app page reads can_admin from its own detail response.
        <ConnectedAppPage
          key={openSlug}
          agent={agent}
          wsId={wsId}
          slug={openSlug}
          onBack={() => writeApp("")}
          onLoaded={scrollIntoView}
        />
      ) : (
        <ConnectedAppsGallery agent={agent} wsId={wsId} onOpen={writeApp} />
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
      <ul className="grid gap-3 sm:grid-cols-2">
        {list.data.apps.map((app) => (
          <li key={app.slug} className="min-w-0">
            <ConnectedAppCard app={app} onOpen={() => onOpen(app.slug)} />
          </li>
        ))}
      </ul>
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
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.connected_apps.configure_hint)}</p>
          <ConfigureLink url={configureUrl} />
        </div>
      ) : null}
    </>
  );
}

function ConnectedAppCard({ app, onOpen }: { app: ConnectedApp; onOpen: () => void }) {
  const { t } = useT("agents");
  const describe = useOfficialAppDescription();
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={t(($) => $.tab_body.connected_apps.card_aria, { name: app.name })}
      className="flex h-full w-full items-start gap-3 rounded-lg border bg-card p-4 text-left transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <ConnectorLogo slug={app.slug} size="md" />
      <span className="min-w-0 flex-1 space-y-1.5">
        <span className="block truncate text-body font-semibold">{app.name}</span>
        <span className="line-clamp-2 block text-caption text-muted-foreground">{describe(app.slug)}</span>
        <ConnectedAppStatusPill app={app} />
      </span>
      <ChevronRight className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
    </button>
  );
}

/**
 * Reads the outcome of a shared-account sign-in the server redirected back
 * with (`?connected=<slug>` or `?connect_error=<code>`) once, and removes
 * those parameters so a reload does not repeat it. The `app` parameter
 * stays, so the app page the sign-in started from stays open.
 */
function useConnectReturn(): ConnectReturn | null {
  const navigation = useNavigation();
  const [value, setValue] = useState<ConnectReturn | null>(null);
  const consumed = useRef(false);

  useEffect(() => {
    if (consumed.current) return;
    const connected = navigation.searchParams.get("connected");
    const error = navigation.searchParams.get("connect_error");
    if (connected === null && error === null) return;
    consumed.current = true;
    setValue(error !== null ? { kind: "error", code: error } : { kind: "connected", slug: connected ?? "" });
    const params = new URLSearchParams(navigation.searchParams);
    params.delete("connected");
    params.delete("connect_error");
    const query = params.toString();
    navigation.replace(`${navigation.pathname}${query ? `?${query}` : ""}`);
  }, [navigation]);

  return value;
}

function ConnectReturnNotice({ result }: { result: ConnectReturn }) {
  const { t } = useT("agents");
  if (result.kind === "connected") {
    return (
      <p role="status" className="flex items-start gap-2 rounded-lg border bg-muted/40 px-4 py-3 text-body">
        <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" aria-hidden="true" />
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
