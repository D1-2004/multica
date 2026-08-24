"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowLeft,
  BarChart3,
  CalendarDays,
  ChevronDown,
  ListChecks,
  Tag,
} from "lucide-react";
import {
  Bar,
  CartesianGrid,
  ComposedChart,
  Line,
  XAxis,
  YAxis,
} from "recharts";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { labelUsageOptions } from "@multica/core/labels";
import type {
  LabelUsageBreakdown,
  LabelUsageDirection,
  LabelUsagePeriod,
  LabelUsageSort,
  LabelUsageTask,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { AppLink, useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { KpiCard } from "../runtimes/components/shared";
import { formatTokens, formatUsd } from "../runtimes/utils";
import { useT } from "../i18n";
import { useViewingTimezone } from "../common/use-viewing-timezone";

const COST_USD_TICKS_PER_USD = 10_000_000_000;
const PAGE_SIZE = 25;
const DEFAULT_PERIOD: LabelUsagePeriod = "all";
const DEFAULT_SORT: LabelUsageSort = "cost";
const DEFAULT_DIRECTION: LabelUsageDirection = "desc";

const PERIODS: LabelUsagePeriod[] = ["7d", "30d", "90d", "all"];
const SORTS: LabelUsageSort[] = ["cost", "tokens", "recent"];
const DIRECTIONS: LabelUsageDirection[] = ["asc", "desc"];

export function LabelUsagePage({ labelId }: { labelId: string }) {
  const { t, i18n } = useT("settings");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const viewTZ = useViewingTimezone();
  const locale = i18n.resolvedLanguage ?? i18n.language;

  const period = readEnum(
    navigation.searchParams.get("period"),
    PERIODS,
    DEFAULT_PERIOD,
  );
  const sort = readEnum(
    navigation.searchParams.get("sort"),
    SORTS,
    DEFAULT_SORT,
  );
  const direction = readEnum(
    navigation.searchParams.get("direction"),
    DIRECTIONS,
    DEFAULT_DIRECTION,
  );
  const page = readPage(navigation.searchParams.get("page"));

  const query = useQuery(
    labelUsageOptions(wsId, labelId, {
      period,
      sort,
      direction,
      tz: viewTZ,
      page,
      page_size: PAGE_SIZE,
    }),
  );

  const replaceParams = (updates: Record<string, string | null>) => {
    const params = new URLSearchParams(navigation.searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (value == null) params.delete(key);
      else params.set(key, value);
    }
    const search = params.toString();
    navigation.replace(search ? `${navigation.pathname}?${search}` : navigation.pathname);
  };

  const changePeriod = (next: LabelUsagePeriod) => {
    replaceParams({
      period: next === DEFAULT_PERIOD ? null : next,
      page: null,
    });
  };

  const changeSort = (value: string) => {
    const [nextSort, nextDirection] = value.split(":");
    if (
      !SORTS.includes(nextSort as LabelUsageSort) ||
      !DIRECTIONS.includes(nextDirection as LabelUsageDirection)
    ) {
      return;
    }
    const safeSort = nextSort as LabelUsageSort;
    const safeDirection = nextDirection as LabelUsageDirection;
    replaceParams({
      sort: safeSort === DEFAULT_SORT ? null : safeSort,
      direction: safeDirection === DEFAULT_DIRECTION ? null : safeDirection,
      page: null,
    });
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader className="justify-between gap-3 px-5">
        <div className="flex min-w-0 items-center gap-2">
          <Button
            variant="ghost"
            size="icon-sm"
            nativeButton={false}
            render={<AppLink href={paths.settingsLabels()} />}
            aria-label={t(($) => $.labels.usage_detail.back)}
          >
            <ArrowLeft className="size-4" />
          </Button>
          <Tag className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-body font-medium">
            {query.data?.label.name || t(($) => $.labels.usage_detail.title)}
          </h1>
          {query.data?.label.color ? (
            <span
              className="size-2 shrink-0 rounded-full"
              style={{ backgroundColor: query.data.label.color }}
            />
          ) : null}
        </div>
        <PeriodFilter value={period} onChange={changePeriod} />
      </PageHeader>

      <main className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-6xl space-y-5 p-4 sm:p-6">
          {query.isLoading ? (
            <LabelUsageSkeleton />
          ) : query.isError ? (
            <QueryError onRetry={() => void query.refetch()} />
          ) : query.data ? (
            <>
              <SummaryCards
                totalTokens={query.data.summary.total_tokens}
                totalCostTicks={query.data.summary.total_cost_usd_ticks}
                taskCount={query.data.summary.task_count}
                uncostedTokens={query.data.summary.uncosted_tokens}
                unpricedCount={query.data.summary.unpriced_task_count}
              />

              {query.data.summary.unpriced_task_count > 0 ? (
                <div className="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/5 px-3 py-2.5 text-caption text-muted-foreground">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
                  <span>
                    {t(($) => $.labels.usage_detail.unpriced_notice, {
                      count: query.data.summary.unpriced_task_count,
                      tokens: formatTokens(query.data.summary.uncosted_tokens),
                    })}
                  </span>
                </div>
              ) : null}

              {query.data.summary.task_count === 0 ? (
                <EmptyUsage />
              ) : (
                <>
                  <TrendCard daily={query.data.daily} locale={locale} />
                  <BreakdownSection rows={query.data.breakdown} />
                  <TaskSection
                    rows={query.data.tasks}
                    sort={sort}
                    direction={direction}
                    onSortChange={changeSort}
                    page={query.data.pagination.page}
                    totalPages={query.data.pagination.total_pages}
                    total={query.data.pagination.total}
                    onPageChange={(next) =>
                      replaceParams({ page: next === 1 ? null : String(next) })
                    }
                    issueHref={paths.issueDetail}
                    locale={locale}
                  />
                </>
              )}
            </>
          ) : null}
        </div>
      </main>
    </div>
  );
}

function PeriodFilter({
  value,
  onChange,
}: {
  value: LabelUsagePeriod;
  onChange: (value: LabelUsagePeriod) => void;
}) {
  const { t } = useT("settings");
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button variant="outline" size="sm" className="gap-1 px-2.5">
            <CalendarDays className="size-3.5 text-muted-foreground" />
            {t(($) => $.labels.usage_detail.periods[value])}
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        }
      />
      <DropdownMenuContent align="end" className="min-w-32">
        <DropdownMenuRadioGroup
          value={value}
          onValueChange={(next) => onChange(next as LabelUsagePeriod)}
        >
          {PERIODS.map((period) => (
            <DropdownMenuRadioItem key={period} value={period}>
              {t(($) => $.labels.usage_detail.periods[period])}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function SummaryCards({
  totalTokens,
  totalCostTicks,
  taskCount,
  uncostedTokens,
  unpricedCount,
}: {
  totalTokens: number;
  totalCostTicks: number;
  taskCount: number;
  uncostedTokens: number;
  unpricedCount: number;
}) {
  const { t } = useT("settings");
  const cost = usdFromTicks(totalCostTicks);
  const partial = unpricedCount > 0 || uncostedTokens > 0;
  const costValue = partial && totalCostTicks === 0 ? "—" : formatUsd(cost);
  return (
    <div className="grid grid-cols-1 divide-y rounded-lg border bg-card sm:grid-cols-2 sm:divide-x sm:divide-y-0 lg:grid-cols-4">
      <KpiCard
        label={t(($) => $.labels.usage_detail.kpi.cost)}
        value={costValue}
        hint={
          partial
            ? t(($) => $.labels.usage_detail.kpi.cost_partial)
            : t(($) => $.labels.usage_detail.kpi.cost_complete)
        }
      />
      <KpiCard
        label={t(($) => $.labels.usage_detail.kpi.tokens)}
        value={formatTokens(totalTokens)}
      />
      <KpiCard
        label={t(($) => $.labels.usage_detail.kpi.tasks)}
        value={taskCount.toLocaleString()}
      />
      <KpiCard
        label={t(($) => $.labels.usage_detail.kpi.unpriced)}
        value={unpricedCount.toLocaleString()}
        hint={t(($) => $.labels.usage_detail.kpi.unpriced_hint, {
          tokens: formatTokens(uncostedTokens),
        })}
        accent={unpricedCount > 0 ? "brand" : "default"}
      />
    </div>
  );
}

function TrendCard({
  daily,
  locale,
}: {
  daily: Array<{
    date: string;
    total_tokens: number;
    total_cost_usd_ticks: number;
  }>;
  locale: string;
}) {
  const { t } = useT("settings");
  const chartData = useMemo(
    () =>
      daily.map((row) => ({
        date: row.date,
        label: formatShortDate(row.date, locale),
        tokens: row.total_tokens,
        cost: usdFromTicks(row.total_cost_usd_ticks),
      })),
    [daily, locale],
  );
  const config = {
    tokens: {
      label: t(($) => $.labels.usage_detail.chart.tokens),
      color: "var(--chart-1)",
    },
    cost: {
      label: t(($) => $.labels.usage_detail.chart.cost),
      color: "var(--chart-3)",
    },
  } satisfies ChartConfig;

  return (
    <section className="rounded-lg border bg-card p-4">
      <div className="mb-4 flex items-center justify-between gap-3">
        <div>
          <h2 className="text-body font-semibold">
            {t(($) => $.labels.usage_detail.chart.title)}
          </h2>
          <p className="mt-0.5 text-caption text-muted-foreground">
            {t(($) => $.labels.usage_detail.chart.description)}
          </p>
        </div>
        <BarChart3 className="size-4 shrink-0 text-muted-foreground" />
      </div>
      {chartData.length === 0 ? (
        <p className="py-12 text-center text-caption text-muted-foreground">
          {t(($) => $.labels.usage_detail.chart.empty)}
        </p>
      ) : (
        <ChartContainer config={config} className="aspect-[3/1] min-h-56 w-full">
          <ComposedChart data={chartData} margin={{ left: 0, right: 0, top: 8, bottom: 0 }}>
            <CartesianGrid vertical={false} />
            <XAxis
              dataKey="label"
              tickLine={false}
              axisLine={false}
              tickMargin={8}
              interval="preserveStartEnd"
            />
            <YAxis
              yAxisId="tokens"
              tickLine={false}
              axisLine={false}
              width="auto"
              tickFormatter={(value: number) => formatTokens(value)}
            />
            <YAxis
              yAxisId="cost"
              orientation="right"
              tickLine={false}
              axisLine={false}
              width="auto"
              tickFormatter={(value: number) => formatUsd(value)}
            />
            <ChartTooltip
              content={
                <ChartTooltipContent
                  formatter={(value, name) =>
                    typeof value === "number"
                      ? name === "cost"
                        ? formatUsd(value)
                        : formatTokens(value)
                      : String(value)
                  }
                />
              }
            />
            <Bar
              yAxisId="tokens"
              dataKey="tokens"
              fill="var(--color-tokens)"
              radius={[3, 3, 0, 0]}
            />
            <Line
              yAxisId="cost"
              dataKey="cost"
              stroke="var(--color-cost)"
              strokeWidth={2}
              dot={false}
            />
          </ComposedChart>
        </ChartContainer>
      )}
    </section>
  );
}

function BreakdownSection({ rows }: { rows: LabelUsageBreakdown[] }) {
  const { t } = useT("settings");
  const providerRows = useMemo(() => aggregateProviders(rows), [rows]);
  const modelRows = useMemo(
    () =>
      rows
        .map((row) => ({
          key: `${row.provider}/${row.model}`,
          label: row.model || t(($) => $.labels.usage_detail.breakdown.unknown_model),
          caption: row.provider || t(($) => $.labels.usage_detail.breakdown.unknown_provider),
          tokens: row.total_tokens,
          costTicks: row.total_cost_usd_ticks,
          unpriced: row.unpriced_task_count,
          uncostedTokens: row.uncosted_tokens,
        }))
        .toSorted(compareDistributionRows),
    [rows, t],
  );

  return (
    <section>
      <div className="mb-3">
        <h2 className="text-body font-semibold">
          {t(($) => $.labels.usage_detail.breakdown.title)}
        </h2>
        <p className="mt-0.5 text-caption text-muted-foreground">
          {t(($) => $.labels.usage_detail.breakdown.description)}
        </p>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        <DistributionCard
          title={t(($) => $.labels.usage_detail.breakdown.providers)}
          rows={providerRows}
        />
        <DistributionCard
          title={t(($) => $.labels.usage_detail.breakdown.models)}
          rows={modelRows}
        />
      </div>
    </section>
  );
}

interface DistributionRow {
  key: string;
  label: string;
  caption: string;
  tokens: number;
  costTicks: number;
  unpriced: number;
  uncostedTokens: number;
}

function DistributionCard({
  title,
  rows,
}: {
  title: string;
  rows: DistributionRow[];
}) {
  const { t } = useT("settings");
  const max = rows.reduce((current, row) => Math.max(current, row.tokens), 0);
  return (
    <div className="rounded-lg border bg-card p-4">
      <h3 className="text-caption font-medium">{title}</h3>
      {rows.length === 0 ? (
        <p className="py-10 text-center text-caption text-muted-foreground">
          {t(($) => $.labels.usage_detail.breakdown.empty)}
        </p>
      ) : (
        <div className="mt-4 space-y-3">
          {rows.slice(0, 8).map((row) => {
            const width =
              max > 0 ? Math.max(2, (row.tokens / max) * 100) : 0;
            return (
              <div key={row.key} className="space-y-1.5">
                <div className="flex min-w-0 items-center justify-between gap-3 text-caption">
                  <div className="min-w-0">
                    <div className="truncate font-medium">{row.label}</div>
                    {row.caption ? (
                      <div className="truncate text-micro text-muted-foreground">
                        {row.caption}
                      </div>
                    ) : null}
                  </div>
                  <div className="shrink-0 text-right tabular-nums">
                    <div>{formatTokens(row.tokens)}</div>
                    <div className="text-micro text-muted-foreground">
                      {formatDistributionCost(row)}
                    </div>
                  </div>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full bg-brand"
                    style={{ width: `${width}%` }}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function TaskSection({
  rows,
  sort,
  direction,
  onSortChange,
  page,
  totalPages,
  total,
  onPageChange,
  issueHref,
  locale,
}: {
  rows: LabelUsageTask[];
  sort: LabelUsageSort;
  direction: LabelUsageDirection;
  onSortChange: (value: string) => void;
  page: number;
  totalPages: number;
  total: number;
  onPageChange: (page: number) => void;
  issueHref: (id: string) => string;
  locale: string;
}) {
  const { t } = useT("settings");
  const sortValue = `${sort}:${direction}`;
  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-col gap-3 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="text-body font-semibold">
            {t(($) => $.labels.usage_detail.tasks.title)}
          </h2>
          <p className="mt-0.5 text-caption text-muted-foreground">
            {t(($) => $.labels.usage_detail.tasks.count, { count: total })}
          </p>
        </div>
        <TaskSort value={sortValue} onChange={onSortChange} />
      </div>
      {rows.length === 0 ? (
        <p className="py-12 text-center text-caption text-muted-foreground">
          {t(($) => $.labels.usage_detail.tasks.empty)}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t(($) => $.labels.usage_detail.tasks.issue)}</TableHead>
              <TableHead>{t(($) => $.labels.usage_detail.tasks.status)}</TableHead>
              <TableHead>{t(($) => $.labels.usage_detail.tasks.model)}</TableHead>
              <TableHead>{t(($) => $.labels.usage_detail.tasks.completed)}</TableHead>
              <TableHead className="text-right">
                {t(($) => $.labels.usage_detail.tasks.tokens)}
              </TableHead>
              <TableHead className="text-right">
                {t(($) => $.labels.usage_detail.tasks.cost)}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((task) => (
              <TaskRow
                key={task.task_id}
                task={task}
                issueHref={issueHref}
                locale={locale}
              />
            ))}
          </TableBody>
        </Table>
      )}
      {totalPages > 1 ? (
        <div className="flex items-center justify-between border-t px-4 py-3">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.labels.usage_detail.tasks.page, {
              page,
              total: totalPages,
            })}
          </span>
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              disabled={page <= 1}
              onClick={() => onPageChange(page - 1)}
            >
              {t(($) => $.labels.usage_detail.tasks.previous)}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={page >= totalPages}
              onClick={() => onPageChange(page + 1)}
            >
              {t(($) => $.labels.usage_detail.tasks.next)}
            </Button>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function TaskSort({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useT("settings");
  const labels: Record<string, string> = {
    "cost:desc": t(($) => $.labels.usage_detail.sort.cost_high),
    "cost:asc": t(($) => $.labels.usage_detail.sort.cost_low),
    "tokens:desc": t(($) => $.labels.usage_detail.sort.tokens_high),
    "tokens:asc": t(($) => $.labels.usage_detail.sort.tokens_low),
    "recent:desc": t(($) => $.labels.usage_detail.sort.recent),
    "recent:asc": t(($) => $.labels.usage_detail.sort.oldest),
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button variant="outline" size="sm" className="gap-1 px-2.5">
            {labels[value] ?? labels["cost:desc"]}
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        }
      />
      <DropdownMenuContent align="end" className="min-w-44">
        <DropdownMenuRadioGroup value={value} onValueChange={onChange}>
          {Object.entries(labels).map(([option, label]) => (
            <DropdownMenuRadioItem key={option} value={option}>
              {label}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function TaskRow({
  task,
  issueHref,
  locale,
}: {
  task: LabelUsageTask;
  issueHref: (id: string) => string;
  locale: string;
}) {
  const { t } = useT("settings");
  const displayCost = !task.has_usage
    ? "—"
    : task.is_priced
      ? formatUsd(usdFromTicks(task.total_cost_usd_ticks))
      : task.total_cost_usd_ticks > 0
        ? `${formatUsd(usdFromTicks(task.total_cost_usd_ticks))}*`
        : "—";
  return (
    <TableRow>
      <TableCell className="max-w-72">
        {task.issue_id ? (
          <AppLink
            href={issueHref(task.issue_identifier || task.issue_id)}
            className="block min-w-0 hover:underline"
          >
            <div className="truncate font-medium">
              {task.issue_identifier || t(($) => $.labels.usage_detail.tasks.unknown_issue)}
            </div>
            <div className="truncate text-caption text-muted-foreground">
              {task.issue_title || "—"}
            </div>
          </AppLink>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell>
        <Badge variant="outline" className="font-mono text-micro">
          {task.status || "—"}
        </Badge>
      </TableCell>
      <TableCell>
        <div className="max-w-48 truncate text-caption font-medium">
          {formatTaskModels(
            task,
            t(($) => $.labels.usage_detail.breakdown.unknown_model),
          )}
        </div>
        <div className="max-w-48 truncate text-micro text-muted-foreground">
          {formatTaskProviders(
            task,
            t(($) => $.labels.usage_detail.breakdown.unknown_provider),
          )}
        </div>
      </TableCell>
      <TableCell className="text-caption text-muted-foreground">
        {formatDateTime(task.activity_at || task.completed_at || task.created_at, locale)}
      </TableCell>
      <TableCell className="text-right font-mono text-caption tabular-nums">
        {formatTokens(task.total_tokens)}
      </TableCell>
      <TableCell className="text-right font-mono text-caption tabular-nums">
        <span className={task.is_priced ? undefined : "text-muted-foreground"}>
          {displayCost}
        </span>
        {task.has_usage && !task.is_priced ? (
          <span
            className="ml-1 text-warning"
            title={t(($) => $.labels.usage_detail.tasks.unpriced_tokens, {
              tokens: formatTokens(task.uncosted_tokens),
            })}
          >
            <AlertTriangle className="inline size-3" />
          </span>
        ) : !task.has_usage ? (
          <span className="ml-1 text-micro text-muted-foreground">
            {t(($) => $.labels.usage_detail.tasks.no_usage)}
          </span>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

function QueryError({ onRetry }: { onRetry: () => void }) {
  const { t } = useT("settings");
  return (
    <div className="flex flex-col items-center rounded-lg border border-dashed py-14 text-center">
      <AlertTriangle className="size-6 text-destructive" />
      <p className="mt-3 text-body font-medium">
        {t(($) => $.labels.usage_detail.error.title)}
      </p>
      <p className="mt-1 text-caption text-muted-foreground">
        {t(($) => $.labels.usage_detail.error.description)}
      </p>
      <Button variant="outline" size="sm" className="mt-4" onClick={onRetry}>
        {t(($) => $.labels.usage_detail.error.retry)}
      </Button>
    </div>
  );
}

function EmptyUsage() {
  const { t } = useT("settings");
  return (
    <div className="flex flex-col items-center rounded-lg border border-dashed py-14 text-center">
      <ListChecks className="size-6 text-faint-foreground" />
      <p className="mt-3 text-body font-medium">
        {t(($) => $.labels.usage_detail.empty.title)}
      </p>
      <p className="mt-1 max-w-md text-caption text-muted-foreground">
        {t(($) => $.labels.usage_detail.empty.description)}
      </p>
    </div>
  );
}

function LabelUsageSkeleton() {
  return (
    <div className="space-y-5">
      <Skeleton className="h-28 rounded-lg" />
      <Skeleton className="h-64 rounded-lg" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-56 rounded-lg" />
        <Skeleton className="h-56 rounded-lg" />
      </div>
      <Skeleton className="h-72 rounded-lg" />
    </div>
  );
}

function aggregateProviders(rows: LabelUsageBreakdown[]): DistributionRow[] {
  const grouped = new Map<string, DistributionRow>();
  for (const row of rows) {
    const key = row.provider || "unknown";
    const current = grouped.get(key) ?? {
      key,
      label: row.provider || "Unknown",
      caption: "",
      tokens: 0,
      costTicks: 0,
      unpriced: 0,
      uncostedTokens: 0,
    };
    current.tokens += row.total_tokens;
    current.costTicks += row.total_cost_usd_ticks;
    current.unpriced += row.unpriced_task_count;
    current.uncostedTokens += row.uncosted_tokens;
    grouped.set(key, current);
  }
  return Array.from(grouped.values()).toSorted(compareDistributionRows);
}

function compareDistributionRows(a: DistributionRow, b: DistributionRow): number {
  return (
    b.costTicks - a.costTicks ||
    b.tokens - a.tokens ||
    a.label.localeCompare(b.label)
  );
}

function formatDistributionCost(row: DistributionRow): string {
  if ((row.unpriced > 0 || row.uncostedTokens > 0) && row.costTicks === 0) {
    return "—";
  }
  const formatted = formatUsd(usdFromTicks(row.costTicks));
  return row.unpriced > 0 || row.uncostedTokens > 0
    ? `${formatted}*`
    : formatted;
}

function formatTaskModels(task: LabelUsageTask, unknown: string): string {
  const models = Array.from(
    new Set(task.usage_breakdown.map((row) => row.model).filter(Boolean)),
  );
  if (models.length > 0) return models.join(", ");
  return task.model || unknown;
}

function formatTaskProviders(task: LabelUsageTask, unknown: string): string {
  const providers = Array.from(
    new Set(task.usage_breakdown.map((row) => row.provider).filter(Boolean)),
  );
  if (providers.length > 0) return providers.join(", ");
  return task.provider || unknown;
}

function usdFromTicks(ticks: number): number {
  return ticks / COST_USD_TICKS_PER_USD;
}

function formatShortDate(value: string, locale: string): string {
  const date = new Date(`${value}T00:00:00Z`);
  if (Number.isNaN(date.getTime())) return value || "—";
  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(date);
}

function formatDateTime(value: string, locale: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function readEnum<T extends string>(
  value: string | null,
  values: readonly T[],
  defaultValue: T,
): T {
  return value && values.includes(value as T) ? (value as T) : defaultValue;
}

function readPage(value: string | null): number {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : 1;
}
