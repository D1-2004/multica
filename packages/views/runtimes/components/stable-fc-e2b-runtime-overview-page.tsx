"use client";

import { useMemo, useState } from "react";
import {
  Activity,
  Boxes,
  CheckCircle2,
  CircleAlert,
  Cloud,
  Layers3,
  RefreshCw,
  Search,
  ShieldCheck,
  TriangleAlert,
  WifiOff,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type {
  FCE2BStableRelease,
  FCE2BStableRuntimeOverview,
  FCE2BStableTemplateBinding,
} from "@multica/core/runtimes";
import {
  useFCE2BStableChannel,
  useFCE2BStableRuntimes,
} from "@multica/core/runtimes";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Progress } from "@multica/ui/components/ui/progress";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { cn } from "@multica/ui/lib/utils";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { CollectionPageState } from "../../layout/collection-page";
import { useT } from "../../i18n";
import { ProviderLogo } from "./provider-logo";
import { StableFCE2BReleaseDialog } from "./stable-fc-e2b-release-dialog";

const ALL = "__all__";

type Alignment = "current" | "active" | "outdated" | "candidate";
type AlignmentFilter = typeof ALL | Alignment;
type HealthFilter = typeof ALL | "online" | "offline";

interface TemplateDistribution {
  key: string;
  alias: string;
  templateId: string;
  buildId: string;
  runtimeCount: number;
  onlineCount: number;
  workspaceCount: number;
  matchesCurrentStable: boolean;
  matchesActiveRelease: boolean;
}

function templateKey(runtime: FCE2BStableRuntimeOverview): string {
  return `${runtime.template_id}:${runtime.template_build_id}`;
}

function runtimeAlignment(
  runtime: FCE2BStableRuntimeOverview,
): Alignment {
  if (runtime.template_channel === "candidate") return "candidate";
  if (runtime.matches_active_release) return "active";
  if (runtime.matches_current_stable) return "current";
  return "outdated";
}

function formatDateTime(value: string | number, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return typeof value === "string" ? value : "";
  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(date);
}

function releaseProgress(release: FCE2BStableRelease | null): {
  updated: number;
  total: number;
  percentage: number;
} {
  if (!release) return { updated: 0, total: 0, percentage: 0 };
  const developerPhase =
    release.status === "developer_rollout" ||
    release.status === "awaiting_rollout";
  const updated = developerPhase
    ? release.developer_updated_targets
    : release.updated_targets;
  const total = developerPhase
    ? release.developer_targets
    : release.total_targets;
  return {
    updated,
    total,
    percentage:
      total > 0
        ? Math.round((updated / total) * 100)
        : release.target_percentage,
  };
}

export function StableFCE2BRuntimeOverviewPage() {
  const { t, i18n } = useT("runtimes");
  const paths = useWorkspacePaths();
  const channelQuery = useFCE2BStableChannel();
  const canPublish = channelQuery.data?.can_publish === true;
  const runtimesQuery = useFCE2BStableRuntimes(canPublish);
  const [releaseDialogOpen, setReleaseDialogOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [workspaceFilter, setWorkspaceFilter] = useState(ALL);
  const [providerFilter, setProviderFilter] = useState(ALL);
  const [healthFilter, setHealthFilter] = useState<HealthFilter>(ALL);
  const [alignmentFilter, setAlignmentFilter] =
    useState<AlignmentFilter>(ALL);
  const [templateFilter, setTemplateFilter] = useState(ALL);

  const runtimes = useMemo(
    () => runtimesQuery.data ?? [],
    [runtimesQuery.data],
  );
  const current = channelQuery.data?.current ?? null;
  const active = channelQuery.data?.active_release ?? null;
  const activeProgress = releaseProgress(active);

  const workspaces = useMemo(
    () =>
      Array.from(
        new Map(
          runtimes.map((runtime) => [
            runtime.workspace_id,
            runtime.workspace_name,
          ]),
        ),
      ).sort((a, b) => a[1].localeCompare(b[1], i18n.language)),
    [i18n.language, runtimes],
  );
  const providers = useMemo(
    () =>
      Array.from(new Set(runtimes.map((runtime) => runtime.provider))).sort(),
    [runtimes],
  );

  const metrics = useMemo(() => {
    const stableManaged = runtimes.filter(
      (runtime) => runtime.template_channel === "stable",
    );
    return {
      total: runtimes.length,
      workspaceCount: new Set(runtimes.map((runtime) => runtime.workspace_id))
        .size,
      online: runtimes.filter(
        (runtime) => runtime.status.toLowerCase() === "online",
      ).length,
      stableManaged: stableManaged.length,
      current: stableManaged.filter(
        (runtime) => runtime.matches_current_stable,
      ).length,
      active: stableManaged.filter(
        (runtime) => runtime.matches_active_release,
      ).length,
      outdated: stableManaged.filter(
        (runtime) =>
          !runtime.matches_current_stable &&
          !runtime.matches_active_release,
      ).length,
      candidate: runtimes.filter(
        (runtime) => runtime.template_channel === "candidate",
      ).length,
    };
  }, [runtimes]);

  const stableCoverage =
    metrics.stableManaged > 0
      ? Math.round((metrics.current / metrics.stableManaged) * 100)
      : 0;

  const templateDistribution = useMemo(() => {
    const grouped = new Map<
      string,
      TemplateDistribution & { workspaceIds: Set<string> }
    >();
    for (const runtime of runtimes) {
      const key = templateKey(runtime);
      const item = grouped.get(key) ?? {
        key,
        alias: runtime.template_alias,
        templateId: runtime.template_id,
        buildId: runtime.template_build_id,
        runtimeCount: 0,
        onlineCount: 0,
        workspaceCount: 0,
        workspaceIds: new Set<string>(),
        matchesCurrentStable: false,
        matchesActiveRelease: false,
      };
      item.runtimeCount += 1;
      if (runtime.status.toLowerCase() === "online") item.onlineCount += 1;
      item.workspaceIds.add(runtime.workspace_id);
      item.workspaceCount = item.workspaceIds.size;
      item.matchesCurrentStable ||= runtime.matches_current_stable;
      item.matchesActiveRelease ||= runtime.matches_active_release;
      grouped.set(key, item);
    }
    return Array.from(grouped.values())
      .map((item) => ({
        key: item.key,
        alias: item.alias,
        templateId: item.templateId,
        buildId: item.buildId,
        runtimeCount: item.runtimeCount,
        onlineCount: item.onlineCount,
        workspaceCount: item.workspaceCount,
        matchesCurrentStable: item.matchesCurrentStable,
        matchesActiveRelease: item.matchesActiveRelease,
      }))
      .sort((a, b) => {
        if (a.matchesCurrentStable !== b.matchesCurrentStable) {
          return a.matchesCurrentStable ? -1 : 1;
        }
        if (a.matchesActiveRelease !== b.matchesActiveRelease) {
          return a.matchesActiveRelease ? -1 : 1;
        }
        return b.runtimeCount - a.runtimeCount;
      });
  }, [runtimes]);

  const filteredRuntimes = useMemo(() => {
    const normalizedSearch = search.trim().toLowerCase();
    return runtimes.filter((runtime) => {
      if (
        normalizedSearch &&
        ![
          runtime.workspace_name,
          runtime.runtime_name,
          runtime.provider,
          runtime.template_alias,
          runtime.template_id,
          runtime.template_build_id,
        ].some((value) => value.toLowerCase().includes(normalizedSearch))
      ) {
        return false;
      }
      if (
        workspaceFilter !== ALL &&
        runtime.workspace_id !== workspaceFilter
      ) {
        return false;
      }
      if (providerFilter !== ALL && runtime.provider !== providerFilter) {
        return false;
      }
      const health =
        runtime.status.toLowerCase() === "online" ? "online" : "offline";
      if (healthFilter !== ALL && health !== healthFilter) return false;
      if (
        alignmentFilter !== ALL &&
        runtimeAlignment(runtime) !== alignmentFilter
      ) {
        return false;
      }
      return templateFilter === ALL || templateKey(runtime) === templateFilter;
    });
  }, [
    alignmentFilter,
    healthFilter,
    providerFilter,
    runtimes,
    search,
    templateFilter,
    workspaceFilter,
  ]);

  const isRefreshing = channelQuery.isFetching || runtimesQuery.isFetching;
  const lastUpdatedAt = Math.max(
    channelQuery.dataUpdatedAt,
    runtimesQuery.dataUpdatedAt,
  );

  const refresh = async () => {
    await Promise.all([channelQuery.refetch(), runtimesQuery.refetch()]);
  };

  const clearFilters = () => {
    setSearch("");
    setWorkspaceFilter(ALL);
    setProviderFilter(ALL);
    setHealthFilter(ALL);
    setAlignmentFilter(ALL);
    setTemplateFilter(ALL);
  };

  const hasFilters =
    search.trim() !== "" ||
    workspaceFilter !== ALL ||
    providerFilter !== ALL ||
    healthFilter !== ALL ||
    alignmentFilter !== ALL ||
    templateFilter !== ALL;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <BreadcrumbHeader
        segments={[
          {
            href: paths.runtimes(),
            label: t(($) => $.page.title),
          },
        ]}
        leaf={
          <span className="flex min-w-0 items-center gap-1.5 truncate font-medium">
            <ShieldCheck className="size-3.5 shrink-0 text-primary" />
            {t(($) => $.fc_e2b_stable_overview.title)}
          </span>
        }
        actions={
          <>
            {lastUpdatedAt > 0 && (
              <span className="hidden text-xs text-muted-foreground lg:inline">
                {t(($) => $.fc_e2b_stable_overview.last_updated, {
                  time: formatDateTime(lastUpdatedAt, i18n.language),
                })}
              </span>
            )}
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={refresh}
              disabled={isRefreshing}
            >
              <RefreshCw
                aria-hidden="true"
                className={cn("size-3.5", isRefreshing && "animate-spin")}
              />
              <span className="hidden sm:inline">
                {t(($) => $.fc_e2b_stable_overview.refresh)}
              </span>
            </Button>
            {canPublish && (
              <Button
                type="button"
                size="sm"
                onClick={() => setReleaseDialogOpen(true)}
              >
                <ShieldCheck aria-hidden="true" className="size-3.5" />
                <span className="hidden sm:inline">
                  {t(($) => $.fc_e2b_stable_overview.publish_action)}
                </span>
              </Button>
            )}
          </>
        }
      />

      {channelQuery.isLoading ? (
        <OverviewSkeleton />
      ) : channelQuery.isError ? (
        <CollectionPageState
          icon={TriangleAlert}
          title={t(($) => $.fc_e2b_stable_overview.load_failed_title)}
          description={
            channelQuery.error instanceof Error
              ? channelQuery.error.message
              : t(($) => $.fc_e2b_stable_overview.load_failed_description)
          }
          tone="destructive"
          role="alert"
        />
      ) : !canPublish ? (
        <CollectionPageState
          icon={ShieldCheck}
          title={t(($) => $.fc_e2b_stable_overview.forbidden_title)}
          description={t(
            ($) => $.fc_e2b_stable_overview.forbidden_description,
          )}
          tone="warning"
        />
      ) : runtimesQuery.isLoading ? (
        <OverviewSkeleton />
      ) : runtimesQuery.isError ? (
        <CollectionPageState
          icon={TriangleAlert}
          title={t(($) => $.fc_e2b_stable_overview.load_failed_title)}
          description={
            runtimesQuery.error instanceof Error
              ? runtimesQuery.error.message
              : t(($) => $.fc_e2b_stable_overview.load_failed_description)
          }
          actions={
            <Button type="button" size="sm" onClick={refresh}>
              {t(($) => $.fc_e2b_stable_overview.retry)}
            </Button>
          }
          tone="destructive"
          role="alert"
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <main className="mx-auto w-full max-w-[1680px] space-y-5 p-4 sm:p-6">
            <div>
              <h1 className="text-xl font-semibold tracking-tight">
                {t(($) => $.fc_e2b_stable_overview.heading)}
              </h1>
              <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
                {t(($) => $.fc_e2b_stable_overview.description)}
              </p>
            </div>

            <section className="grid gap-4 xl:grid-cols-[minmax(0,1.25fr)_minmax(360px,0.75fr)]">
              <CurrentStableCard
                current={current}
                currentCount={metrics.current}
                managedCount={metrics.stableManaged}
                coverage={stableCoverage}
              />
              <ActiveReleaseCard
                release={active}
                progress={activeProgress}
              />
            </section>

            <section className="grid grid-cols-2 overflow-hidden rounded-xl border bg-card shadow-sm sm:grid-cols-3 xl:grid-cols-6">
              <OverviewMetric
                icon={Boxes}
                label={t(($) => $.fc_e2b_stable_overview.metrics.total)}
                value={metrics.total}
                hint={t(($) => $.fc_e2b_stable_overview.metrics.workspaces, {
                  count: metrics.workspaceCount,
                })}
                active={alignmentFilter === ALL && healthFilter === ALL}
                onClick={() => {
                  setAlignmentFilter(ALL);
                  setHealthFilter(ALL);
                }}
              />
              <OverviewMetric
                icon={Activity}
                label={t(($) => $.fc_e2b_stable_overview.metrics.online)}
                value={metrics.online}
                tone="success"
                active={healthFilter === "online"}
                onClick={() =>
                  setHealthFilter((value) =>
                    value === "online" ? ALL : "online",
                  )
                }
              />
              <OverviewMetric
                icon={CheckCircle2}
                label={t(($) => $.fc_e2b_stable_overview.metrics.current)}
                value={metrics.current}
                tone="primary"
                active={alignmentFilter === "current"}
                onClick={() =>
                  setAlignmentFilter((value) =>
                    value === "current" ? ALL : "current",
                  )
                }
              />
              <OverviewMetric
                icon={Cloud}
                label={t(($) => $.fc_e2b_stable_overview.metrics.active)}
                value={metrics.active}
                tone="warning"
                active={alignmentFilter === "active"}
                onClick={() =>
                  setAlignmentFilter((value) =>
                    value === "active" ? ALL : "active",
                  )
                }
              />
              <OverviewMetric
                icon={CircleAlert}
                label={t(($) => $.fc_e2b_stable_overview.metrics.outdated)}
                value={metrics.outdated}
                tone="danger"
                active={alignmentFilter === "outdated"}
                onClick={() =>
                  setAlignmentFilter((value) =>
                    value === "outdated" ? ALL : "outdated",
                  )
                }
              />
              <OverviewMetric
                icon={Layers3}
                label={t(($) => $.fc_e2b_stable_overview.metrics.candidate)}
                value={metrics.candidate}
                active={alignmentFilter === "candidate"}
                onClick={() =>
                  setAlignmentFilter((value) =>
                    value === "candidate" ? ALL : "candidate",
                  )
                }
              />
            </section>

            <section className="grid items-start gap-4 xl:grid-cols-[360px_minmax(0,1fr)]">
              <TemplateDistributionCard
                items={templateDistribution}
                selected={templateFilter}
                onSelect={(key) =>
                  setTemplateFilter((currentKey) =>
                    currentKey === key ? ALL : key,
                  )
                }
              />

              <Card className="min-w-0 gap-0 py-0">
                <div className="flex flex-col gap-3 border-b p-4">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div>
                      <h2 className="text-sm font-semibold">
                        {t(($) => $.fc_e2b_stable_overview.runtime_list_title)}
                      </h2>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t(($) => $.fc_e2b_stable_overview.showing, {
                          shown: filteredRuntimes.length,
                          total: runtimes.length,
                        })}
                      </p>
                    </div>
                    {hasFilters && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        onClick={clearFilters}
                      >
                        {t(($) => $.fc_e2b_stable_overview.clear_filters)}
                      </Button>
                    )}
                  </div>
                  <RuntimeFilters
                    search={search}
                    onSearchChange={setSearch}
                    workspaces={workspaces}
                    workspace={workspaceFilter}
                    onWorkspaceChange={setWorkspaceFilter}
                    providers={providers}
                    provider={providerFilter}
                    onProviderChange={setProviderFilter}
                    health={healthFilter}
                    onHealthChange={setHealthFilter}
                    alignment={alignmentFilter}
                    onAlignmentChange={setAlignmentFilter}
                  />
                </div>
                <RuntimeOverviewTable runtimes={filteredRuntimes} />
              </Card>
            </section>
          </main>
        </div>
      )}

      {releaseDialogOpen && (
        <StableFCE2BReleaseDialog
          onClose={() => setReleaseDialogOpen(false)}
        />
      )}
    </div>
  );
}

function CurrentStableCard({
  current,
  currentCount,
  managedCount,
  coverage,
}: {
  current: FCE2BStableTemplateBinding | null;
  currentCount: number;
  managedCount: number;
  coverage: number;
}) {
  const { t } = useT("runtimes");
  return (
    <Card className="relative border-primary/25 bg-primary/[0.025]">
      <div className="absolute inset-y-0 left-0 w-1 bg-primary" />
      <CardHeader className="pl-5">
        <CardTitle className="flex items-center gap-2">
          <ShieldCheck className="size-4 text-primary" />
          {t(($) => $.fc_e2b_stable_overview.current_stable_title)}
        </CardTitle>
        <CardDescription>
          {t(($) => $.fc_e2b_stable_overview.current_stable_description)}
        </CardDescription>
        <CardAction>
          <Badge variant="outline" className="font-mono tabular-nums">
            {currentCount}/{managedCount}
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4 pl-5">
        {current ? (
          <>
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold" title={current.template_alias}>
                {current.template_alias}
              </p>
              <p
                className="mt-1 truncate font-mono text-[11px] text-muted-foreground"
                title={`${current.template_id} · ${current.template_build_id}`}
              >
                {current.template_id} · {current.template_build_id}
              </p>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between text-xs">
                <span className="text-muted-foreground">
                  {t(($) => $.fc_e2b_stable_overview.stable_coverage)}
                </span>
                <span className="font-mono tabular-nums">{coverage}%</span>
              </div>
              <Progress value={coverage} />
            </div>
          </>
        ) : (
          <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
            {t(($) => $.fc_e2b_stable_overview.current_stable_empty)}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ActiveReleaseCard({
  release,
  progress,
}: {
  release: FCE2BStableRelease | null;
  progress: ReturnType<typeof releaseProgress>;
}) {
  const { t, i18n } = useT("runtimes");
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Activity className="size-4 text-muted-foreground" />
          {t(($) => $.fc_e2b_stable_overview.active_release_title)}
        </CardTitle>
        <CardDescription>
          {t(($) => $.fc_e2b_stable_overview.active_release_description)}
        </CardDescription>
        {release && (
          <CardAction>
            <Badge variant="secondary">
              {stableReleaseStatusLabel(release.status, t)}
            </Badge>
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="space-y-4">
        {release ? (
          <>
            <div className="min-w-0">
              <p className="truncate text-sm font-semibold" title={release.template_alias}>
                {release.template_alias}
              </p>
              <p
                className="mt-1 truncate font-mono text-[11px] text-muted-foreground"
                title={`${release.template_id} · ${release.template_build_id}`}
              >
                {release.template_id} · {release.template_build_id}
              </p>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between text-xs">
                <span className="text-muted-foreground">
                  {t(($) => $.fc_e2b_stable.progress, {
                    updated: progress.updated,
                    total: progress.total,
                  })}
                </span>
                <span className="font-mono tabular-nums">
                  {progress.percentage}%
                </span>
              </div>
              <Progress value={progress.percentage} />
            </div>
            {release.next_batch_at && (
              <p className="text-xs text-muted-foreground">
                {t(($) => $.fc_e2b_stable_overview.next_batch, {
                  time: formatDateTime(
                    release.next_batch_at,
                    i18n.language,
                  ),
                })}
              </p>
            )}
          </>
        ) : (
          <div className="flex items-center gap-3 rounded-lg border border-dashed p-4">
            <CheckCircle2 className="size-4 shrink-0 text-emerald-600" />
            <p className="text-sm text-muted-foreground">
              {t(($) => $.fc_e2b_stable_overview.no_active_release)}
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

type MetricTone = "neutral" | "primary" | "success" | "warning" | "danger";

const metricTone: Record<MetricTone, string> = {
  neutral: "text-foreground",
  primary: "text-primary",
  success: "text-emerald-600 dark:text-emerald-400",
  warning: "text-amber-600 dark:text-amber-400",
  danger: "text-destructive",
};

function OverviewMetric({
  icon: Icon,
  label,
  value,
  hint,
  tone = "neutral",
  active,
  onClick,
}: {
  icon: LucideIcon;
  label: string;
  value: number;
  hint?: string;
  tone?: MetricTone;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "group min-w-0 border-b p-4 text-left transition-colors hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring sm:border-b-0 sm:border-r last:border-r-0",
        active && "bg-muted/50",
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-medium text-muted-foreground">
          {label}
        </span>
        <Icon className={cn("size-3.5 shrink-0", metricTone[tone])} />
      </div>
      <p
        className={cn(
          "mt-3 font-mono text-2xl font-semibold tabular-nums tracking-tight",
          metricTone[tone],
        )}
      >
        {value}
      </p>
      {hint && (
        <p className="mt-1 truncate text-[11px] text-muted-foreground">{hint}</p>
      )}
    </button>
  );
}

function TemplateDistributionCard({
  items,
  selected,
  onSelect,
}: {
  items: TemplateDistribution[];
  selected: string;
  onSelect: (key: string) => void;
}) {
  const { t } = useT("runtimes");
  return (
    <Card className="gap-0 py-0 xl:sticky xl:top-4">
      <CardHeader className="border-b py-4">
        <CardTitle>{t(($) => $.fc_e2b_stable_overview.templates_title)}</CardTitle>
        <CardDescription>
          {t(($) => $.fc_e2b_stable_overview.templates_description, {
            count: items.length,
          })}
        </CardDescription>
      </CardHeader>
      <div className="max-h-[520px] overflow-y-auto">
        {items.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">
            {t(($) => $.fc_e2b_stable_overview.empty)}
          </p>
        ) : (
          items.map((item) => (
            <button
              key={item.key}
              type="button"
              aria-pressed={selected === item.key}
              onClick={() => onSelect(item.key)}
              className={cn(
                "w-full border-b p-3 text-left transition-colors last:border-b-0 hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                selected === item.key && "bg-muted/60",
              )}
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <p
                    className="truncate text-xs font-medium"
                    title={item.alias || item.templateId}
                  >
                    {item.alias || item.templateId || "—"}
                  </p>
                  <p
                    className="mt-1 truncate font-mono text-[10px] text-muted-foreground"
                    title={`${item.templateId} · ${item.buildId}`}
                  >
                    {item.buildId || item.templateId || "—"}
                  </p>
                </div>
                <span className="shrink-0 font-mono text-lg font-semibold tabular-nums">
                  {item.runtimeCount}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                {item.matchesCurrentStable && (
                  <Badge
                    variant="outline"
                    className="border-primary/30 bg-primary/5 text-[10px] text-primary"
                  >
                    {t(($) => $.fc_e2b_stable_overview.alignment.current)}
                  </Badge>
                )}
                {item.matchesActiveRelease && (
                  <Badge
                    variant="outline"
                    className="border-amber-500/30 bg-amber-500/5 text-[10px] text-amber-700 dark:text-amber-400"
                  >
                    {t(($) => $.fc_e2b_stable_overview.alignment.active)}
                  </Badge>
                )}
                <span className="text-[10px] text-muted-foreground">
                  {t(($) => $.fc_e2b_stable_overview.template_scope, {
                    online: item.onlineCount,
                    workspaces: item.workspaceCount,
                  })}
                </span>
              </div>
            </button>
          ))
        )}
      </div>
    </Card>
  );
}

function RuntimeFilters({
  search,
  onSearchChange,
  workspaces,
  workspace,
  onWorkspaceChange,
  providers,
  provider,
  onProviderChange,
  health,
  onHealthChange,
  alignment,
  onAlignmentChange,
}: {
  search: string;
  onSearchChange: (value: string) => void;
  workspaces: Array<[string, string]>;
  workspace: string;
  onWorkspaceChange: (value: string) => void;
  providers: string[];
  provider: string;
  onProviderChange: (value: string) => void;
  health: HealthFilter;
  onHealthChange: (value: HealthFilter) => void;
  alignment: AlignmentFilter;
  onAlignmentChange: (value: AlignmentFilter) => void;
}) {
  const { t } = useT("runtimes");
  return (
    <div className="flex flex-wrap gap-2">
      <div className="relative min-w-[220px] flex-1">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={search}
          onChange={(event) => onSearchChange(event.target.value)}
          placeholder={t(($) => $.fc_e2b_stable_overview.search_placeholder)}
          className="h-8 pl-8 text-xs"
        />
      </div>
      <Select
        value={workspace}
        onValueChange={(value) => onWorkspaceChange(value ?? ALL)}
      >
        <SelectTrigger size="sm" className="max-w-48">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value={ALL}>
            {t(($) => $.fc_e2b_stable_overview.filters.all_workspaces)}
          </SelectItem>
          {workspaces.map(([id, name]) => (
            <SelectItem key={id} value={id}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={provider}
        onValueChange={(value) => onProviderChange(value ?? ALL)}
      >
        <SelectTrigger size="sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value={ALL}>
            {t(($) => $.fc_e2b_stable_overview.filters.all_providers)}
          </SelectItem>
          {providers.map((item) => (
            <SelectItem key={item} value={item}>
              {item}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select
        value={health}
        onValueChange={(value) =>
          onHealthChange((value ?? ALL) as HealthFilter)
        }
      >
        <SelectTrigger size="sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value={ALL}>
            {t(($) => $.fc_e2b_stable_overview.filters.all_health)}
          </SelectItem>
          <SelectItem value="online">
            {t(($) => $.fc_e2b_stable.runtime_online)}
          </SelectItem>
          <SelectItem value="offline">
            {t(($) => $.fc_e2b_stable.runtime_offline)}
          </SelectItem>
        </SelectContent>
      </Select>
      <Select
        value={alignment}
        onValueChange={(value) =>
          onAlignmentChange((value ?? ALL) as AlignmentFilter)
        }
      >
        <SelectTrigger size="sm">
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value={ALL}>
            {t(($) => $.fc_e2b_stable_overview.filters.all_alignment)}
          </SelectItem>
          <SelectItem value="current">
            {t(($) => $.fc_e2b_stable_overview.alignment.current)}
          </SelectItem>
          <SelectItem value="active">
            {t(($) => $.fc_e2b_stable_overview.alignment.active)}
          </SelectItem>
          <SelectItem value="outdated">
            {t(($) => $.fc_e2b_stable_overview.alignment.outdated)}
          </SelectItem>
          <SelectItem value="candidate">
            {t(($) => $.fc_e2b_stable_overview.alignment.candidate)}
          </SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

function RuntimeOverviewTable({
  runtimes,
}: {
  runtimes: FCE2BStableRuntimeOverview[];
}) {
  const { t, i18n } = useT("runtimes");
  if (runtimes.length === 0) {
    return (
      <div className="flex min-h-56 flex-col items-center justify-center gap-2 px-6 text-center">
        <Search className="size-5 text-muted-foreground" />
        <p className="text-sm font-medium">
          {t(($) => $.fc_e2b_stable_overview.no_matches)}
        </p>
        <p className="text-xs text-muted-foreground">
          {t(($) => $.fc_e2b_stable_overview.no_matches_hint)}
        </p>
      </div>
    );
  }
  return (
    <div className="max-h-[680px] overflow-auto">
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-card shadow-[0_1px_0_0_var(--border)]">
          <TableRow className="hover:bg-card">
            <TableHead className="min-w-64 pl-4">
              {t(($) => $.fc_e2b_stable_overview.table.runtime)}
            </TableHead>
            <TableHead>
              {t(($) => $.fc_e2b_stable_overview.table.provider)}
            </TableHead>
            <TableHead>
              {t(($) => $.fc_e2b_stable_overview.table.health)}
            </TableHead>
            <TableHead>
              {t(($) => $.fc_e2b_stable_overview.table.channel)}
            </TableHead>
            <TableHead className="min-w-72">
              {t(($) => $.fc_e2b_stable_overview.table.template)}
            </TableHead>
            <TableHead>
              {t(($) => $.fc_e2b_stable_overview.table.alignment)}
            </TableHead>
            <TableHead className="pr-4 text-right">
              {t(($) => $.fc_e2b_stable_overview.table.updated)}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {runtimes.map((runtime) => {
            const alignment = runtimeAlignment(runtime);
            const online = runtime.status.toLowerCase() === "online";
            return (
              <TableRow key={runtime.runtime_id}>
                <TableCell className="pl-4">
                  <div className="max-w-72 min-w-0">
                    <p
                      className="truncate text-xs font-medium"
                      title={runtime.runtime_name}
                    >
                      {runtime.runtime_name}
                    </p>
                    <p
                      className="mt-1 truncate text-[11px] text-muted-foreground"
                      title={runtime.workspace_name}
                    >
                      {runtime.workspace_name}
                    </p>
                  </div>
                </TableCell>
                <TableCell>
                  <span className="inline-flex items-center gap-1.5 text-xs">
                    <ProviderLogo
                      provider={runtime.provider}
                      className="size-3.5"
                    />
                    {runtime.provider}
                  </span>
                </TableCell>
                <TableCell>
                  <span
                    className={cn(
                      "inline-flex items-center gap-1.5 text-xs",
                      online
                        ? "text-emerald-600 dark:text-emerald-400"
                        : "text-muted-foreground",
                    )}
                  >
                    {online ? (
                      <span className="size-1.5 rounded-full bg-current" />
                    ) : (
                      <WifiOff className="size-3" />
                    )}
                    {online
                      ? t(($) => $.fc_e2b_stable.runtime_online)
                      : t(($) => $.fc_e2b_stable.runtime_offline)}
                  </span>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className="text-[10px]">
                    {runtime.template_channel === "candidate"
                      ? t(($) => $.fc_e2b_stable_overview.channel.candidate)
                      : t(($) => $.fc_e2b_stable_overview.channel.stable)}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="max-w-80 min-w-0">
                    <p
                      className="truncate text-xs font-medium"
                      title={runtime.template_alias}
                    >
                      {runtime.template_alias || "—"}
                    </p>
                    <p
                      className="mt-1 truncate font-mono text-[10px] text-muted-foreground"
                      title={`${runtime.template_id} · ${runtime.template_build_id}`}
                    >
                      {runtime.template_build_id ||
                        runtime.template_id ||
                        "—"}
                    </p>
                  </div>
                </TableCell>
                <TableCell>
                  <AlignmentBadge
                    alignment={alignment}
                    targetStatus={runtime.active_release_target_status}
                  />
                </TableCell>
                <TableCell className="pr-4 text-right text-[11px] text-muted-foreground">
                  {formatDateTime(runtime.updated_at, i18n.language)}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

function AlignmentBadge({
  alignment,
  targetStatus,
}: {
  alignment: Alignment;
  targetStatus: string;
}) {
  const { t } = useT("runtimes");
  const className = {
    current: "border-primary/30 bg-primary/5 text-primary",
    active:
      "border-amber-500/30 bg-amber-500/5 text-amber-700 dark:text-amber-400",
    outdated: "border-destructive/30 bg-destructive/5 text-destructive",
    candidate: "border-border bg-muted/50 text-muted-foreground",
  }[alignment];
  return (
    <div className="flex flex-col items-start gap-1">
      <Badge variant="outline" className={cn("text-[10px]", className)}>
        {alignmentLabel(alignment, t)}
      </Badge>
      {targetStatus && (
        <span className="text-[10px] text-muted-foreground">
          {targetStatusLabel(targetStatus, t)}
        </span>
      )}
    </div>
  );
}

function alignmentLabel(
  alignment: Alignment,
  t: ReturnType<typeof useT<"runtimes">>["t"],
): string {
  switch (alignment) {
    case "current":
      return t(($) => $.fc_e2b_stable_overview.alignment.current);
    case "active":
      return t(($) => $.fc_e2b_stable_overview.alignment.active);
    case "outdated":
      return t(($) => $.fc_e2b_stable_overview.alignment.outdated);
    case "candidate":
      return t(($) => $.fc_e2b_stable_overview.alignment.candidate);
  }
}

function stableReleaseStatusLabel(
  status: string,
  t: ReturnType<typeof useT<"runtimes">>["t"],
): string {
  const labels: Record<string, string> = {
    validating: t(($) => $.fc_e2b_stable.status.validating),
    developer_rollout: t(($) => $.fc_e2b_stable.status.developer_rollout),
    awaiting_rollout: t(($) => $.fc_e2b_stable.status.awaiting_rollout),
    rolling_out: t(($) => $.fc_e2b_stable.status.rolling_out),
    observing: t(($) => $.fc_e2b_stable.status.observing),
    paused: t(($) => $.fc_e2b_stable.status.paused),
    rolling_back: t(($) => $.fc_e2b_stable.status.rolling_back),
  };
  return labels[status] ?? status;
}

function targetStatusLabel(
  status: string,
  t: ReturnType<typeof useT<"runtimes">>["t"],
): string {
  const labels: Record<string, string> = {
    pending: t(($) => $.fc_e2b_stable_overview.target_status.pending),
    updating: t(($) => $.fc_e2b_stable_overview.target_status.updating),
    updated: t(($) => $.fc_e2b_stable_overview.target_status.updated),
    failed: t(($) => $.fc_e2b_stable_overview.target_status.failed),
    rolling_back: t(
      ($) => $.fc_e2b_stable_overview.target_status.rolling_back,
    ),
    rolled_back: t(
      ($) => $.fc_e2b_stable_overview.target_status.rolled_back,
    ),
  };
  return labels[status] ?? status;
}

function OverviewSkeleton() {
  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[1680px] space-y-5 p-4 sm:p-6">
        <div>
          <Skeleton className="h-6 w-52" />
          <Skeleton className="mt-2 h-4 w-[min(560px,80%)]" />
        </div>
        <div className="grid gap-4 xl:grid-cols-2">
          <Skeleton className="h-52 rounded-xl" />
          <Skeleton className="h-52 rounded-xl" />
        </div>
        <Skeleton className="h-28 rounded-xl" />
        <div className="grid gap-4 xl:grid-cols-[360px_minmax(0,1fr)]">
          <Skeleton className="h-[460px] rounded-xl" />
          <Skeleton className="h-[620px] rounded-xl" />
        </div>
      </div>
    </div>
  );
}
