"use client";

import { useMemo, useState } from "react";
import {
  CalendarClock,
  Loader2,
  MoreHorizontal,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  Webhook,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiError, errorCode } from "@multica/core/api";
import {
  sceneRoutineRunsOptions,
  sceneRoutinesOptions,
  useCreateSceneRoutine,
  useDeleteSceneRoutine,
  useRotateSceneRoutineWebhook,
  useRunSceneRoutine,
  useUpdateSceneRoutine,
  type ContextRoutine,
  type ContextRoutineInput,
  type ContextRoutineRun,
  type SceneRoutinesTarget,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { ConfirmDialog } from "../agents/components/tabs/connectors-ui";
import { parseCron, toCron } from "../autopilots/components/schedule-editor/cron-mapping";
import { useDescribeSchedule } from "../autopilots/components/schedule-editor/describe";
import { getDefaultScheduleConfig, type ScheduleConfig } from "../autopilots/components/schedule-editor/model";
import { ScheduleEditor } from "../autopilots/components/schedule-editor/schedule-editor";
import { WebhookUrlField } from "../autopilots/components/webhook-url-field";
import { formatInTimeZone } from "../common/format-in-time-zone";
import { useT } from "../i18n";
import { routineLocalDateTime, routineRunAt } from "./context-config-routine-time";
import { ToggleControl } from "./context-config-ui";

/** The default timezone of a new schedule, the one the server assumes. */
const ROUTINE_DEFAULT_TIMEZONE = "Asia/Shanghai";
const ROUTINE_TITLE_MAX = 120;
const ROUTINE_INSTRUCTIONS_MAX = 8000;

type RoutineKind = "schedule" | "once" | "webhook";

/**
 * The routines (例行任务) of one group or 1:1 chat scene: what runs when,
 * what happened last time, and the controls to add, pause, run, edit and
 * delete them. Shared by the DingTalk configure page (`target.kind ===
 * "config"`) and the admin Context Builder (`"node"`). Read-only without
 * `canEdit`.
 */
export function ScopeRoutines({
  target,
  canEdit,
  bare = false,
  reportError,
}: {
  target: SceneRoutinesTarget;
  canEdit: boolean;
  /** Just the list and its add button: no heading or explanation (the
   * configure page, whose tab already names it). */
  bare?: boolean;
  reportError: (error: unknown) => boolean;
}) {
  const { t } = useT("agents");
  const routines = useQuery(sceneRoutinesOptions(target));
  const [editing, setEditing] = useState<ContextRoutine | "new" | null>(null);
  const [revealUrl, setRevealUrl] = useState<{ title: string; url: string } | null>(null);
  const list = routines.data ?? [];

  const fail = (error: unknown) => {
    if (reportError(error)) return;
    toast.error(routineErrorMessage(t, error));
  };

  let body: React.ReactNode;
  if (routines.isPending) {
    body = (
      <p className="flex items-center gap-2 py-6 text-caption text-muted-foreground" role="status">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.context_config.routines.loading)}
      </p>
    );
  } else if (routines.isError) {
    body = (
      <div className="space-y-2 py-4">
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.routines.load_failed)}</p>
        <Button size="sm" variant="outline" onClick={() => void routines.refetch()}>
          {t(($) => $.context_config.routines.retry)}
        </Button>
      </div>
    );
  } else if (list.length === 0) {
    body = (
      <div className="flex flex-col items-start gap-3 rounded-lg border border-dashed px-4 py-6">
        <div className="space-y-1">
          <p className="text-body font-medium">{t(($) => $.context_config.routines.empty_title)}</p>
          {bare ? null : (
            <p className="text-caption text-muted-foreground text-pretty">
              {t(($) => $.context_config.routines.empty_hint)}
            </p>
          )}
        </div>
        {canEdit ? (
          <Button size="sm" onClick={() => setEditing("new")}>
            <Plus className="size-4" />
            {t(($) => $.context_config.routines.add)}
          </Button>
        ) : null}
      </div>
    );
  } else {
    body = (
      <ul className="divide-y rounded-lg border bg-card">
        {list.map((routine) => (
          <RoutineRow
            key={routine.id}
            routine={routine}
            target={target}
            canEdit={canEdit}
            bare={bare}
            onEdit={() => setEditing(routine)}
            onRotated={(url) => setRevealUrl({ title: routine.title, url })}
            onError={fail}
          />
        ))}
      </ul>
    );
  }

  return (
    <section className="space-y-3" aria-labelledby={bare ? undefined : "scene-routines-title"} aria-label={bare ? t(($) => $.context_config.routines.title) : undefined}>
      {bare ? (
        canEdit && list.length > 0 ? (
          <div className="flex justify-end">
            <Button size="sm" variant="outline" onClick={() => setEditing("new")}>
              <Plus className="size-4" />
              {t(($) => $.context_config.routines.add)}
            </Button>
          </div>
        ) : null
      ) : (
      <div className="flex min-h-8 items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h3 id="scene-routines-title" className="text-body font-semibold">
            {t(($) => $.context_config.routines.title)}
          </h3>
          <p className="text-caption text-muted-foreground text-pretty">
            {canEdit
              ? t(($) => $.context_config.routines.subtitle)
              : t(($) => $.context_config.routines.read_only)}
          </p>
        </div>
        {canEdit && list.length > 0 ? (
          <Button size="sm" variant="outline" className="shrink-0" onClick={() => setEditing("new")}>
            <Plus className="size-4" />
            {t(($) => $.context_config.routines.add)}
          </Button>
        ) : null}
      </div>
      )}
      {body}
      {editing !== null ? (
        <RoutineEditor
          target={target}
          routine={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onWebhookMinted={(title, url) => setRevealUrl({ title, url })}
          onError={fail}
        />
      ) : null}
      <WebhookRevealDialog value={revealUrl} onClose={() => setRevealUrl(null)} />
    </section>
  );
}

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

/** A refusal the server explains with a code, in the page's words. */
function routineErrorMessage(t: AgentsT, error: unknown): string {
  switch (errorCode(error)) {
    case "routine_requires_dingtalk_identity":
      return t(($) => $.context_config.routines.error_identity);
    case "dm_target_unknown":
      return t(($) => $.context_config.routines.error_dm_target);
    case "agent_runtime_required":
      return t(($) => $.context_config.routines.error_runtime);
    case "routine_duplicate":
      return t(($) => $.context_config.routines.error_duplicate);
    case "routine_paused":
      return t(($) => $.context_config.routines.error_paused);
    case "manager_only":
      return t(($) => $.context_config.routines.error_manager_only);
    case "invalid_routine":
      return t(($) => $.context_config.routines.error_invalid, {
        detail: error instanceof ApiError ? error.message : "",
      });
    default:
      return t(($) => $.context_config.routines.error_generic);
  }
}

/** A routine's rhythm in plain words: "工作日 09:00" or "收到 Webhook 请求时". */
function useRoutineRhythm() {
  const { t, i18n } = useT("agents");
  const describe = useDescribeSchedule();
  return (routine: ContextRoutine): string => {
    if (routine.trigger.kind === "once") {
      return routine.trigger.runAt
        ? t(($) => $.context_config.routines.rhythm_once, { time: formatInTimeZone(routine.trigger.runAt, undefined, i18n.language) })
        : t(($) => $.context_config.routines.rhythm_unknown);
    }
    if (routine.trigger.kind === "webhook") return t(($) => $.context_config.routines.rhythm_webhook);
    if (routine.trigger.kind !== "schedule" || !routine.trigger.cron) {
      return t(($) => $.context_config.routines.rhythm_unknown);
    }
    const config = parseCron(routine.trigger.cron, routine.trigger.timezone || ROUTINE_DEFAULT_TIMEZONE);
    return describe(config) ?? routine.trigger.cron;
  };
}

function RoutineRow({
  routine,
  target,
  canEdit,
  bare,
  onEdit,
  onRotated,
  onError,
}: {
  routine: ContextRoutine;
  target: SceneRoutinesTarget;
  canEdit: boolean;
  /** No secondary labels beyond rhythm and status. */
  bare: boolean;
  onEdit: () => void;
  onRotated: (url: string) => void;
  onError: (error: unknown) => void;
}) {
  const { t, i18n } = useT("agents");
  const rhythm = useRoutineRhythm();
  const update = useUpdateSceneRoutine(target);
  const run = useRunSceneRoutine(target);
  const remove = useDeleteSceneRoutine(target);
  const rotate = useRotateSceneRoutineWebhook(target);
  const [confirm, setConfirm] = useState<"delete" | "rotate" | null>(null);
  const [detailOpen, setDetailOpen] = useState(false);
  const isWebhook = routine.trigger.kind === "webhook";
  const isOnce = routine.trigger.kind === "once";
  const consumed = isOnce && (routine.trigger.consumed === true || routine.lastRun != null);
  const timezone = routine.trigger.kind === "once" ? undefined : routine.trigger.timezone || ROUTINE_DEFAULT_TIMEZONE;
  const when = (iso: string) => formatInTimeZone(iso, timezone, i18n.language);
  const Icon = isWebhook ? Webhook : CalendarClock;

  const toggle = (enabled: boolean) =>
    update.mutate({ routineId: routine.id, patch: { enabled } }, { onError });
  const runNow = () =>
    run.mutate(routine.id, {
      onSuccess: (started) => {
        if (started?.status === "skipped") {
          toast.message(
            t(($) => $.context_config.routines.run_skipped, { reason: started.failureReason || "—" }),
          );
        } else {
          toast.success(t(($) => $.context_config.routines.run_started));
        }
      },
      onError,
    });

  const last = routine.lastRun;
  const lastTone =
    last?.status === "completed"
      ? "bg-success"
      : last?.status === "failed"
        ? "bg-destructive"
        : last?.status === "running"
          ? "bg-info"
          : "bg-muted-foreground/40";
  const lastText = !last
    ? t(($) => $.context_config.routines.never_run)
    : last.status === "completed"
      ? t(($) => $.context_config.routines.last_ok, { time: when(last.completedAt ?? last.createdAt) })
      : last.status === "failed"
        ? t(($) => $.context_config.routines.last_failed, { time: when(last.createdAt) })
        : last.status === "running"
          ? t(($) => $.context_config.routines.last_running, { time: when(last.createdAt) })
          : last.status === "skipped"
            ? t(($) => $.context_config.routines.last_skipped, { time: when(last.createdAt) })
            : t(($) => $.context_config.routines.last_status, { status: last.status, time: when(last.createdAt) });

  return (
    <li className="flex items-start gap-1 p-1.5 sm:p-2" aria-label={routine.title}>
      {/* The routine's summary opens its detail and run history; its text
          is the button's name. */}
      <button
        type="button"
        onClick={() => setDetailOpen(true)}
        className="flex min-w-0 flex-1 items-start gap-3 rounded-md p-1.5 text-left transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:p-2"
      >
        <span
          className={cn(
            "mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground",
            routine.enabled && "bg-accent text-accent-foreground",
          )}
          aria-hidden="true"
        >
          <Icon className="size-4" />
        </span>
        <span className={cn("block min-w-0 flex-1 space-y-1", !routine.enabled && "opacity-70")}>
          <span className="block text-body font-medium break-words">{routine.title}</span>
          <span className="block text-label text-foreground/90">{rhythm(routine)}</span>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-caption text-muted-foreground">
            {consumed ? (
              <span>{t(($) => $.context_config.routines.once_triggered)}</span>
            ) : !routine.enabled ? (
              <span className="font-medium text-warning">
                {routine.pauseReason
                  ? t(($) => $.context_config.routines.system_paused, { reason: routine.pauseReason })
                  : t(($) => $.context_config.routines.paused)}
              </span>
            ) : routine.trigger.nextRunAt ? (
              <span>{t(($) => $.context_config.routines.next_run, { time: when(routine.trigger.nextRunAt) })}</span>
            ) : null}
            <span className="inline-flex items-center gap-1.5">
              <span className={cn("size-1.5 shrink-0 rounded-full", lastTone)} aria-hidden="true" />
              {lastText}
            </span>
            {routine.createdByType === "agent" && !bare ? (
              <span>{t(($) => $.context_config.routines.created_in_chat)}</span>
            ) : null}
          </span>
          {isWebhook && routine.trigger.webhookUrlMasked ? (
            <span className="block truncate font-mono text-micro text-muted-foreground" title={routine.trigger.webhookUrlMasked}>
              {routine.trigger.webhookUrlMasked}
            </span>
          ) : null}
          {last?.status === "failed" && last.failureReason ? (
            <span className="line-clamp-2 text-caption text-destructive break-words">{last.failureReason}</span>
          ) : null}
          <span className="sr-only">{t(($) => $.context_config.routines.open_detail_hint)}</span>
        </span>
      </button>
      {canEdit ? (
        <div className="flex shrink-0 items-center gap-0.5 pt-1.5 sm:pt-2">
          <ToggleControl
            busy={update.isPending}
            disabled={consumed}
            checked={routine.enabled}
            label={t(($) => $.context_config.routines.toggle, { name: routine.title })}
            onToggle={toggle}
          />
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-10 sm:size-8"
                  aria-label={t(($) => $.context_config.routines.actions, { name: routine.title })}
                >
                  {run.isPending || remove.isPending || rotate.isPending ? (
                    <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
                  ) : (
                    <MoreHorizontal className="size-4" />
                  )}
                </Button>
              }
            />
            <DropdownMenuContent align="end">
              <DropdownMenuItem disabled={!routine.enabled || run.isPending || isOnce} onClick={runNow}>
                <Play className="size-4" />
                {t(($) => $.context_config.routines.run_now)}
              </DropdownMenuItem>
              <DropdownMenuItem disabled={consumed} onClick={onEdit}>
                <Pencil className="size-4" />
                {t(($) => $.context_config.routines.edit)}
              </DropdownMenuItem>
              {isWebhook ? (
                <DropdownMenuItem onClick={() => setConfirm("rotate")}>
                  <RefreshCw className="size-4" />
                  {t(($) => $.context_config.routines.rotate)}
                </DropdownMenuItem>
              ) : null}
              <DropdownMenuItem variant="destructive" onClick={() => setConfirm("delete")}>
                <Trash2 className="size-4" />
                {t(($) => $.context_config.routines.delete)}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      ) : null}
      <RoutineDetailDialog
        routine={routine}
        target={target}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />
      <ConfirmDialog
        open={confirm === "delete"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t(($) => $.context_config.routines.delete_title, { name: routine.title })}
        description={t(($) => $.context_config.routines.delete_description)}
        confirmLabel={t(($) => $.context_config.routines.delete)}
        pending={remove.isPending}
        onConfirm={() =>
          remove.mutate(routine.id, {
            onSuccess: () => setConfirm(null),
            onError: (error) => {
              setConfirm(null);
              onError(error);
            },
          })
        }
      />
      <ConfirmDialog
        open={confirm === "rotate"}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t(($) => $.context_config.routines.rotate_title)}
        description={t(($) => $.context_config.routines.rotate_description)}
        confirmLabel={t(($) => $.context_config.routines.rotate_confirm)}
        pending={rotate.isPending}
        onConfirm={() =>
          rotate.mutate(routine.id, {
            onSuccess: (rotated) => {
              setConfirm(null);
              if (rotated?.trigger.webhookUrl) onRotated(rotated.trigger.webhookUrl);
            },
            onError: (error) => {
              setConfirm(null);
              onError(error);
            },
          })
        }
      />
    </li>
  );
}


/** A run's status in words. */
function useRunStatusLabel(): (run: ContextRoutineRun) => string {
  const { t } = useT("agents");
  return (run) => {
    switch (run.status) {
      case "completed":
        return t(($) => $.context_config.routines.status_completed);
      case "failed":
        return t(($) => $.context_config.routines.status_failed);
      case "running":
      case "issue_created":
        return t(($) => $.context_config.routines.status_running);
      case "skipped":
        return t(($) => $.context_config.routines.status_skipped);
      case "pending":
        return t(($) => $.context_config.routines.status_pending);
      default:
        return run.status;
    }
  };
}

/** What started a run, in words. */
function useRunSourceLabel(): (run: ContextRoutineRun) => string {
  const { t } = useT("agents");
  return (run) => {
    switch (run.source) {
      case "schedule":
        return t(($) => $.context_config.routines.source_schedule);
      case "once":
        return t(($) => $.context_config.routines.trigger_once);
      case "webhook":
        return t(($) => $.context_config.routines.source_webhook);
      case "manual":
        return t(($) => $.context_config.routines.source_manual);
      case "api":
        return t(($) => $.context_config.routines.source_api);
      case "dingtalk_message":
        return t(($) => $.context_config.routines.source_message);
      default:
        return run.source;
    }
  };
}

function runTone(status: string): string {
  switch (status) {
    case "completed":
      return "bg-success";
    case "failed":
      return "bg-destructive";
    case "running":
    case "issue_created":
      return "bg-info";
    default:
      return "bg-muted-foreground/40";
  }
}

/** A routine opened from its row: what it does, when it runs and its run
 * history (status and times only; a run's own output is posted in the
 * chat). */
function RoutineDetailDialog({
  routine,
  target,
  open,
  onOpenChange,
}: {
  routine: ContextRoutine;
  target: SceneRoutinesTarget;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t, i18n } = useT("agents");
  const rhythm = useRoutineRhythm();
  const statusLabel = useRunStatusLabel();
  const sourceLabel = useRunSourceLabel();
  const runsOptions = sceneRoutineRunsOptions(target, routine.id);
  // Fetched only while the detail is open.
  const runs = useQuery({ ...runsOptions, enabled: open && runsOptions.enabled !== false });
  const timezone = routine.trigger.kind === "once" ? undefined : routine.trigger.timezone || ROUTINE_DEFAULT_TIMEZONE;
  const when = (iso: string) => formatInTimeZone(iso, timezone, i18n.language);
  const upcoming = routine.enabled ? routine.trigger.nextRuns.slice(0, 3) : [];

  let history: React.ReactNode;
  if (runs.isPending) {
    history = (
      <p className="flex items-center gap-2 text-caption text-muted-foreground" role="status">
        <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
        {t(($) => $.context_config.routines.history_loading)}
      </p>
    );
  } else if (runs.isError) {
    history = (
      <div className="flex items-center gap-2">
        <p className="text-caption text-muted-foreground">{t(($) => $.context_config.routines.history_failed)}</p>
        <Button size="sm" variant="ghost" onClick={() => void runs.refetch()}>
          {t(($) => $.context_config.routines.retry)}
        </Button>
      </div>
    );
  } else if (runs.data.length === 0) {
    history = <p className="text-caption text-muted-foreground">{t(($) => $.context_config.routines.history_empty)}</p>;
  } else {
    history = (
      <ol className="divide-y rounded-lg border" aria-label={t(($) => $.context_config.routines.history_title)}>
        {runs.data.map((run) => (
          <li key={run.id} className="flex items-start gap-2.5 px-3 py-2.5">
            <span className={cn("mt-1.5 size-2 shrink-0 rounded-full", runTone(run.status))} aria-hidden="true" />
            <div className="min-w-0 flex-1 space-y-0.5">
              <p className="text-body">
                {statusLabel(run)}
                <span className="text-muted-foreground"> · {sourceLabel(run)}</span>
              </p>
              <p className="text-caption text-muted-foreground tabular-nums">
                {run.completedAt
                  ? t(($) => $.context_config.routines.history_span, { start: when(run.createdAt), end: when(run.completedAt) })
                  : when(run.createdAt)}
              </p>
              {run.failureReason ? (
                <p className="line-clamp-2 text-caption text-destructive break-words">{run.failureReason}</p>
              ) : null}
            </div>
          </li>
        ))}
      </ol>
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-md">
        <DialogHeader className="space-y-1 border-b p-4 pr-12 text-left">
          <DialogTitle className="break-words">{routine.title}</DialogTitle>
          <DialogDescription>
            {rhythm(routine)}
            {!routine.enabled ? ` · ${t(($) => $.context_config.routines.paused)}` : ""}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-4">
          {routine.instructions ? (
            <section className="space-y-1.5">
              <h3 className="text-caption font-medium text-muted-foreground">
                {t(($) => $.context_config.routines.field_instructions)}
              </h3>
              <p className="whitespace-pre-wrap break-words text-body">{routine.instructions}</p>
            </section>
          ) : null}
          {upcoming.length > 0 ? (
            <section className="space-y-1.5">
              <h3 className="text-caption font-medium text-muted-foreground">
                {t(($) => $.context_config.routines.upcoming_title)}
              </h3>
              <ul className="space-y-0.5 text-body tabular-nums">
                {upcoming.map((time) => (
                  <li key={time}>{when(time)}</li>
                ))}
              </ul>
            </section>
          ) : null}
          <section className="space-y-1.5">
            <h3 className="text-caption font-medium text-muted-foreground">
              {t(($) => $.context_config.routines.history_title)}
            </h3>
            {history}
          </section>
        </div>
      </DialogContent>
    </Dialog>
  );
}

/** Create or edit a routine: a bottom sheet on a phone, a dialog on a
 * wider screen. */
function RoutineEditor({
  target,
  routine,
  onClose,
  onWebhookMinted,
  onError,
}: {
  target: SceneRoutinesTarget;
  routine: ContextRoutine | null;
  onClose: () => void;
  onWebhookMinted: (title: string, url: string) => void;
  onError: (error: unknown) => void;
}) {
  const { t } = useT("agents");
  const isMobile = useIsMobile();
  const create = useCreateSceneRoutine(target);
  const update = useUpdateSceneRoutine(target);
  const pending = create.isPending || update.isPending;
  const initialKind: RoutineKind = routine?.trigger.kind === "once" ? "once" : routine?.trigger.kind === "webhook" ? "webhook" : "schedule";
  const [title, setTitle] = useState(routine?.title ?? "");
  const [instructions, setInstructions] = useState(routine?.instructions ?? "");
  const [kind, setKind] = useState<RoutineKind>(initialKind);
  const [schedule, setSchedule] = useState<ScheduleConfig>(() =>
    routine?.trigger.kind === "schedule" && routine.trigger.cron
      ? parseCron(routine.trigger.cron, routine.trigger.timezone || ROUTINE_DEFAULT_TIMEZONE)
      : getDefaultScheduleConfig(ROUTINE_DEFAULT_TIMEZONE),
  );
  const [localRunAt, setLocalRunAt] = useState(() => routineLocalDateTime(routine?.trigger.runAt ?? new Date(Date.now() + 15 * 60_000).toISOString()));
  const runAt = routineRunAt(localRunAt);
  const onceValid = runAt !== null && new Date(runAt).getTime() > Date.now();
  const localTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [scheduleValid, setScheduleValid] = useState(true);
  const [showErrors, setShowErrors] = useState(false);
  const titleMissing = title.trim() === "";
  const instructionsMissing = instructions.trim() === "";
  const wsId = target.kind === "node" ? target.wsId : "";
  const idPrefix = `routine-${routine?.id ?? "new"}`;

  const submit = () => {
    setShowErrors(true);
    if (titleMissing || instructionsMissing || (kind === "schedule" && !scheduleValid) || (kind === "once" && (!runAt || new Date(runAt).getTime() <= Date.now()))) return;
    const cron = kind === "schedule" ? toCron(schedule) : "";
    if (routine) {
      const patch =
        kind === "schedule"
          ? { title: title.trim(), instructions: instructions.trim(), cron, timezone: schedule.timezone }
          : kind === "once"
            ? { title: title.trim(), instructions: instructions.trim(), run_at: runAt! }
            : { title: title.trim(), instructions: instructions.trim() };
      update.mutate(
        { routineId: routine.id, patch },
        { onSuccess: () => onClose(), onError },
      );
      return;
    }
    const input: ContextRoutineInput = {
      title: title.trim(),
      instructions: instructions.trim(),
      trigger: kind === "schedule" ? { kind, cron, timezone: schedule.timezone } : kind === "once" ? { kind, run_at: runAt! } : { kind },
    };
    create.mutate(input, {
      onSuccess: (result) => {
        onClose();
        if (result?.updated) toast.message(t(($) => $.context_config.routines.updated_existing));
        else toast.success(t(($) => $.context_config.routines.created));
        if (result?.routine.trigger.webhookUrl) onWebhookMinted(result.routine.title, result.routine.trigger.webhookUrl);
      },
      onError,
    });
  };

  const heading = routine
    ? t(($) => $.context_config.routines.editor_edit_title)
    : t(($) => $.context_config.routines.editor_create_title);
  const description = t(($) => $.context_config.routines.editor_description);

  const form = (
    <form
      id={`${idPrefix}-form`}
      className="space-y-5"
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <div className="space-y-1.5">
        <label htmlFor={`${idPrefix}-title`} className="text-label font-medium">
          {t(($) => $.context_config.routines.field_title)}
        </label>
        <Input
          id={`${idPrefix}-title`}
          value={title}
          maxLength={ROUTINE_TITLE_MAX}
          placeholder={t(($) => $.context_config.routines.field_title_placeholder)}
          aria-invalid={showErrors && titleMissing}
          onChange={(event) => setTitle(event.target.value)}
          className="h-10"
        />
        {showErrors && titleMissing ? (
          <p className="text-caption text-destructive">{t(($) => $.context_config.routines.field_title_required)}</p>
        ) : null}
      </div>
      <div className="space-y-1.5">
        <label htmlFor={`${idPrefix}-instructions`} className="text-label font-medium">
          {t(($) => $.context_config.routines.field_instructions)}
        </label>
        <Textarea
          id={`${idPrefix}-instructions`}
          value={instructions}
          maxLength={ROUTINE_INSTRUCTIONS_MAX}
          rows={5}
          placeholder={t(($) => $.context_config.routines.field_instructions_placeholder)}
          aria-invalid={showErrors && instructionsMissing}
          onChange={(event) => setInstructions(event.target.value)}
          className="min-h-28"
        />
        <p className={cn("text-caption", showErrors && instructionsMissing ? "text-destructive" : "text-muted-foreground")}>
          {showErrors && instructionsMissing
            ? t(($) => $.context_config.routines.field_instructions_required)
            : t(($) => $.context_config.routines.field_instructions_hint)}
        </p>
      </div>
      <div className="space-y-2">
        <p className="text-label font-medium">{t(($) => $.context_config.routines.field_trigger)}</p>
        {routine ? null : (
          <Tabs value={kind} onValueChange={(value) => setKind(value === "once" ? "once" : value === "webhook" ? "webhook" : "schedule")}>
            <TabsList className="!h-10 w-full">
              <TabsTrigger value="once">
                <CalendarClock className="size-4" />
                {t(($) => $.context_config.routines.trigger_once)}
              </TabsTrigger>
              <TabsTrigger value="schedule">
                <CalendarClock className="size-4" />
                {t(($) => $.context_config.routines.trigger_schedule)}
              </TabsTrigger>
              <TabsTrigger value="webhook">
                <Webhook className="size-4" />
                {t(($) => $.context_config.routines.trigger_webhook)}
              </TabsTrigger>
            </TabsList>
          </Tabs>
        )}
        {kind === "once" ? (
          <div className="space-y-1.5">
            <label htmlFor={`${idPrefix}-run-at`} className="text-label font-medium">
              {t(($) => $.context_config.routines.field_run_at)}
            </label>
            <Input
              id={`${idPrefix}-run-at`}
              type="datetime-local"
              step={1}
              value={localRunAt}
              min={routineLocalDateTime(new Date().toISOString())}
              disabled={pending}
              aria-invalid={showErrors && !onceValid}
              onChange={(event) => setLocalRunAt(event.target.value)}
              className="h-10"
            />
            <p className={cn("text-caption", showErrors && !onceValid ? "text-destructive" : "text-muted-foreground")}>
              {showErrors && !onceValid
                ? t(($) => $.context_config.routines.field_run_at_invalid)
                : t(($) => $.context_config.routines.once_hint, { timezone: localTimezone })}
            </p>
          </div>
        ) : kind === "schedule" ? (
          <ScheduleEditor
            value={schedule}
            onChange={setSchedule}
            wsId={wsId}
            serverPreview={target.kind === "node"}
            onValidityChange={setScheduleValid}
            disabled={pending}
          />
        ) : (
          <p className="rounded-md bg-muted px-3 py-2.5 text-caption text-muted-foreground text-pretty">
            {routine
              ? t(($) => $.context_config.routines.webhook_edit_hint)
              : t(($) => $.context_config.routines.webhook_create_hint)}
          </p>
        )}
      </div>
    </form>
  );

  const actions = (
    <>
      <Button type="button" variant="outline" className="h-10 sm:h-9" disabled={pending} onClick={onClose}>
        {t(($) => $.context_config.routines.cancel)}
      </Button>
      <Button type="submit" form={`${idPrefix}-form`} className="h-10 sm:h-9" disabled={pending}>
        {pending ? <Loader2 className="size-4 animate-spin motion-reduce:animate-none" /> : null}
        {routine ? t(($) => $.context_config.routines.save) : t(($) => $.context_config.routines.create)}
      </Button>
    </>
  );

  const close = (open: boolean) => {
    if (!open && !pending) onClose();
  };

  if (isMobile) {
    return (
      <Sheet open onOpenChange={close}>
        <SheetContent
          side="bottom"
          className="max-h-[92dvh] gap-0 rounded-t-xl pb-[env(safe-area-inset-bottom)]"
        >
          <SheetHeader className="border-b">
            <SheetTitle>{heading}</SheetTitle>
            <SheetDescription>{description}</SheetDescription>
          </SheetHeader>
          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-4">{form}</div>
          <SheetFooter className="grid grid-cols-2 gap-2 border-t">{actions}</SheetFooter>
        </SheetContent>
      </Sheet>
    );
  }
  return (
    <Dialog open onOpenChange={close}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 p-0 sm:max-w-xl">
        <DialogHeader className="border-b px-6 py-4">
          <DialogTitle>{heading}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">{form}</div>
        <DialogFooter className="mx-0 mb-0 px-6 py-3">{actions}</DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** The full webhook URL right after it was minted: the only time it is
 * shown. */
function WebhookRevealDialog({
  value,
  onClose,
}: {
  value: { title: string; url: string } | null;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const open = value !== null;
  const title = useMemo(() => value?.title ?? "", [value]);
  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="grid-cols-[minmax(0,1fr)] sm:max-w-lg">
        <DialogHeader className="min-w-0 pr-8">
          <DialogTitle className="break-words [overflow-wrap:anywhere]">{t(($) => $.context_config.routines.webhook_reveal_title, { name: title })}</DialogTitle>
          <DialogDescription>{t(($) => $.context_config.routines.webhook_reveal_description)}</DialogDescription>
        </DialogHeader>
        {value ? <WebhookUrlField url={value.url} size="md" /> : null}
        <DialogFooter>
          <Button className="h-10 sm:h-9" onClick={onClose}>
            {t(($) => $.context_config.routines.webhook_reveal_done)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
