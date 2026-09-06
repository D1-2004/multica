"use client";

import { useMemo, useState } from "react";
import {
  AlertCircle,
  Blocks,
  Download,
  ExternalLink,
  Package,
  RefreshCw,
  Star,
  Trash2,
} from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import {
  dshPluginBindingsOptions,
  dshPluginCatalogOptions,
  dshPluginCategoriesOptions,
  dshPluginKeys,
  dshPluginListOptions,
  selectDshPluginAssignments,
  type DshPlugin,
  type DshPluginCatalogEntry,
} from "@multica/core/dsh-plugins";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { ImportDshPluginDialog } from "./import-dsh-plugin-dialog";

const CATALOG_PAGE_SIZE = 30;

/**
 * The DSH plugin surface.
 *
 * Two tabs, because there are exactly two honest things to show: what this
 * workspace has imported, and what the community index lists. DeepSeek ships no
 * plugin registry of its own, so the browse tab is explicit about being a
 * community source rather than an official one.
 */
export function DshPluginsPage() {
  const { t } = useT("dsh-plugins");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const paths = useWorkspacePaths();

  const [tab, setTab] = useState<"installed" | "market">("installed");
  const [installedQuery, setInstalledQuery] = useState("");
  const [marketQuery, setMarketQuery] = useState("");
  const [category, setCategory] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [presetSource, setPresetSource] = useState("");

  const installed = useQuery(dshPluginListOptions(wsId));
  const bindings = useQuery(dshPluginBindingsOptions(wsId));
  const agents = useQuery(agentListOptions(wsId));
  const categories = useQuery(dshPluginCategoriesOptions(wsId));
  const catalog = useQuery(
    dshPluginCatalogOptions(wsId, {
      query: marketQuery,
      category,
      offset: 0,
      limit: CATALOG_PAGE_SIZE,
    }),
  );

  const assignments = useMemo(
    () => selectDshPluginAssignments(bindings.data, agents.data),
    [bindings.data, agents.data],
  );

  const installedByPackage = useMemo(() => {
    const set = new Set<string>();
    for (const plugin of installed.data ?? []) set.add(plugin.packageName);
    return set;
  }, [installed.data]);

  const visibleInstalled = useMemo(() => {
    const needle = installedQuery.trim().toLowerCase();
    const rows = installed.data ?? [];
    if (!needle) return rows;
    return rows.filter(
      (plugin) =>
        plugin.packageName.toLowerCase().includes(needle) ||
        plugin.displayName.toLowerCase().includes(needle) ||
        plugin.description.toLowerCase().includes(needle),
    );
  }, [installed.data, installedQuery]);

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: dshPluginKeys.all(wsId) });
  };

  const refreshCatalog = useMutation({
    mutationFn: () => api.refreshDshPluginCatalog(),
    onSettled: invalidate,
  });

  const removePlugin = useMutation({
    mutationFn: (id: string) => api.deleteDshPlugin(id),
    onSettled: invalidate,
  });

  const state = catalog.data?.state;

  return (
    <Tabs
      value={tab}
      onValueChange={(value) => setTab(value as "installed" | "market")}
      className="flex h-full min-h-0 flex-col gap-0"
    >
      <CollectionPageHeader
        icon={Blocks}
        title={t(($) => $.page.title)}
        count={installed.data?.length}
        description={t(($) => $.page.tagline)}
        actions={
          <Button
            size="sm"
            onClick={() => {
              setPresetSource("");
              setImportOpen(true);
            }}
          >
            <Download />
            {t(($) => $.page.import)}
          </Button>
        }
      />

      <div className="h-12 shrink-0 overflow-x-auto border-b px-5">
        <div className="flex h-full w-max min-w-full items-center justify-between gap-2">
          <TabsList
            variant="line"
            className="gap-0 p-0 group-data-horizontal/tabs:h-full"
          >
            <TabsTrigger
              value="installed"
              className="h-full rounded-none px-2.5 text-label group-data-horizontal/tabs:after:bottom-0"
            >
              {t(($) => $.page.tab_installed)}
            </TabsTrigger>
            <TabsTrigger
              value="market"
              className="h-full rounded-none px-2.5 text-label group-data-horizontal/tabs:after:bottom-0"
            >
              {t(($) => $.page.tab_market)}
            </TabsTrigger>
          </TabsList>

          <div className="flex shrink-0 items-center gap-2">
            {tab === "installed" ? (
              <Input
                value={installedQuery}
                onChange={(event) => setInstalledQuery(event.target.value)}
                placeholder={t(($) => $.page.search_installed)}
                className="h-8 w-56"
              />
            ) : (
              <>
                <Input
                  value={marketQuery}
                  onChange={(event) => setMarketQuery(event.target.value)}
                  placeholder={t(($) => $.page.search_market)}
                  className="h-8 w-56"
                />
                <select
                  value={category}
                  onChange={(event) => setCategory(event.target.value)}
                  aria-label={t(($) => $.page.all_categories)}
                  className="h-8 rounded-md border bg-background px-2 text-caption"
                >
                  <option value="">{t(($) => $.page.all_categories)}</option>
                  {(categories.data ?? []).map((entry) => (
                    <option key={entry.category} value={entry.category}>
                      {entry.category} ({entry.entryCount})
                    </option>
                  ))}
                </select>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.page.refresh_catalog)}
                  onClick={() => refreshCatalog.mutate()}
                  disabled={refreshCatalog.isPending}
                >
                  <RefreshCw
                    className={refreshCatalog.isPending ? "animate-spin" : undefined}
                  />
                </Button>
              </>
            )}
          </div>
        </div>
      </div>

      <TabsContent value="installed" className="min-h-0 flex-1 overflow-y-auto">
        {installed.isPending ? (
          <ListSkeleton />
        ) : installed.isError ? (
          <CollectionPageState
            icon={AlertCircle}
            tone="destructive"
            title={t(($) => $.installed.error_title)}
            actions={
              <Button size="sm" variant="outline" onClick={() => void installed.refetch()}>
                {t(($) => $.installed.error_retry)}
              </Button>
            }
          />
        ) : visibleInstalled.length === 0 ? (
          <CollectionPageState
            icon={Package}
            title={t(($) => $.installed.empty_title)}
            description={t(($) => $.installed.empty_description)}
            actions={
              <Button size="sm" onClick={() => setTab("market")}>
                {t(($) => $.page.tab_market)}
              </Button>
            }
          />
        ) : (
          <ul className="divide-y">
            {visibleInstalled.map((plugin) => (
              <InstalledRow
                key={plugin.id}
                plugin={plugin}
                onOpen={() => navigation.push(paths.dshPluginDetail(plugin.id))}
                agentCount={assignments.get(plugin.id)?.length ?? 0}
                onRemove={() => {
                  if (
                    typeof window !== "undefined" &&
                    !window.confirm(
                      t(($) => $.installed.remove_confirm, {
                        name: plugin.packageName,
                      }),
                    )
                  ) {
                    return;
                  }
                  removePlugin.mutate(plugin.id);
                }}
                removing={
                  removePlugin.isPending && removePlugin.variables === plugin.id
                }
              />
            ))}
          </ul>
        )}
      </TabsContent>

      <TabsContent value="market" className="min-h-0 flex-1 overflow-y-auto">
        {/* Say where this data comes from, every time. DeepSeek publishes no
            plugin catalog, so presenting a community list without provenance
            would imply an endorsement that does not exist. */}
        {state?.sourceRepo ? (
          <p className="border-b px-5 py-2 text-caption text-muted-foreground">
            {t(($) => $.catalog.community_notice, {
              repo: state.sourceRepo,
              license: state.license,
            })}
            {state.entryCount > 0 ? (
              <>
                {" · "}
                {t(($) => $.catalog.state, {
                  count: state.entryCount,
                  version: state.catalogVersion || "—",
                })}
              </>
            ) : null}
          </p>
        ) : null}

        {catalog.isPending ? (
          <ListSkeleton />
        ) : (catalog.data?.entries.length ?? 0) === 0 ? (
          <CollectionPageState
            icon={Package}
            title={
              (state?.entryCount ?? 0) === 0
                ? t(($) => $.catalog.never_refreshed)
                : t(($) => $.catalog.empty_title)
            }
            description={
              (state?.entryCount ?? 0) === 0
                ? undefined
                : t(($) => $.catalog.empty_description)
            }
            actions={
              (state?.entryCount ?? 0) === 0 ? (
                <Button
                  size="sm"
                  onClick={() => refreshCatalog.mutate()}
                  disabled={refreshCatalog.isPending}
                >
                  {refreshCatalog.isPending
                    ? t(($) => $.catalog.refreshing)
                    : t(($) => $.catalog.refresh_now)}
                </Button>
              ) : undefined
            }
          />
        ) : (
          <ul className="divide-y">
            {(catalog.data?.entries ?? []).map((entry) => (
              <CatalogRow
                key={`${entry.owner}/${entry.name}`}
                entry={entry}
                installed={installedByPackage.has(entry.npmPackage)}
                onInstall={() => {
                  setPresetSource(entry.sourceSpec);
                  setImportOpen(true);
                }}
              />
            ))}
          </ul>
        )}
      </TabsContent>

      <ImportDshPluginDialog
        open={importOpen}
        onOpenChange={setImportOpen}
        initialSource={presetSource}
        onImported={invalidate}
      />
    </Tabs>
  );
}

function ListSkeleton() {
  return (
    <ul className="divide-y">
      {Array.from({ length: 6 }, (_, index) => (
        <li key={index} className="flex items-center gap-3 px-5 py-3">
          <Skeleton className="h-4 w-48" />
          <Skeleton className="h-4 w-24" />
          <Skeleton className="ml-auto h-4 w-16" />
        </li>
      ))}
    </ul>
  );
}

function InstalledRow({
  plugin,
  agentCount,
  onOpen,
  onRemove,
  removing,
}: {
  plugin: DshPlugin;
  agentCount: number;
  onOpen: () => void;
  onRemove: () => void;
  removing: boolean;
}) {
  const { t } = useT("dsh-plugins");
  return (
    <li className="flex items-start gap-3 px-5 py-3">
      <button
        type="button"
        onClick={onOpen}
        className="min-w-0 flex-1 rounded-md text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate font-medium">{plugin.packageName}</span>
          {plugin.resolvedVersion ? (
            <Badge variant="secondary" className="font-mono tabular-nums">
              {plugin.resolvedVersion}
            </Badge>
          ) : null}
          <Badge variant="outline">{plugin.sourceKind}</Badge>
          {/* An unpinned plugin can change under the workspace between tasks,
              which is worth seeing at a glance rather than in a detail pane. */}
          {plugin.integrity ? null : (
            <Badge variant="outline" className="text-muted-foreground">
              {t(($) => $.installed.unpinned)}
            </Badge>
          )}
        </div>
        {plugin.description ? (
          <p className="mt-0.5 line-clamp-2 text-caption text-muted-foreground">
            {plugin.description}
          </p>
        ) : null}
        <p className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-caption text-muted-foreground">
          {plugin.bundleRows.length > 0 ? (
            <span>
              {t(($) => $.installed.rows)}: {plugin.bundleRows.join(", ")}
            </span>
          ) : null}
          <span>
            {agentCount === 0
              ? t(($) => $.installed.used_by_none)
              : t(($) => $.installed.used_by_count, { count: agentCount })}
          </span>
        </p>
      </button>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={t(($) => $.installed.remove)}
        onClick={onRemove}
        disabled={removing}
      >
        <Trash2 />
      </Button>
    </li>
  );
}

function CatalogRow({
  entry,
  installed,
  onInstall,
}: {
  entry: DshPluginCatalogEntry;
  installed: boolean;
  onInstall: () => void;
}) {
  const { t } = useT("dsh-plugins");
  const description = entry.descriptionEn || entry.descriptionZh;
  return (
    <li className="flex items-start gap-3 px-5 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate font-medium">{entry.name}</span>
          {entry.npmVersion ? (
            <Badge variant="secondary" className="font-mono tabular-nums">
              {entry.npmVersion}
            </Badge>
          ) : null}
          {entry.category ? (
            <Badge variant="outline">{entry.category}</Badge>
          ) : null}
          {entry.stars > 0 ? (
            <span className="flex items-center gap-1 text-caption text-muted-foreground tabular-nums">
              <Star className="size-3" aria-hidden="true" />
              {entry.stars}
            </span>
          ) : null}
        </div>
        {description ? (
          <p className="mt-0.5 line-clamp-2 text-caption text-muted-foreground">
            {description}
          </p>
        ) : null}
        {entry.url ? (
          <a
            href={entry.url}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-1 inline-flex items-center gap-1 text-caption text-muted-foreground underline-offset-4 hover:underline"
          >
            {entry.owner}/{entry.name}
            <ExternalLink className="size-3" aria-hidden="true" />
          </a>
        ) : null}
      </div>
      {installed ? (
        <Badge variant="secondary">{t(($) => $.catalog.installed)}</Badge>
      ) : (
        <Button
          size="sm"
          variant="outline"
          onClick={onInstall}
          disabled={!entry.sourceSpec}
        >
          {t(($) => $.catalog.install)}
        </Button>
      )}
    </li>
  );
}
