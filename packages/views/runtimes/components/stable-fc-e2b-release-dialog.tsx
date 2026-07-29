"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Check, ChevronsRight, Clock3, Loader2, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import {
  isReadyFCE2BTemplate,
  type FCE2BTemplate,
  useCreateFCE2BStableRelease,
  useFCE2BStableChannel,
  useFCE2BTemplates,
  useMutateFCE2BStableRelease,
  type FCE2BStableReleaseAction,
} from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

function displayTemplate(template: FCE2BTemplate): string {
  return template.name || template.template || template.id || "";
}

function formatTemplateUpdatedAt(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

function formatRolloutTime(value: string): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

const rolloutOffsetByBatch: Record<number, string> = {
  1: "T+0",
  2: "T+2h",
  3: "T+8h",
  4: "T+20h",
  5: "T+24h",
};

export function StableFCE2BReleaseDialog({
  onClose,
}: {
  onClose: () => void;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const channelQuery = useFCE2BStableChannel();
  const templatesQuery = useFCE2BTemplates(wsId);
  const createRelease = useCreateFCE2BStableRelease();
  const pauseRelease = useMutateFCE2BStableRelease("pause");
  const resumeRelease = useMutateFCE2BStableRelease("resume");
  const startRollout = useMutateFCE2BStableRelease("start-rollout");
  const advanceRollout = useMutateFCE2BStableRelease("advance-rollout");
  const completeObservation = useMutateFCE2BStableRelease(
    "complete-observation",
  );
  const terminateRelease = useMutateFCE2BStableRelease("terminate");
  const rollbackRelease = useMutateFCE2BStableRelease("rollback");
  const [selected, setSelected] = useState<FCE2BTemplate | null>(null);
  const [note, setNote] = useState("");
  const active = channelQuery.data?.active_release ?? null;
  const current = channelQuery.data?.current ?? null;
  const bootstrap = current == null;
  const templates = (templatesQuery.data ?? []).filter(isReadyFCE2BTemplate);
  const developerProgress =
    active?.status === "developer_rollout" ||
    active?.status === "awaiting_rollout";
  const progressUpdated = developerProgress
    ? active?.developer_updated_targets ?? 0
    : active?.updated_targets ?? 0;
  const progressTotal = developerProgress
    ? active?.developer_targets ?? 0
    : active?.total_targets ?? 0;
  const progressPercentage = developerProgress
    ? progressTotal > 0
      ? Math.round((progressUpdated / progressTotal) * 100)
      : 100
    : active?.target_percentage ?? 0;
  const nextRolloutMilestone =
    active?.status === "rolling_out"
      ? active.rollout_schedule?.find(
          (milestone) =>
            milestone.kind === "rollout" &&
            milestone.batch === active.current_batch + 1,
        )
      : undefined;
  const canRollback =
    active != null &&
    !active.bootstrap &&
    active.updated_targets > 0 &&
    [
      "developer_rollout",
      "awaiting_rollout",
      "rolling_out",
      "observing",
      "paused",
      "failed",
    ].includes(active.status);

  const activeStatusLabel = (() => {
    switch (active?.status) {
      case "validating":
        return t(($) => $.fc_e2b_stable.status.validating);
      case "developer_rollout":
        return t(($) => $.fc_e2b_stable.status.developer_rollout);
      case "awaiting_rollout":
        return t(($) => $.fc_e2b_stable.status.awaiting_rollout);
      case "rolling_out":
        return t(($) => $.fc_e2b_stable.status.rolling_out);
      case "observing":
        return t(($) => $.fc_e2b_stable.status.observing);
      case "paused":
        return t(($) => $.fc_e2b_stable.status.paused);
      case "rolling_back":
        return t(($) => $.fc_e2b_stable.status.rolling_back);
      default:
        return active?.status ?? "";
    }
  })();

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selected?.id || !selected.build_id) return;
    try {
      await createRelease.mutateAsync({
        idempotencyKey: crypto.randomUUID(),
        data: {
          template_id: selected.id,
          expected_build_id: selected.build_id,
          note: note.trim(),
        },
      });
      toast.success(t(($) => $.fc_e2b_stable.toast_submitted));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.fc_e2b_stable.toast_failed),
      );
    }
  };

  const mutate = async (
    action: FCE2BStableReleaseAction,
    releaseId: string,
  ) => {
    const mutation = {
      pause: pauseRelease,
      resume: resumeRelease,
      "start-rollout": startRollout,
      "advance-rollout": advanceRollout,
      "complete-observation": completeObservation,
      terminate: terminateRelease,
      rollback: rollbackRelease,
    }[action];
    try {
      await mutation.mutateAsync(releaseId);
      toast.success(t(($) => $.fc_e2b_stable.toast_updated));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.fc_e2b_stable.toast_failed),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_stable.title)}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {t(($) => $.fc_e2b_stable.description)}
          </DialogDescription>
        </DialogHeader>

        {current && (
          <div className="space-y-1.5 rounded-md border border-primary/25 bg-primary/5 p-3 text-xs">
            <div className="flex items-center gap-2">
              <ShieldCheck className="h-3.5 w-3.5 text-primary" />
              <span className="font-medium">
                {t(($) => $.fc_e2b_stable.current_stable)}
              </span>
            </div>
            <p className="truncate font-medium" title={current.template_alias}>
              {current.template_alias}
            </p>
            <p
              className="truncate text-muted-foreground"
              title={`${current.template_id} · ${current.template_build_id}`}
            >
              {current.template_id} · {current.template_build_id}
            </p>
          </div>
        )}

        {active ? (
          <div className="space-y-4">
            <div className="space-y-2 rounded-md border p-3 text-xs">
              <div className="flex items-center justify-between gap-3">
                <span className="font-medium">{active.template_alias}</span>
                <span className="rounded bg-muted px-2 py-0.5">
                  {activeStatusLabel}
                </span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full bg-primary transition-[width]"
                  style={{ width: `${progressPercentage}%` }}
                />
              </div>
              <div className="flex justify-between text-muted-foreground">
                <span>
                  {t(($) => $.fc_e2b_stable.progress, {
                    updated: progressUpdated,
                    total: progressTotal,
                  })}
                </span>
                <span>{progressPercentage}%</span>
              </div>
              {active.status === "awaiting_rollout" && (
                <p className="text-muted-foreground">
                  {t(($) => $.fc_e2b_stable.awaiting_rollout_notice)}
                </p>
              )}
              {active.validation_error && (
                <p className="text-destructive">{active.validation_error}</p>
              )}
            </div>
            {active.rollout_schedule && active.rollout_schedule.length > 0 && (
              <section className="space-y-3 rounded-md border bg-muted/20 p-3">
                <div className="flex items-start justify-between gap-4">
                  <div className="space-y-1">
                    <h3 className="flex items-center gap-2 text-xs font-medium">
                      <Clock3 className="h-3.5 w-3.5 text-muted-foreground" />
                      {t(($) => $.fc_e2b_stable.rollout_schedule_title)}
                    </h3>
                    <p className="text-[11px] leading-4 text-muted-foreground">
                      {t(($) => $.fc_e2b_stable.rollout_schedule_hint)}
                    </p>
                  </div>
                </div>
                <ol className="grid gap-2 sm:grid-cols-5">
                  {active.rollout_schedule.map((milestone) => {
                    const reached =
                      milestone.kind === "rollout" &&
                      (active.current_batch > milestone.batch ||
                        active.status === "observing");
                    const currentMilestone =
                      (milestone.kind === "rollout" &&
                        active.current_batch === milestone.batch &&
                        active.status === "rolling_out") ||
                      (milestone.kind === "complete" &&
                        active.status === "observing");
                    return (
                      <li
                        key={`${milestone.kind}:${milestone.batch}`}
                        aria-current={currentMilestone ? "step" : undefined}
                        className={[
                          "space-y-1.5 rounded-md border px-2.5 py-2",
                          currentMilestone
                            ? "border-primary/50 bg-primary/5"
                            : reached
                              ? "border-emerald-500/25 bg-emerald-500/5"
                              : "bg-background",
                        ].join(" ")}
                      >
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-[10px] font-medium text-muted-foreground">
                            {rolloutOffsetByBatch[milestone.batch]}
                          </span>
                          {reached && (
                            <Check className="h-3 w-3 text-emerald-600" />
                          )}
                          {currentMilestone && (
                            <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                          )}
                        </div>
                        <p className="text-xs font-medium">
                          {milestone.kind === "complete"
                            ? t(
                                ($) =>
                                  $.fc_e2b_stable.rollout_schedule_complete,
                              )
                            : t(
                                ($) =>
                                  $.fc_e2b_stable.rollout_schedule_percentage,
                                { percentage: milestone.percentage },
                              )}
                        </p>
                        <time
                          dateTime={milestone.scheduled_at}
                          className="block text-[10px] leading-4 text-muted-foreground"
                        >
                          {formatRolloutTime(milestone.scheduled_at)}
                        </time>
                      </li>
                    );
                  })}
                </ol>
              </section>
            )}
            {active.status === "observing" && (
              <p className="rounded-md border border-primary/20 bg-primary/5 px-3 py-2 text-[11px] leading-4 text-muted-foreground">
                {t(($) => $.fc_e2b_stable.complete_observation_notice)}
              </p>
            )}
            {canRollback && (
              <p className="rounded-md border border-destructive/20 bg-destructive/5 px-3 py-2 text-[11px] leading-4 text-muted-foreground">
                {t(($) => $.fc_e2b_stable.rollback_notice, {
                  template: active.previous_template_alias,
                })}
              </p>
            )}
            <div className="flex flex-wrap justify-end gap-2">
              {active.status === "paused" ? (
                <Button
                  size="sm"
                  onClick={() => mutate("resume", active.id)}
                  disabled={resumeRelease.isPending}
                >
                  {t(($) => $.fc_e2b_stable.resume)}
                </Button>
              ) : (
                [
                  "validating",
                  "developer_rollout",
                  "rolling_out",
                  "observing",
                ].includes(active.status) && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => mutate("pause", active.id)}
                    disabled={pauseRelease.isPending}
                  >
                    {t(($) => $.fc_e2b_stable.pause)}
                  </Button>
                )
              )}
              {active.status === "awaiting_rollout" && (
                <Button
                  size="sm"
                  onClick={() => mutate("start-rollout", active.id)}
                  disabled={startRollout.isPending}
                >
                  {t(($) => $.fc_e2b_stable.start_rollout)}
                </Button>
              )}
              {nextRolloutMilestone && (
                <Button
                  size="sm"
                  onClick={() => mutate("advance-rollout", active.id)}
                  disabled={advanceRollout.isPending}
                >
                  <ChevronsRight className="mr-1.5 h-3.5 w-3.5" />
                  {t(($) => $.fc_e2b_stable.advance_rollout, {
                    percentage: nextRolloutMilestone.percentage,
                  })}
                </Button>
              )}
              {active.status === "observing" && (
                <Button
                  size="sm"
                  onClick={() => mutate("complete-observation", active.id)}
                  disabled={completeObservation.isPending}
                >
                  <Check className="mr-1.5 h-3.5 w-3.5" />
                  {t(($) => $.fc_e2b_stable.complete_observation)}
                </Button>
              )}
              {canRollback && (
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() => mutate("rollback", active.id)}
                  disabled={rollbackRelease.isPending}
                >
                  {t(($) => $.fc_e2b_stable.rollback)}
                </Button>
              )}
              {[
                "validating",
                "developer_rollout",
                "awaiting_rollout",
                "rolling_out",
                "observing",
                "paused",
              ].includes(active.status) && (
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() => mutate("terminate", active.id)}
                  disabled={terminateRelease.isPending}
                >
                  {t(($) => $.fc_e2b_stable.terminate)}
                </Button>
              )}
            </div>
          </div>
        ) : (
          <form id="fc-e2b-stable-release-form" onSubmit={submit} className="space-y-4">
            {bootstrap && (
              <p className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs">
                {t(($) => $.fc_e2b_stable.bootstrap_notice)}
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              {t(($) => $.fc_e2b_stable.validation_notice)}
            </p>
            <div className="space-y-1.5">
              <Label className="text-xs">{t(($) => $.fc_e2b_stable.template)}</Label>
              <div className="max-h-44 overflow-y-auto rounded-md border">
                {templatesQuery.isLoading && (
                  <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    {t(($) => $.fc_e2b_runtime.templates_loading)}
                  </div>
                )}
                {templatesQuery.isError && (
                  <div className="p-3 text-xs text-destructive">
                    {templatesQuery.error instanceof Error
                      ? templatesQuery.error.message
                      : t(($) => $.fc_e2b_runtime.templates_failed)}
                  </div>
                )}
                {!templatesQuery.isLoading &&
                  !templatesQuery.isError &&
                  templates.length === 0 && (
                    <div className="p-3 text-xs text-muted-foreground">
                      {t(($) => $.fc_e2b_runtime.templates_empty)}
                    </div>
                  )}
                {templates.map((template) => {
                  const isCurrent =
                    current != null &&
                    template.id === current.template_id &&
                    template.build_id === current.template_build_id;
                  const isSelected =
                    selected?.id === template.id &&
                    selected?.build_id === template.build_id;
                  const updatedAt = formatTemplateUpdatedAt(template.updated_at);
                  return (
                    <button
                      key={`${template.id}:${template.build_id}`}
                      type="button"
                      onClick={() => setSelected(template)}
                      className="flex w-full items-start justify-between gap-3 border-b p-3 text-left text-xs last:border-b-0 hover:bg-muted/50"
                    >
                      <span className="min-w-0 space-y-1">
                        <span className="block truncate font-medium">
                          {displayTemplate(template)}
                        </span>
                        <span className="block truncate text-muted-foreground">
                          {template.id} · {template.build_id}
                        </span>
                        {updatedAt && (
                          <span className="block truncate text-muted-foreground">
                            {t(($) => $.fc_e2b_runtime.template_updated, {
                              time: updatedAt,
                            })}
                          </span>
                        )}
                      </span>
                      <span className="flex shrink-0 items-center gap-1.5">
                        {isCurrent && (
                          <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground">
                            {t(($) => $.fc_e2b_stable.template_current)}
                          </span>
                        )}
                        {isSelected && <Check className="h-3.5 w-3.5" />}
                      </span>
                    </button>
                  );
                })}
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="stable-release-note" className="text-xs">
                {t(($) => $.fc_e2b_stable.note)}
              </Label>
              <Textarea
                id="stable-release-note"
                value={note}
                onChange={(event) => setNote(event.target.value)}
                maxLength={2000}
              />
            </div>
          </form>
        )}

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            {t(($) => $.fc_e2b_stable.close)}
          </Button>
          {!active && (
            <Button
              type="submit"
              form="fc-e2b-stable-release-form"
              disabled={
                createRelease.isPending ||
                !selected?.id ||
                !selected.build_id
              }
            >
              {createRelease.isPending && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              )}
              {bootstrap
                ? t(($) => $.fc_e2b_stable.initialize)
                : t(($) => $.fc_e2b_stable.publish_developers)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
