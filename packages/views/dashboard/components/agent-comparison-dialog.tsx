"use client";

import { useMemo, useState } from "react";
import {
  Check,
  ChartNoAxesCombined,
  ChevronDown,
  Search,
  X,
} from "lucide-react";
import {
  CartesianGrid,
  Line,
  LineChart,
  XAxis,
  YAxis,
} from "recharts";
import { Button } from "@multica/ui/components/ui/button";
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@multica/ui/components/ui/chart";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import type {
  DashboardRunTimeDaily,
  DashboardUsageDaily,
} from "@multica/core/types";
import { formatTokens } from "../../runtimes/utils";
import { useT } from "../../i18n";
import {
  buildAgentComparisonSeries,
  DELETED_AGENTS_ROW_ID,
  formatDuration,
  type AgentComparisonDimension,
  type AgentComparisonMetric,
} from "../utils";

interface ComparisonAgent {
  id: string;
  name: string;
}

interface AgentComparisonDialogProps {
  agents: ComparisonAgent[];
  usage: DashboardUsageDaily[];
  runTime: DashboardRunTimeDaily[];
  defaultAgentId: string | undefined;
  defaultMetric: AgentComparisonMetric;
  dimension: AgentComparisonDimension;
  days: number;
  tz: string;
  lessThanMinuteLabel: string;
}

const LINE_COLORS = [
  "var(--chart-1)",
  "var(--chart-2)",
  "var(--chart-3)",
  "var(--chart-4)",
  "var(--chart-5)",
] as const;
const LINE_DASHES = [undefined, "6 3", "2 3"] as const;

export function AgentComparisonDialog({
  agents,
  usage,
  runTime,
  defaultAgentId,
  defaultMetric,
  dimension,
  days,
  tz,
  lessThanMinuteLabel,
}: AgentComparisonDialogProps) {
  const { t, i18n } = useT("usage");
  const locales = i18n.resolvedLanguage ?? i18n.language;
  const [open, setOpen] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [metric, setMetric] = useState<AgentComparisonMetric>(defaultMetric);
  const [selectedAgentIds, setSelectedAgentIds] = useState<string[]>(
    defaultAgentId ? [defaultAgentId] : [],
  );

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (nextOpen) {
      setMetric(defaultMetric);
      setSelectedAgentIds(defaultAgentId ? [defaultAgentId] : []);
    } else {
      setPickerOpen(false);
      setSearch("");
    }
  };

  const agentById = useMemo(
    () => new Map(agents.map((agent) => [agent.id, agent])),
    [agents],
  );
  const selectedAgents = selectedAgentIds.flatMap((id) => {
    const agent = agentById.get(id);
    return agent ? [agent] : [];
  });
  const knownAgentIds = useMemo(
    () =>
      new Set(
        agents
          .map((agent) => agent.id)
          .filter((id) => id !== DELETED_AGENTS_ROW_ID),
      ),
    [agents],
  );
  const points = useMemo(
    () =>
      buildAgentComparisonSeries({
        usage,
        runTime,
        metric,
        dimension,
        days,
        tz,
        selectedAgentIds,
        knownAgentIds,
      }),
    [
      usage,
      runTime,
      metric,
      dimension,
      days,
      tz,
      selectedAgentIds,
      knownAgentIds,
    ],
  );
  const series = selectedAgents.map((agent, index) => ({
    ...agent,
    key: `agent${index}`,
    color: LINE_COLORS[index % LINE_COLORS.length],
    strokeDasharray:
      LINE_DASHES[Math.floor(index / LINE_COLORS.length) % LINE_DASHES.length],
  }));
  const chartData = points.map((point) => {
    const row: Record<string, string | number> = {
      bucket: point.bucket,
      label: point.label,
    };
    for (const item of series) {
      row[item.key] = point.values[item.id] ?? 0;
    }
    return row;
  });
  const chartConfig = Object.fromEntries(
    series.map((item) => [
      item.key,
      { label: item.name, color: item.color },
    ]),
  ) satisfies ChartConfig;

  const normalizedSearch = search.trim().toLocaleLowerCase();
  const filteredAgents = agents.filter(
    (agent) =>
      !normalizedSearch ||
      agent.name.toLocaleLowerCase().includes(normalizedSearch),
  );
  const toggleAgent = (agentId: string) => {
    setSelectedAgentIds((current) =>
      current.includes(agentId)
        ? current.filter((id) => id !== agentId)
        : [...current, agentId],
    );
  };
  const removeAgent = (agentId: string) => {
    setSelectedAgentIds((current) =>
      current.filter((id) => id !== agentId),
    );
  };

  const formatValue = (value: number) => {
    if (metric === "tokens") return formatTokens(value);
    if (metric === "cost") {
      return new Intl.NumberFormat(locales, {
        style: "currency",
        currency: "USD",
        maximumFractionDigits: 2,
      }).format(value);
    }
    if (metric === "time") {
      return formatDuration(value, lessThanMinuteLabel);
    }
    return value.toLocaleString(locales);
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger
        render={
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={agents.length === 0}
          />
        }
      >
        <ChartNoAxesCombined />
        {t(($) => $.comparison.open)}
      </DialogTrigger>
      <DialogContent className="max-h-[calc(100vh-2rem)] w-[min(960px,calc(100vw-2rem))] overflow-y-auto sm:max-w-[960px]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.comparison.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.comparison.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center justify-between gap-3">
          <MetricPicker value={metric} onChange={setMetric} />
          <Popover
            open={pickerOpen}
            onOpenChange={(nextOpen) => {
              setPickerOpen(nextOpen);
              if (!nextOpen) setSearch("");
            }}
          >
            <PopoverTrigger
              render={
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  aria-label={t(($) => $.comparison.select_agents)}
                />
              }
            >
              {t(($) => $.comparison.select_agents)}
              <ChevronDown
                className={`transition-transform ${pickerOpen ? "rotate-180" : ""}`}
              />
            </PopoverTrigger>
            <PopoverContent align="end" className="w-72 gap-0 p-0">
              <div className="flex items-center gap-2 border-b px-2.5 py-2">
                <Search className="h-3.5 w-3.5 text-muted-foreground" />
                <input
                  autoFocus
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  placeholder={t(($) => $.comparison.search_agents)}
                  className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                />
              </div>
              <div
                role="listbox"
                aria-multiselectable="true"
                className="max-h-72 overflow-y-auto p-1"
              >
                {filteredAgents.length === 0 ? (
                  <p className="px-2 py-6 text-center text-xs text-muted-foreground">
                    {t(($) => $.comparison.no_agents)}
                  </p>
                ) : (
                  filteredAgents.map((agent) => {
                    const selected = selectedAgentIds.includes(agent.id);
                    return (
                      <button
                        key={agent.id}
                        type="button"
                        role="option"
                        aria-selected={selected}
                        onClick={() => toggleAgent(agent.id)}
                        className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted"
                      >
                        <span
                          className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-sm border ${
                            selected
                              ? "border-primary bg-primary text-primary-foreground"
                              : "border-input"
                          }`}
                        >
                          {selected ? <Check className="h-3 w-3" /> : null}
                        </span>
                        <span className="truncate">{agent.name}</span>
                      </button>
                    );
                  })
                )}
              </div>
            </PopoverContent>
          </Popover>
        </div>

        <div className="flex min-h-7 flex-wrap items-center gap-1.5">
          {selectedAgents.length === 0 ? (
            <span className="text-xs text-muted-foreground">
              {t(($) => $.comparison.no_selection)}
            </span>
          ) : (
            selectedAgents.map((agent, index) => (
              <span
                key={agent.id}
                className="inline-flex items-center gap-1.5 rounded-full border bg-background py-1 pr-1 pl-2 text-xs"
              >
                <span
                  className="h-2 w-2 rounded-full"
                  style={{
                    backgroundColor:
                      LINE_COLORS[index % LINE_COLORS.length],
                  }}
                />
                <span className="max-w-40 truncate">{agent.name}</span>
                <button
                  type="button"
                  onClick={() => removeAgent(agent.id)}
                  aria-label={t(($) => $.comparison.remove_agent, {
                    name: agent.name,
                  })}
                  className="rounded-full p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                >
                  <X className="h-3 w-3" />
                </button>
              </span>
            ))
          )}
        </div>

        {series.length === 0 ? (
          <div className="flex h-[360px] items-center justify-center rounded-lg border border-dashed bg-muted/20 text-sm text-muted-foreground">
            {t(($) => $.comparison.no_selection)}
          </div>
        ) : (
          <ChartContainer
            config={chartConfig}
            className="h-[360px] w-full"
            initialDimension={{ width: 896, height: 360 }}
          >
            <LineChart
              data={chartData}
              margin={{ top: 8, right: 12, bottom: 0, left: 4 }}
            >
              <CartesianGrid vertical={false} />
              <XAxis
                dataKey="label"
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                interval="preserveStartEnd"
              />
              <YAxis
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                width={64}
                tickFormatter={(value: number) => formatValue(value)}
              />
              <ChartTooltip
                content={
                  <ChartTooltipContent
                    formatter={(value, name, item) => (
                      <div className="flex w-full items-center gap-2">
                        <span
                          className="h-2 w-2 shrink-0 rounded-full"
                          style={{ backgroundColor: item.color }}
                        />
                        <span className="flex-1 text-muted-foreground">
                          {name}
                        </span>
                        <span className="font-mono font-medium tabular-nums">
                          {typeof value === "number"
                            ? formatValue(value)
                            : String(value)}
                        </span>
                      </div>
                    )}
                  />
                }
              />
              {series.map((item) => (
                <Line
                  key={item.id}
                  type="monotone"
                  dataKey={item.key}
                  name={item.name}
                  stroke={`var(--color-${item.key})`}
                  strokeWidth={2}
                  strokeDasharray={item.strokeDasharray}
                  dot={{ r: 2 }}
                  activeDot={{ r: 4 }}
                />
              ))}
            </LineChart>
          </ChartContainer>
        )}
      </DialogContent>
    </Dialog>
  );
}

function MetricPicker({
  value,
  onChange,
}: {
  value: AgentComparisonMetric;
  onChange: (metric: AgentComparisonMetric) => void;
}) {
  const { t } = useT("usage");
  const options = [
    { value: "tokens" as const, label: t(($) => $.daily.metric_tokens) },
    { value: "cost" as const, label: t(($) => $.daily.metric_cost) },
    { value: "time" as const, label: t(($) => $.daily.metric_time) },
    { value: "tasks" as const, label: t(($) => $.daily.metric_tasks) },
  ];

  return (
    <div className="inline-flex items-center gap-0.5 rounded-md bg-muted p-0.5">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          onClick={() => onChange(option.value)}
          className={`rounded-sm px-2.5 py-1 text-xs font-medium transition-colors ${
            option.value === value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground"
          }`}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
