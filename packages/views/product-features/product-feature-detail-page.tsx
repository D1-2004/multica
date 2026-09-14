"use client";

import { AlertCircle, ArrowLeft, CheckCircle2, History, Megaphone, RefreshCw } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { productFeatureReleaseDetailOptions } from "@multica/core/product-features";
import type { ProductFeatureRelease } from "@multica/core/product-features";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { CollectionPageHeader, CollectionPageState } from "../layout/collection-page";
import { useNavigation } from "../navigation";
import { RichContent } from "../rich-content";
import { useT } from "../i18n";

function formatPublishedAt(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(date);
}

function DetailSection({ title, content }: { title: string; content: string }) {
  return (
    <section className="space-y-3">
      <h2 className="text-body font-medium">{title}</h2>
      <div className="rounded-lg border bg-card px-4 py-3">
        <RichContent content={content} density="document" phase="settled" />
      </div>
    </section>
  );
}

function ImageRequirement({ release }: { release: ProductFeatureRelease }) {
  const { t } = useT("product-features");
  const needsUpgrade = release.requiresImageUpgrade;
  const message = release.imageRequirement === "latest_at_publish"
    ? t(($) => $.detail.image_latest_at_publish)
    : release.imageRequirement === "min_version"
      ? t(($) => $.detail.image_min_version)
      : t(($) => $.detail.image_none);
  const Icon = needsUpgrade ? RefreshCw : CheckCircle2;

  return (
    <section className="space-y-3">
      <h2 className="text-body font-medium">{t(($) => $.detail.image_requirement)}</h2>
      <div className="flex items-start gap-3 rounded-lg border bg-card px-4 py-3">
        <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0">
          <p className="text-title-sm">{message}</p>
          {release.requiredImageVersion ? (
            <p className="mt-1 break-all font-mono text-caption text-muted-foreground">
              {release.requiredImageVersion}
            </p>
          ) : null}
        </div>
      </div>
    </section>
  );
}

export function ProductFeatureDetailPage({ releaseId }: { releaseId: string }) {
  const { t } = useT("product-features");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const release = useQuery(productFeatureReleaseDetailOptions(releaseId));

  if (release.isPending) {
    return (
      <div role="status" aria-label={t(($) => $.page.loading)} className="space-y-4 p-5">
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }

  if (release.isError) {
    return (
      <CollectionPageState
        role="alert"
        icon={AlertCircle}
        tone="destructive"
        title={t(($) => $.detail.error_title)}
        actions={
          <Button size="sm" variant="outline" onClick={() => void release.refetch()}>
            {t(($) => $.page.error_retry)}
          </Button>
        }
      />
    );
  }

  if (!release.data?.id) {
    return (
      <CollectionPageState icon={Megaphone} title={t(($) => $.detail.not_found)} />
    );
  }

  const item = release.data;
  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Megaphone}
        title={item.title}
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigation.push(paths.featureUpdates())}
          >
            <ArrowLeft />
            {t(($) => $.detail.back)}
          </Button>
        }
      />

      <div className="flex flex-wrap items-center gap-2 border-b px-5 py-2.5">
        <Badge variant={item.releaseType === "new" ? "default" : "secondary"}>
          {item.releaseType === "new"
            ? t(($) => $.release.new)
            : t(($) => $.release.improvement)}
        </Badge>
        {item.versionLabel ? <Badge variant="outline">{item.versionLabel}</Badge> : null}
        <time dateTime={item.publishedAt} className="text-caption tabular-nums text-muted-foreground">
          {formatPublishedAt(item.publishedAt)}
        </time>
      </div>

      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-7 px-5 py-6">
          <DetailSection title={t(($) => $.detail.description)} content={item.description} />
          <DetailSection title={t(($) => $.detail.use_cases)} content={item.useCases} />
          <DetailSection title={t(($) => $.detail.usage_guide)} content={item.usageGuide} />
          <ImageRequirement release={item} />

          {item.previousRelease ? (
            <section className="space-y-3">
              <h2 className="text-body font-medium">{t(($) => $.detail.previous_version)}</h2>
              <button
                type="button"
                onClick={() => navigation.push(paths.featureUpdateDetail(item.previousRelease!.id))}
                className="flex w-full items-center gap-3 rounded-lg border bg-card px-4 py-3 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <History aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate text-title-sm font-medium">
                  {item.previousRelease.title}
                </span>
                {item.previousRelease.versionLabel ? (
                  <Badge variant="outline">{item.previousRelease.versionLabel}</Badge>
                ) : null}
                <time
                  dateTime={item.previousRelease.publishedAt}
                  className="shrink-0 text-caption tabular-nums text-muted-foreground"
                >
                  {formatPublishedAt(item.previousRelease.publishedAt)}
                </time>
              </button>
            </section>
          ) : null}
        </div>
      </main>
    </div>
  );
}
