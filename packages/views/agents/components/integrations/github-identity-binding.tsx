"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GitFork, RefreshCw, ShieldCheck } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentIdentityGithubStatusOptions,
  useBeginAgentIdentityGitHubOAuth,
  useTestAgentIdentityGitHubConnection,
} from "@multica/core/agent-identity-github";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

function currentReturnPath(): string {
  if (typeof window === "undefined") return "/";
  const url = new URL(window.location.href);
  url.searchParams.delete("github_connection");
  url.searchParams.delete("connection_id");
  return `${url.pathname}${url.search}${url.hash}`;
}

function clearOAuthReturnParams() {
  if (typeof window === "undefined") return;
  const url = new URL(window.location.href);
  if (!url.searchParams.has("github_connection")) return;
  url.searchParams.delete("github_connection");
  url.searchParams.delete("connection_id");
  window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
}

function formatDateTime(value?: number | null): string | null {
  if (!value) return null;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return null;
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function statusLabel(status: string, active: string, reauth: string, fallback: string): string {
  const normalized = status.trim().toUpperCase();
  if (normalized === "ACTIVE") return active;
  if (normalized === "NEEDS_REAUTH") return reauth;
  return fallback;
}

export function GitHubIdentityBindingCard({
  agentId,
  canManage,
}: {
  agentId: string;
  canManage: boolean;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionMessage, setActionMessage] = useState<string | null>(null);
  const { data, isPending, refetch } = useQuery({
    ...agentIdentityGithubStatusOptions(wsId, agentId),
    enabled: !!wsId && !!agentId && canManage,
  });
  const beginOAuth = useBeginAgentIdentityGitHubOAuth(wsId);
  const testConnection = useTestAgentIdentityGitHubConnection(wsId);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const params = new URLSearchParams(window.location.search);
    if (params.get("github_connection") !== "success") return;
    setActionMessage(t(($) => $.tab_body.integrations.github_identity_connected));
    setActionError(null);
    clearOAuthReturnParams();
    void refetch();
  }, [refetch, t]);

  const connection = data?.connection ?? null;
  const refreshExpiresAt = useMemo(
    () => formatDateTime(connection?.refreshExpiresAt),
    [connection?.refreshExpiresAt],
  );
  const lastTestAt = useMemo(
    () => formatDateTime(connection?.lastTestAt),
    [connection?.lastTestAt],
  );
  const isReauth = connection?.status?.trim().toUpperCase() === "NEEDS_REAUTH";

  async function connect() {
    setActionError(null);
    setActionMessage(null);
    try {
      const response = await beginOAuth.mutateAsync({
        agentId,
        returnUrl: currentReturnPath(),
      });
      if (!response.authorizationUrl) {
        setActionError(t(($) => $.tab_body.integrations.github_identity_connect_failed));
        return;
      }
      window.location.assign(response.authorizationUrl);
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.github_identity_connect_failed),
        ),
      );
    }
  }

  async function test() {
    if (!connection) return;
    setActionError(null);
    setActionMessage(null);
    try {
      const response = await testConnection.mutateAsync({
        agentId,
        connectionId: connection.connectionId,
      });
      setActionMessage(
        response.refreshed
          ? t(($) => $.tab_body.integrations.github_identity_test_refreshed)
          : t(($) => $.tab_body.integrations.github_identity_test_ok),
      );
    } catch (error) {
      setActionError(
        errorMessage(
          error,
          t(($) => $.tab_body.integrations.github_identity_test_failed),
        ),
      );
    }
  }

  return (
    <section className="rounded-lg border" data-testid="github-identity-binding-card">
      <div className="flex items-start gap-3 p-4">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
          <GitFork className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1 space-y-1">
          <h3 className="text-sm font-medium">
            {t(($) => $.tab_body.integrations.github_identity_title)}
          </h3>
          <p className="text-xs leading-relaxed text-muted-foreground">
            {t(($) => $.tab_body.integrations.github_identity_description)}
          </p>
        </div>
      </div>

      <div className="border-t px-4 py-3">
        {!canManage ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.github_identity_members_note)}
          </p>
        ) : isPending ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.github_identity_loading)}
          </p>
        ) : data?.configured !== true ? (
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.integrations.github_identity_not_configured)}
          </p>
        ) : connection ? (
          <div className="space-y-3">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0 space-y-1">
                <p className="truncate text-sm font-medium">@{connection.accountLogin}</p>
                <div className="space-y-1 text-xs text-muted-foreground">
                  <p>
                    {t(($) => $.tab_body.integrations.github_identity_status)}:{" "}
                    {statusLabel(
                      connection.status,
                      t(($) => $.tab_body.integrations.github_identity_status_active),
                      t(($) => $.tab_body.integrations.github_identity_status_reauth),
                      connection.status,
                    )}
                  </p>
                  {connection.grantedScopes ? (
                    <p>
                      {t(($) => $.tab_body.integrations.github_identity_scopes)}:{" "}
                      {connection.grantedScopes}
                    </p>
                  ) : null}
                  {refreshExpiresAt ? (
                    <p>
                      {t(($) => $.tab_body.integrations.github_identity_refresh_expires)}:{" "}
                      {refreshExpiresAt}
                    </p>
                  ) : null}
                  {lastTestAt ? (
                    <p>
                      {t(($) => $.tab_body.integrations.github_identity_last_test)}:{" "}
                      {lastTestAt}
                    </p>
                  ) : null}
                </div>
              </div>
              <div className="flex shrink-0 flex-wrap gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={test}
                  disabled={testConnection.isPending || isReauth}
                >
                  <ShieldCheck className="mr-2 h-4 w-4" />
                  {testConnection.isPending
                    ? t(($) => $.tab_body.integrations.github_identity_testing)
                    : t(($) => $.tab_body.integrations.github_identity_test)}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant={isReauth ? "default" : "outline"}
                  onClick={connect}
                  disabled={beginOAuth.isPending}
                >
                  <RefreshCw className="mr-2 h-4 w-4" />
                  {beginOAuth.isPending
                    ? t(($) => $.tab_body.integrations.github_identity_connecting)
                    : t(($) => $.tab_body.integrations.github_identity_reconnect)}
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <Button
            type="button"
            size="sm"
            onClick={connect}
            disabled={beginOAuth.isPending}
          >
            <GitFork className="mr-2 h-4 w-4" />
            {beginOAuth.isPending
              ? t(($) => $.tab_body.integrations.github_identity_connecting)
              : t(($) => $.tab_body.integrations.github_identity_connect)}
          </Button>
        )}
        {actionMessage ? (
          <p className="mt-3 text-xs text-emerald-600">{actionMessage}</p>
        ) : null}
        {actionError ? (
          <p className="mt-3 text-xs text-destructive">{actionError}</p>
        ) : null}
      </div>
    </section>
  );
}
