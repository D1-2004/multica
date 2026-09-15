"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import {
  Braces,
  ChevronRight,
  Clock3,
  Search,
  ShieldCheck,
  X,
} from "lucide-react";
import type { DSHTrajectoryArtifact } from "@multica/core/types/agent";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  buildTrajectorySpans,
  parseDSHTrajectory,
  trajectoryCategory,
  trajectoryEventStep,
  trajectoryEventSummary,
  trajectoryEventTurn,
  type DSHTrajectoryEvent,
  type TrajectoryCategory,
  type TrajectorySpan,
} from "./trajectory-model";

const CATEGORY_STYLES: Record<TrajectoryCategory, string> = {
  user: "bg-sky-500",
  assistant: "bg-violet-500",
  tool: "bg-amber-500",
  system: "bg-slate-400",
};

function formatDuration(ms: number): string {
  if (ms < 1_000) return `${Math.max(0, Math.round(ms))} ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(ms < 10_000 ? 2 : 1)} s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1_000)}s`;
}

function formatClock(time: number): string {
  return new Date(time).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    fractionalSecondDigits: 3,
  });
}

function TrajectoryOverview({
  events,
  spans,
  selectedSeq,
  onSelect,
}: {
  events: DSHTrajectoryEvent[];
  spans: TrajectorySpan[];
  selectedSeq: number | null;
  onSelect: (seq: number) => void;
}) {
  const { t } = useT("issues");
  if (events.length === 0) return <div className="h-16 border-b bg-muted/20" />;
  const start = events[0]!.time;
  const end = Math.max(start + 1, events.at(-1)!.time);
  const duration = end - start;

  return (
    <div className="shrink-0 border-b bg-muted/15 px-4 py-2.5">
      <div className="mb-1.5 flex items-center justify-between text-micro text-muted-foreground">
        <span>{t(($) => $.trajectory.overview)}</span>
        <span className="tabular-nums">{formatDuration(duration)}</span>
      </div>
      <div className="relative h-11 overflow-hidden rounded border bg-background">
        <div className="absolute inset-x-2 top-1/2 h-px bg-border" />
        {spans.map((span) => {
          const left = ((span.startTime - start) / duration) * 100;
          const width = Math.max(
            span.startSeq === span.endSeq ? 0.45 : 0.8,
            ((span.endTime - span.startTime) / duration) * 100,
          );
          return (
            <button
              key={`${span.startSeq}:${span.endSeq}:${span.label}`}
              type="button"
              title={`${span.label} · ${formatDuration(span.endTime - span.startTime)}`}
              aria-label={span.label}
              onClick={() => onSelect(span.startSeq)}
              className={cn(
                "absolute top-1/2 h-2.5 -translate-y-1/2 rounded-full opacity-75 transition hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                CATEGORY_STYLES[span.category],
                selectedSeq !== null &&
                  selectedSeq >= span.startSeq &&
                  selectedSeq <= span.endSeq &&
                  "h-4 opacity-100",
              )}
              style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }}
            />
          );
        })}
      </div>
    </div>
  );
}

function usageRows(event: DSHTrajectoryEvent): Array<[string, string]> {
  const usage =
    typeof event.data.usage === "object" && event.data.usage !== null
      ? (event.data.usage as Record<string, unknown>)
      : null;
  if (!usage) return [];
  return Object.entries(usage)
    .filter(([, value]) => typeof value === "number")
    .map(([key, value]) => [key, Number(value).toLocaleString()]);
}

function EventInspector({
  event,
  firstTime,
}: {
  event: DSHTrajectoryEvent | null;
  firstTime: number;
}) {
  const { t } = useT("issues");
  if (!event) {
    return (
      <div className="flex h-full items-center justify-center p-6 text-center text-caption text-muted-foreground">
        {t(($) => $.trajectory.select_event)}
      </div>
    );
  }
  const usage = usageRows(event);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <span className={cn("h-2 w-2 rounded-full", CATEGORY_STYLES[trajectoryCategory(event.type)])} />
          <span className="min-w-0 flex-1 truncate text-body font-medium">{event.type}</span>
          <span className="font-mono text-micro text-muted-foreground">#{event.seq}</span>
        </div>
        <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-caption">
          <span className="text-muted-foreground">{t(($) => $.trajectory.time)}</span>
          <span className="text-right tabular-nums">{formatClock(event.time)}</span>
          <span className="text-muted-foreground">{t(($) => $.trajectory.elapsed)}</span>
          <span className="text-right tabular-nums">+{formatDuration(event.time - firstTime)}</span>
          {trajectoryEventTurn(event) !== null && (
            <>
              <span className="text-muted-foreground">{t(($) => $.trajectory.turn)}</span>
              <span className="text-right tabular-nums">{trajectoryEventTurn(event)}</span>
            </>
          )}
          {trajectoryEventStep(event) !== null && (
            <>
              <span className="text-muted-foreground">{t(($) => $.trajectory.step)}</span>
              <span className="text-right tabular-nums">{trajectoryEventStep(event)}</span>
            </>
          )}
          {usage.map(([label, value]) => (
            <div key={label} className="contents">
              <span className="truncate text-muted-foreground">{label}</span>
              <span className="text-right tabular-nums">{value}</span>
            </div>
          ))}
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-3">
        <div className="mb-2 flex items-center gap-1.5 text-caption font-medium">
          <Braces className="h-3.5 w-3.5 text-muted-foreground" />
          {t(($) => $.trajectory.event_data)}
        </div>
        <pre className="whitespace-pre-wrap break-words rounded border bg-muted/20 p-3 font-mono text-micro leading-relaxed text-foreground">
          {JSON.stringify(event.data, null, 2)}
        </pre>
      </div>
    </div>
  );
}

export function DSHTrajectoryDialog({
  open,
  onOpenChange,
  artifact,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  artifact: DSHTrajectoryArtifact;
}) {
  const { t } = useT("issues");
  const listRef = useRef<VirtuosoHandle>(null);
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<TrajectoryCategory | "all">("all");
  const bundle = useMemo(() => {
    const parsed = parseDSHTrajectory(artifact.jsonl);
    if (
      parsed.header.id !== artifact.session_id ||
      parsed.events.length + (parsed.children ?? []).reduce((sum, child) => sum + child.events.length, 0) !== artifact.event_count
    ) {
      throw new Error("DSH trajectory metadata does not match its native ledger");
    }
    return parsed;
  }, [artifact]);
  const [selectedActivation, setSelectedActivation] = useState("root");
  const document = bundle.children?.find((child) => child.activationId === selectedActivation) ?? bundle;
  const [selectedSeq, setSelectedSeq] = useState<number | null>(
    document.events.at(-1)?.seq ?? null,
  );
  const spans = useMemo(() => buildTrajectorySpans(document.events), [document.events]);
  const visibleEvents = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return document.events.filter((event) => {
      if (category !== "all" && trajectoryCategory(event.type) !== category) return false;
      if (!normalized) return true;
      return `${event.type}\n${trajectoryEventSummary(event)}\n${JSON.stringify(event.data)}`
        .toLowerCase()
        .includes(normalized);
    });
  }, [category, document.events, query]);
  const selectedEvent =
    selectedSeq === null
      ? null
      : document.events.find((event) => event.seq === selectedSeq) ?? null;
  const categoryLabels: Record<TrajectoryCategory | "all", string> = {
    all: t(($) => $.trajectory.all_events),
    user: t(($) => $.trajectory.category_user),
    assistant: t(($) => $.trajectory.category_assistant),
    tool: t(($) => $.trajectory.category_tool),
    system: t(($) => $.trajectory.category_system),
  };

  useEffect(() => {
    if (!open || selectedSeq === null) return;
    const index = visibleEvents.findIndex((event) => event.seq === selectedSeq);
    if (index >= 0) listRef.current?.scrollToIndex({ index, align: "center" });
  }, [open, selectedSeq, visibleEvents]);

  const selectFromOverview = (seq: number) => {
    setQuery("");
    setCategory("all");
    setSelectedSeq(seq);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="!h-[calc(100dvh-2rem)] !max-h-[calc(100dvh-2rem)] !w-[calc(100vw-2rem)] !max-w-6xl overflow-hidden !gap-0 !p-0"
        showCloseButton={false}
      >
        <DialogTitle className="sr-only">{t(($) => $.trajectory.title)}</DialogTitle>
        <div className="flex h-full min-h-0 flex-col">
          <header className="flex shrink-0 items-center gap-3 border-b px-4 py-3">
            <div className="flex h-8 w-8 items-center justify-center rounded-md bg-sky-500/10 text-sky-600 dark:text-sky-400">
              <ProviderLogo provider="dsh" className="h-4 w-5" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <h2 className="truncate text-body font-semibold">{t(($) => $.trajectory.title)}</h2>
                <span className="rounded-full border px-1.5 py-0.5 text-micro text-muted-foreground">
                  DSH
                </span>
                <span className="inline-flex items-center gap-1 rounded-full bg-success/10 px-1.5 py-0.5 text-micro text-success">
                  <ShieldCheck className="h-3 w-3" />
                  {t(($) => $.trajectory.read_only)}
                </span>
              </div>
              <div className="mt-0.5 flex min-w-0 items-center gap-2 font-mono text-micro text-muted-foreground">
                <span className="truncate">{document.header.id}</span>
                <span>·</span>
                <span>
                  {t(($) => $.trajectory.events, {
                    count: artifact.event_count,
                  })}
                </span>
                <span>·</span>
                <span>{t(($) => $.trajectory.version, { version: document.header.version })}</span>
              </div>
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.trajectory.close)}
              onClick={() => onOpenChange(false)}
            >
              <X className="h-4 w-4" />
            </Button>
          </header>

          <div className="flex shrink-0 items-center gap-2 border-b px-3 py-2">
            {bundle.children && bundle.children.length > 0 && (
              <select
                aria-label={t(($) => $.trajectory.session)}
                className="h-8 max-w-52 rounded border bg-background px-2 text-caption"
                value={selectedActivation}
                onChange={(event) => {
                  const key = event.target.value;
                  setSelectedActivation(key);
                  const next = bundle.children?.find((child) => child.activationId === key) ?? bundle;
                  setSelectedSeq(next.events.at(-1)?.seq ?? null);
                  setQuery("");
                  setCategory("all");
                }}
              >
                <option value="root">{t(($) => $.trajectory.root_session)}</option>
                {bundle.children.map((child, index) => (
                  <option key={child.activationId} value={child.activationId}>
                    {t(($) => $.trajectory.child_session, { number: index + 1 })}
                    {!child.closed && ` · ${t(($) => $.trajectory.interrupted)}`}
                  </option>
                ))}
              </select>
            )}
            <div className="relative min-w-0 flex-1 sm:max-w-sm">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t(($) => $.trajectory.search)}
                className="h-8 pl-8 text-caption"
              />
            </div>
            {(["all", "user", "assistant", "tool", "system"] as const).map((item) => (
              <button
                key={item}
                type="button"
                onClick={() => setCategory(item)}
                className={cn(
                  "hidden rounded px-2 py-1 text-micro capitalize transition sm:block",
                  category === item
                    ? "bg-foreground text-background"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )}
              >
                {categoryLabels[item]}
              </button>
            ))}
            <span className="ml-auto shrink-0 text-micro tabular-nums text-muted-foreground">
              {visibleEvents.length}/{document.events.length}
            </span>
          </div>

          <TrajectoryOverview
            events={document.events}
            spans={spans}
            selectedSeq={selectedSeq}
            onSelect={selectFromOverview}
          />

          <div className="grid min-h-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,3fr)_minmax(10rem,2fr)] md:grid-cols-[minmax(0,1fr)_minmax(18rem,36%)] md:grid-rows-1">
            <section className="flex min-h-0 min-w-0 flex-col border-r">
              <div className="grid h-8 shrink-0 grid-cols-[3.5rem_9.5rem_minmax(0,1fr)_5.5rem] items-center border-b bg-muted/20 px-2 text-micro font-medium text-muted-foreground">
                <span className="text-right">#</span>
                <span className="pl-3">{t(($) => $.trajectory.event)}</span>
                <span className="pl-3">{t(($) => $.trajectory.content)}</span>
                <span className="text-right">{t(($) => $.trajectory.time)}</span>
              </div>
              <div className="min-h-0 flex-1">
                {visibleEvents.length === 0 ? (
                  <div className="flex h-full items-center justify-center text-caption text-muted-foreground">
                    {t(($) => $.trajectory.no_events)}
                  </div>
                ) : (
                  <Virtuoso
                    ref={listRef}
                    data={visibleEvents}
                    initialTopMostItemIndex={visibleEvents.length - 1}
                    itemContent={(_, event) => {
                      const prior = document.events[event.seq - (document.events[0]?.seq ?? 0) - 1];
                      const turnStart =
                        event.type === "turn/start" ||
                        (trajectoryEventTurn(event) !== null &&
                          trajectoryEventTurn(prior ?? event) !== trajectoryEventTurn(event));
                      const step = trajectoryEventStep(event);
                      return (
                        <button
                          type="button"
                          onClick={() => setSelectedSeq(event.seq)}
                          className={cn(
                            "grid h-8 w-full grid-cols-[3.5rem_9.5rem_minmax(0,1fr)_5.5rem] items-center border-b px-2 text-left text-micro transition hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                            turnStart && "border-t-2 border-t-sky-500/40",
                            selectedSeq === event.seq && "bg-accent",
                          )}
                        >
                          <span className="text-right font-mono text-muted-foreground">{event.seq}</span>
                          <span className="flex min-w-0 items-center gap-1.5 pl-3">
                            <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", CATEGORY_STYLES[trajectoryCategory(event.type)])} />
                            <span className="truncate font-mono">{event.type}</span>
                          </span>
                          <span className="flex min-w-0 items-center gap-1.5 pl-3">
                            {step !== null && (
                              <span className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-[9px] text-muted-foreground">
                                S{step}
                              </span>
                            )}
                            <span className="truncate">{trajectoryEventSummary(event)}</span>
                          </span>
                          <span className="text-right font-mono text-muted-foreground">
                            {formatClock(event.time).replace(/^.*?(?=\d{2}:\d{2}:\d{2})/, "")}
                          </span>
                        </button>
                      );
                    }}
                  />
                )}
              </div>
            </section>
            <aside className="min-h-0 border-t bg-muted/5 md:hidden">
              <EventInspector
                event={selectedEvent}
                firstTime={document.events[0]?.time ?? document.header.createdAt}
              />
            </aside>
            <aside className="hidden min-h-0 bg-muted/5 md:block">
              <EventInspector
                event={selectedEvent}
                firstTime={document.events[0]?.time ?? document.header.createdAt}
              />
            </aside>
          </div>

          <footer className="flex h-8 shrink-0 items-center gap-3 border-t bg-muted/15 px-3 text-micro text-muted-foreground">
            <span className="inline-flex items-center gap-1">
              <Clock3 className="h-3 w-3" />
              {t(($) => $.trajectory.actual_timing)}
            </span>
            <span className="inline-flex items-center gap-1">
              <ChevronRight className="h-3 w-3" />
              {t(($) => $.trajectory.select_hint)}
            </span>
          </footer>
        </div>
      </DialogContent>
    </Dialog>
  );
}
