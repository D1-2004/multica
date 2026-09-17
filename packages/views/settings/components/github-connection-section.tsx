"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
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
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import {
  githubInstallationsOptions,
} from "@multica/core/github";
import { api } from "@multica/core/api";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { GitHubMark } from "./github-mark";
import { githubConnectionErrorField } from "./github-connection-error";

export function GitHubConnectionSection() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  // `canView` gates the read-only installation list (every workspace member
  // sees it after MUL-2413); `canManage` gates the Connect / Disconnect
  // actions and comes from the backend response (`can_manage`) so the
  // frontend never claims management rights the server would reject.
  const canView = !!currentMember;

  const { data: installationData } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId && canView,
  });
  const installations = installationData?.installations ?? [];
  const reusableInstallations = installationData?.reusable_installations ?? [];
  const configured = installationData?.configured ?? false;
  const canManage = installationData?.can_manage === true;
  const connected = installations.length > 0;
  const primaryInstallation = installations[0] ?? null;

  useEffect(() => {
    const error = navigation.searchParams.get("github_error");
    const connected = navigation.searchParams.get("github_connected") === "1";
    if (!error && !connected) return;
    if (error) {
      toast.error(t(($) => $.github[githubConnectionErrorField(error)]));
    } else {
      void qc.invalidateQueries({ queryKey: ["github", wsId] });
      void qc.invalidateQueries({ queryKey: ["git-repo", wsId] });
      toast.success(t(($) => $.github.toast_connected));
    }
    const next = new URLSearchParams(navigation.searchParams);
    next.set("tab", "repositories");
    next.set("section", "connections");
    next.delete("github_error");
    next.delete("github_connected");
    const search = next.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  }, [navigation, qc, t, wsId]);

  const [connecting, setConnecting] = useState(false);
  const [reusingId, setReusingId] = useState<string | null>(null);
  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleConnect() {
    setConnecting(true);
    try {
      const resp = await api.getGitHubConnectURL(wsId);
      if (!resp.configured || !resp.url) {
        toast.error(t(($) => $.github.toast_not_configured));
        return;
      }
      window.open(resp.url, "_blank", "noopener");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_open_failed));
    } finally {
      setConnecting(false);
    }
  }

  async function handleReuse(sourceInstallationId: string) {
    if (reusingId) return;
    setReusingId(sourceInstallationId);
    try {
      await api.reuseGitHubInstallation(wsId, sourceInstallationId);
      await qc.invalidateQueries({ queryKey: ["github", wsId] });
      await qc.invalidateQueries({ queryKey: ["git-repo", wsId] });
      toast.success(t(($) => $.github.toast_reused));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_reuse_failed));
    } finally {
      setReusingId(null);
    }
  }

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteGitHubInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: ["github", wsId] });
      await qc.invalidateQueries({ queryKey: ["git-repo", wsId] });
      toast.success(t(($) => $.github.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.github.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  if (!workspace) return null;

  return (
    <div className="space-y-4">
      <section className="space-y-3">
        <Card>
          <CardContent className="space-y-4">
            <div className="flex items-start justify-between gap-4">
              <div className="flex items-start gap-3">
                <GitHubMark className="h-6 w-6 mt-0.5 shrink-0" />
                <div className="space-y-1">
                  <p className="text-body font-medium">{t(($) => $.github.connection_title)}</p>
                  {connected ? (
                    <>
                      <p className="text-caption text-muted-foreground">
                        {t(($) => $.github.connected_to, {
                          login: installations.map((i) => i.account_login).join(", "),
                        })}
                      </p>
                      {primaryInstallation?.connected_by && (
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.github.connected_by, {
                            name: primaryInstallation.connected_by!,
                          })}
                        </p>
                      )}
                    </>
                  ) : canManage ? (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.github.connection_description)}
                    </p>
                  ) : (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.github.contact_admin_to_connect)}
                    </p>
                  )}
                </div>
              </div>
              {canManage && (
                <div className="flex items-center gap-2">
                  {connected && primaryInstallation ? (
                    // Disconnect must stay reachable even when the master switch
                    // is off — disconnect is a separate intent (revoke the App
                    // grant) from hiding the feature.
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setDisconnectTarget(primaryInstallation.id)}
                    >
                      {t(($) => $.github.disconnect)}
                    </Button>
                  ) : (
                    <Button
                      size="sm"
                      onClick={handleConnect}
                      disabled={connecting || !configured}
                      title={
                        !configured
                          ? t(($) => $.github.connect_disabled_tooltip)
                          : undefined
                      }
                    >
                      {connecting
                        ? t(($) => $.github.connect_opening)
                        : t(($) => $.github.connect_github)}
                    </Button>
                  )}
                </div>
              )}
            </div>

            {canManage && !connected && reusableInstallations.length > 0 && (
              <div className="space-y-3 border-t pt-4">
                <div className="space-y-1">
                  <p className="text-body font-medium">
                    {t(($) => $.github.reusable_title)}
                  </p>
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.github.reusable_description)}
                  </p>
                </div>
                <div className="space-y-2">
                  {reusableInstallations.map((installation) => (
                    <div
                      key={installation.id}
                      className="flex items-center justify-between gap-3 rounded-md border px-3 py-2"
                    >
                      <div className="min-w-0">
                        <p className="truncate text-body font-medium">
                          {installation.account_login}
                        </p>
                        <p className="truncate text-caption text-muted-foreground">
                          {t(($) => $.github.reusable_from, {
                            workspace: installation.source_workspace_name,
                          })}
                        </p>
                      </div>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => handleReuse(installation.id)}
                        disabled={reusingId !== null || !configured}
                        aria-label={t(($) => $.github.reuse_connection_aria, {
                          login: installation.account_login,
                          workspace: installation.source_workspace_name,
                        })}
                      >
                        {reusingId === installation.id
                          ? t(($) => $.github.reusing_connection)
                          : t(($) => $.github.reuse_connection)}
                      </Button>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {canManage && !configured && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.not_configured)}{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-micro">GITHUB_APP_SLUG</code>{" "}
                {t(($) => $.github.not_configured_and)}{" "}
                <code className="rounded bg-muted px-1 py-0.5 text-micro">GITHUB_WEBHOOK_SECRET</code>.
              </p>
            )}

            {!canManage && connected && (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.github.read_only_hint)}
              </p>
            )}
          </CardContent>
        </Card>
      </section>
      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.github.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.github.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.github.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.github.disconnecting)
                : t(($) => $.github.disconnect_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
