"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Globe2, Loader2, Share2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  hostedSiteListOptions,
  useDeleteHostedSite,
  type HostedSite,
} from "@multica/core/sitehosting";
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
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";

function siteStatusLabel(
  status: string,
  labels: { active: string; pending: string; failed: string },
) {
  switch (status) {
    case "active":
      return labels.active;
    case "pending":
    case "uploading":
      return labels.pending;
    case "failed":
      return labels.failed;
    default:
      return status;
  }
}

function statusVariant(status: string): "secondary" | "destructive" | "outline" {
  if (status === "active") return "secondary";
  if (status === "failed") return "destructive";
  return "outline";
}

function HostedSiteCard({
  site,
  onShare,
  onDelete,
}: {
  site: HostedSite;
  onShare: (site: HostedSite) => void;
  onDelete: (site: HostedSite) => void;
}) {
  const { t } = useT("settings");
  const status = site.latestStatus || site.status;

  return (
    <Card className="shadow-none">
      <CardContent className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-3">
          <div className="shrink-0 rounded-lg border border-surface-border bg-muted/50 p-2 text-muted-foreground">
            <Globe2 className="size-4" />
          </div>
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <p className="font-mono text-body font-medium">
                {site.publicSiteId}
              </p>
              <Badge variant={statusVariant(status)}>
                {siteStatusLabel(status, {
                  active: t(($) => $.hosted_sites.status_active),
                  pending: t(($) => $.hosted_sites.status_pending),
                  failed: t(($) => $.hosted_sites.status_failed),
                })}
              </Badge>
            </div>
            <p className="break-all text-caption text-muted-foreground">
              {site.siteUrl}
            </p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.hosted_sites.updated, {
                time: new Date(site.updatedAt).toLocaleString(),
              })}
            </p>
            {site.latestError ? (
              <p className="text-caption text-destructive">{site.latestError}</p>
            ) : null}
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2 sm:justify-end">
          <Button
            variant="outline"
            size="sm"
            render={
              <a href={site.siteUrl} target="_blank" rel="noopener noreferrer" />
            }
          >
            <ExternalLink data-icon="inline-start" />
            {t(($) => $.hosted_sites.open)}
          </Button>
          <Button variant="outline" size="sm" onClick={() => onShare(site)}>
            <Share2 data-icon="inline-start" />
            {t(($) => $.hosted_sites.share)}
          </Button>
          <Button variant="destructive" size="sm" onClick={() => onDelete(site)}>
            <Trash2 data-icon="inline-start" />
            {t(($) => $.hosted_sites.delete)}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

export function HostedSitesTab() {
  const { t } = useT("settings");
  const sitesQuery = useQuery(hostedSiteListOptions());
  const deleteSite = useDeleteHostedSite();
  const [deleteTarget, setDeleteTarget] = useState<HostedSite | null>(null);

  async function handleShare(site: HostedSite) {
    if (await copyText(site.siteUrl)) {
      toast.success(t(($) => $.hosted_sites.copy_success));
    } else {
      toast.error(t(($) => $.hosted_sites.copy_failed));
    }
  }

  async function handleDelete() {
    if (!deleteTarget || deleteSite.isPending) return;
    try {
      await deleteSite.mutateAsync(deleteTarget.siteId);
      setDeleteTarget(null);
      toast.success(t(($) => $.hosted_sites.delete_success));
    } catch {
      toast.error(t(($) => $.hosted_sites.delete_failed));
    }
  }

  const sites = sitesQuery.data ?? [];

  return (
    <SettingsTab
      title={t(($) => $.hosted_sites.title)}
      description={t(($) => $.hosted_sites.description)}
    >
      <SettingsSection
        title={t(($) => $.hosted_sites.list_title)}
        description={t(($) => $.hosted_sites.list_description)}
      >
        {sitesQuery.isLoading ? (
          <Card className="shadow-none">
            <CardContent className="flex items-center gap-2 text-body text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              {t(($) => $.hosted_sites.loading)}
            </CardContent>
          </Card>
        ) : sitesQuery.isError ? (
          <Card className="shadow-none">
            <CardContent className="text-body text-destructive">
              {t(($) => $.hosted_sites.load_failed)}
            </CardContent>
          </Card>
        ) : sites.length === 0 ? (
          <Card className="shadow-none">
            <CardContent className="space-y-1 text-center">
              <p className="text-body font-medium">
                {t(($) => $.hosted_sites.empty_title)}
              </p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.hosted_sites.empty_description)}
              </p>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-3">
            {sites.map((site) => (
              <HostedSiteCard
                key={site.siteId}
                site={site}
                onShare={handleShare}
                onDelete={setDeleteTarget}
              />
            ))}
          </div>
        )}
      </SettingsSection>

      <AlertDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.hosted_sites.delete_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.hosted_sites.delete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteSite.isPending}>
              {t(($) => $.hosted_sites.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={deleteSite.isPending}
              onClick={(event) => {
                event.preventDefault();
                void handleDelete();
              }}
            >
              {deleteSite.isPending ? (
                <>
                  <Loader2 className="size-4 animate-spin" />
                  {t(($) => $.hosted_sites.deleting)}
                </>
              ) : (
                t(($) => $.hosted_sites.delete_confirm)
              )}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
