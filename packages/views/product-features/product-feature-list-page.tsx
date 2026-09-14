"use client";

import { useMemo, useState } from "react";
import { AlertCircle, Megaphone, Search } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { productFeatureReleaseListOptions } from "@multica/core/product-features";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { CollectionPageHeader, CollectionPageState } from "../layout/collection-page";
import { useDebouncedValue } from "../common/use-debounced-value";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

function formatPublishedAt(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(date);
}

export function ProductFeatureListPage() {
  const { t } = useT("product-features");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search, 250);
  const releases = useQuery(productFeatureReleaseListOptions(debouncedSearch));
  const ordered = useMemo(
    () =>
      [...(releases.data?.releases ?? [])].sort(
        (left, right) =>
          new Date(right.publishedAt).getTime() - new Date(left.publishedAt).getTime(),
      ),
    [releases.data?.releases],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Megaphone}
        title={t(($) => $.page.title)}
        count={releases.data?.total}
        description={t(($) => $.page.tagline)}
      />

      <div className="relative shrink-0 border-b px-5 py-2.5">
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 left-7 size-3.5 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder={t(($) => $.page.search_placeholder)}
          aria-label={t(($) => $.page.search_placeholder)}
          className="max-w-md pl-8"
        />
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {releases.isPending ? (
          <div role="status" aria-label={t(($) => $.page.loading)} className="space-y-3 p-5">
            {Array.from({ length: 5 }, (_, index) => (
              <Skeleton key={index} className="h-24 w-full" />
            ))}
          </div>
        ) : releases.isError ? (
          <CollectionPageState
            role="alert"
            icon={AlertCircle}
            tone="destructive"
            title={t(($) => $.page.error_title)}
            actions={
              <Button size="sm" variant="outline" onClick={() => void releases.refetch()}>
                {t(($) => $.page.error_retry)}
              </Button>
            }
          />
        ) : ordered.length === 0 ? (
          <CollectionPageState
            icon={Search}
            title={t(($) => $.page.empty_title)}
            description={t(($) => $.page.empty_description)}
          />
        ) : (
          <ul className="divide-y">
            {ordered.map((release) => (
              <li key={release.id}>
                <button
                  type="button"
                  onClick={() => navigation.push(paths.featureUpdateDetail(release.id))}
                  className="flex w-full items-start gap-4 px-5 py-4 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
                >
                  <div className="min-w-0 flex-1">
                    <div className="mb-1.5 flex flex-wrap items-center gap-2">
                      <h2 className="text-body font-medium text-foreground">{release.title}</h2>
                      <Badge variant={release.releaseType === "new" ? "default" : "secondary"}>
                        {release.releaseType === "new"
                          ? t(($) => $.release.new)
                          : t(($) => $.release.improvement)}
                      </Badge>
                      {release.versionLabel ? (
                        <Badge variant="outline">{release.versionLabel}</Badge>
                      ) : null}
                    </div>
                    <p className="line-clamp-2 text-title-sm leading-5 text-muted-foreground">
                      {release.description}
                    </p>
                  </div>
                  <time
                    dateTime={release.publishedAt}
                    className="shrink-0 pt-0.5 text-caption tabular-nums text-muted-foreground"
                  >
                    {formatPublishedAt(release.publishedAt)}
                  </time>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
