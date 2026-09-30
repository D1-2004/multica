"use client";

import { useEffect, useState } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
import { errorCode } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import {
  useRefreshInternalConnectorTools,
  useSetInternalConnectorCredential,
  useSetInternalConnectorWriteEnabled,
  useStartInternalConnectorOAuth,
  type ConnectorCatalogApp,
  type InternalConnector,
  type InternalConnectorToolsRefresh,
} from "@multica/core/internal-connectors";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { ConnectorLogo, connectorBrandName } from "../../../common/connector-logo";
import { openExternal } from "../../../platform/open-external";
import { isDesktopShell } from "../../../platform/local-directory";
import { useT } from "../../../i18n";

/** Bearer rules shared with the server: 1..4096 chars, no CR/LF/NUL. */
const MAX_TOKEN_LENGTH = 4096;

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

/** Short description of an official app, per catalog slug. */
export function useOfficialAppDescription(): (slug: string) => string {
  const { t } = useT("agents");
  return (slug: string) => {
    switch (slug) {
      case "github":
        return t(($) => $.internal_mcp.catalog.apps.github);
      case "notion":
        return t(($) => $.internal_mcp.catalog.apps.notion);
      case "linear":
        return t(($) => $.internal_mcp.catalog.apps.linear);
      case "atlassian":
        return t(($) => $.internal_mcp.catalog.apps.atlassian);
      case "sentry":
        return t(($) => $.internal_mcp.catalog.apps.sentry);
      case "asana":
        return t(($) => $.internal_mcp.catalog.apps.asana);
      case "figma":
        return t(($) => $.internal_mcp.catalog.apps.figma);
      case "stripe":
        return t(($) => $.internal_mcp.catalog.apps.stripe);
      default:
        return t(($) => $.internal_mcp.catalog.apps.other);
    }
  };
}

/**
 * One official app enabled for an agent, with the workspace shared-account
 * controls: connect (OAuth), a GitHub Personal Access Token, tool refresh
 * and the write switch. `returnPath` is the in-app path the provider sign-in
 * comes back to (the agent's connector tab).
 */
export function OfficialAppConnectorCard({
  workspaceId,
  connector,
  app,
  returnPath,
  actions,
}: {
  workspaceId: string;
  connector: InternalConnector;
  /** null while the catalog is unavailable: shown without sign-in actions. */
  app: ConnectorCatalogApp | null;
  returnPath: string;
  /** Agent-level actions (remove, turn on) rendered next to the controls. */
  actions?: React.ReactNode;
}) {
  const { t } = useT("agents");
  const describe = useOfficialAppDescription();
  const startOAuth = useStartInternalConnectorOAuth(workspaceId);
  const refresh = useRefreshInternalConnectorTools(workspaceId);
  const setWrite = useSetInternalConnectorWriteEnabled(workspaceId);
  const daemonAppUrl = useConfigStore((s) => s.daemonAppUrl);
  const [message, setMessage] = useState<{ tone: "status" | "alert"; text: string } | null>(null);
  const [patOpen, setPatOpen] = useState(false);
  const [redirecting, setRedirecting] = useState(false);
  const slug = connector.catalogSlug;
  const name = app?.name || connector.name || connectorBrandName(slug);

  // Coming back from the provider can restore this page from the
  // back/forward cache with "redirecting" still set; clear it then.
  useEffect(() => {
    if (!redirecting) return;
    const onPageShow = (event: PageTransitionEvent) => {
      if (event.persisted) setRedirecting(false);
    };
    window.addEventListener("pageshow", onPageShow);
    return () => window.removeEventListener("pageshow", onPageShow);
  }, [redirecting]);

  const account = connector.credentialAccount;
  const hasSharedAccount = connector.credentialReady === true;
  const toolCount = connector.allowedTools.length;
  const status =
    connector.discoveredToolCount === 0 && toolCount === 0
      ? t(($) => $.internal_mcp.catalog.status_pending)
      : [
          t(($) => $.internal_mcp.catalog.tools_count, { count: toolCount }),
          account
            ? t(($) => $.internal_mcp.catalog.connected_as, { account })
            : hasSharedAccount
              ? t(($) => $.internal_mcp.catalog.connected)
              : t(($) => $.internal_mcp.catalog.no_shared_account),
        ].join(" · ");

  async function connectShared() {
    setMessage(null);
    if (isDesktopShell()) {
      // The start response binds the sign-in to the browser that receives
      // it, and desktop API responses land in the app's own cookie jar. So
      // desktop never starts the connect: it opens this page on the web in
      // the system browser, where the admin connects (the list here
      // refetches on focus).
      const appUrl = daemonAppUrl.trim().replace(/\/+$/, "");
      if (!appUrl) {
        setMessage({ tone: "alert", text: t(($) => $.internal_mcp.catalog.connect_on_web) });
        return;
      }
      openExternal(`${appUrl}${returnPath}`);
      setMessage({ tone: "status", text: t(($) => $.internal_mcp.catalog.continue_in_browser) });
      return;
    }
    try {
      const url = await startOAuth.mutateAsync({ connectorId: connector.id, returnTo: returnPath });
      if (!url) {
        setMessage({ tone: "alert", text: t(($) => $.internal_mcp.catalog.connect_failed, { name }) });
        return;
      }
      setRedirecting(true);
      window.location.assign(url);
    } catch (error) {
      setMessage({ tone: "alert", text: errorMessage(error, t(($) => $.internal_mcp.catalog.connect_failed, { name })) });
    }
  }

  async function refreshTools() {
    setMessage(null);
    try {
      const result: InternalConnectorToolsRefresh | null = await refresh.mutateAsync(connector.id);
      setMessage(
        result
          ? {
              tone: "status",
              text: t(($) => $.internal_mcp.catalog.refresh_result, {
                discovered: result.discovered,
                allowed: result.allowedTools.length,
              }),
            }
          : null,
      );
    } catch (error) {
      setMessage({
        tone: "alert",
        text:
          errorCode(error) === "no_connected_account"
            ? t(($) => $.internal_mcp.catalog.refresh_no_account)
            : errorMessage(error, t(($) => $.internal_mcp.catalog.refresh_failed)),
      });
    }
  }

  async function toggleWrite(next: boolean) {
    setMessage(null);
    try {
      await setWrite.mutateAsync({ connector, writeEnabled: next });
    } catch (error) {
      setMessage({ tone: "alert", text: errorMessage(error, t(($) => $.internal_mcp.catalog.write_failed)) });
    }
  }

  const connecting = startOAuth.isPending || redirecting;
  const writeSwitchId = `connector-write-${connector.id}`;
  const oauthAvailable = app?.oauthAvailable === true;
  const allowsPat = app?.allowsPat === true;

  return (
    <article className="flex flex-col gap-4 rounded-lg border bg-card p-4" aria-label={name}>
      <div className="flex items-start gap-3">
        <ConnectorLogo slug={slug} size="md" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <h4 className="truncate text-body font-semibold">{name}</h4>
            {!connector.enabled && (
              <Badge variant="secondary">{t(($) => $.tab_body.connectors.disabled_in_workspace)}</Badge>
            )}
          </div>
          <p className="text-caption text-muted-foreground">{describe(slug)}</p>
          <p className="text-caption text-foreground">{status}</p>
        </div>
      </div>

      <div className="flex flex-wrap gap-2">
        {oauthAvailable && (
          <Button
            size="sm"
            variant={hasSharedAccount ? "outline" : "default"}
            disabled={connecting}
            onClick={() => void connectShared()}
          >
            {connecting && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
            {connecting
              ? t(($) => $.internal_mcp.catalog.connecting)
              : hasSharedAccount
                ? t(($) => $.internal_mcp.catalog.reconnect_shared)
                : t(($) => $.internal_mcp.catalog.connect_shared)}
          </Button>
        )}
        {allowsPat && !patOpen && (
          <Button size="sm" variant="outline" onClick={() => setPatOpen(true)}>
            {t(($) => $.internal_mcp.catalog.use_pat)}
          </Button>
        )}
        {/* The server lists tools with the shared account or, without one,
            with any person's or group's connected account, so the action
            stays available; it reports when nobody is connected. */}
        <Button size="sm" variant="outline" disabled={refresh.isPending} onClick={() => void refreshTools()}>
          {refresh.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {refresh.isPending
            ? t(($) => $.internal_mcp.catalog.refreshing)
            : t(($) => $.internal_mcp.catalog.refresh_tools)}
        </Button>
        {actions}
      </div>

      {app && !oauthAvailable && !allowsPat && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.internal_mcp.catalog.oauth_unconfigured, { name })}
        </p>
      )}

      {/* The configuration page lets people and groups connect their own
          accounts only for apps an agent allows there; an app that is only
          enabled for agents works through the shared account. */}
      {!hasSharedAccount && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.internal_mcp.catalog.personal_connect_hint)}
        </p>
      )}

      {patOpen && (
        <SharedTokenForm
          workspaceId={workspaceId}
          connectorId={connector.id}
          appName={name}
          onDone={(text) => {
            setPatOpen(false);
            setMessage({ tone: "status", text });
          }}
          onCancel={() => setPatOpen(false)}
        />
      )}

      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0 flex-1 space-y-0.5">
          <label htmlFor={writeSwitchId} className="block text-label font-medium">
            {t(($) => $.internal_mcp.catalog.write_label)}
          </label>
          <p id={`${writeSwitchId}-hint`} className="text-caption text-muted-foreground">
            {t(($) => $.internal_mcp.catalog.write_hint)}
          </p>
        </div>
        <span className="flex h-6 w-10 shrink-0 items-center justify-end">
          {setWrite.isPending ? (
            <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
          ) : (
            <Switch
              id={writeSwitchId}
              checked={connector.writeEnabled}
              onCheckedChange={(next) => void toggleWrite(next)}
              aria-describedby={`${writeSwitchId}-hint`}
            />
          )}
        </span>
      </div>

      {slug === "github" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.internal_mcp.catalog.install_hint)}
          {app?.installUrl && (
            <>
              {" "}
              <a
                href={app.installUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-foreground underline-offset-4 hover:underline"
                onClick={(event) => {
                  if (!isDesktopShell()) return;
                  event.preventDefault();
                  openExternal(app.installUrl);
                }}
              >
                {t(($) => $.internal_mcp.catalog.install_link)}
                <ExternalLink className="size-3" />
              </a>
            </>
          )}
        </p>
      )}

      {message && (
        <p
          role={message.tone}
          className={message.tone === "alert" ? "text-caption text-destructive" : "text-caption text-muted-foreground"}
        >
          {message.text}
        </p>
      )}
    </article>
  );
}

function SharedTokenForm({
  workspaceId,
  connectorId,
  appName,
  onDone,
  onCancel,
}: {
  workspaceId: string;
  connectorId: string;
  appName: string;
  onDone: (message: string) => void;
  onCancel: () => void;
}) {
  const { t } = useT("agents");
  const save = useSetInternalConnectorCredential(workspaceId);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const inputId = `connector-pat-${connectorId}`;

  async function submit() {
    const value = token.trim();
    if (!value || value.length > MAX_TOKEN_LENGTH || /[\r\n\0]/.test(value)) {
      setError(t(($) => $.internal_mcp.catalog.pat_invalid));
      return;
    }
    setError("");
    try {
      await save.mutateAsync({ connectorId, bearer: value });
      setToken("");
      onDone(t(($) => $.internal_mcp.catalog.pat_saved));
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
        maxLength={MAX_TOKEN_LENGTH}
        value={token}
        aria-invalid={error ? true : undefined}
        placeholder={t(($) => $.internal_mcp.catalog.pat_placeholder)}
        onChange={(event) => {
          setToken(event.target.value);
          setError("");
        }}
      />
      <p className={error ? "text-caption text-destructive" : "text-caption text-muted-foreground"} role={error ? "alert" : undefined}>
        {error || t(($) => $.internal_mcp.catalog.pat_hint)}
      </p>
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={save.isPending}>
          {t(($) => $.internal_mcp.catalog.cancel)}
        </Button>
        <Button size="sm" onClick={() => void submit()} disabled={save.isPending || !token.trim()}>
          {save.isPending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.internal_mcp.catalog.save)}
        </Button>
      </div>
    </div>
  );
}
