"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Check, Loader2, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import {
  isReadyFCE2BTemplate,
  type FCE2BTemplate,
  useCreateFCE2BStableRelease,
  useFCE2BStableChannel,
  useFCE2BStableRuntimes,
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

export function StableFCE2BReleaseDialog({
  onClose,
}: {
  onClose: () => void;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const channelQuery = useFCE2BStableChannel();
  const stableRuntimesQuery = useFCE2BStableRuntimes(
    channelQuery.data?.can_publish === true,
  );
  const templatesQuery = useFCE2BTemplates(wsId);
  const createRelease = useCreateFCE2BStableRelease();
  const pauseRelease = useMutateFCE2BStableRelease("pause");
  const resumeRelease = useMutateFCE2BStableRelease("resume");
  const startRollout = useMutateFCE2BStableRelease("start-rollout");
  const terminateRelease = useMutateFCE2BStableRelease("terminate");
  const rollbackRelease = useMutateFCE2BStableRelease("rollback");
  const [selected, setSelected] = useState<FCE2BTemplate | null>(null);
  const [note, setNote] = useState("");
  const active = channelQuery.data?.active_release ?? null;
  const current = channelQuery.data?.current ?? null;
  const bootstrap = current == null;
  const templates = (templatesQuery.data ?? []).filter(isReadyFCE2BTemplate);
  const stableRuntimes = stableRuntimesQuery.data ?? [];
  const currentStableRuntimes = stableRuntimes.filter(
    (runtime) => runtime.matches_current_stable,
  ).length;
  const activeCandidateRuntimes = stableRuntimes.filter(
    (runtime) => runtime.matches_active_release,
  ).length;
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
              {!active.bootstrap &&
                active.updated_targets > 0 &&
                [
                  "developer_rollout",
                  "awaiting_rollout",
                  "rolling_out",
                  "observing",
                  "paused",
                  "failed",
                ].includes(active.status) && (
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

        <div className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Label className="text-xs">
              {t(($) => $.fc_e2b_stable.runtime_overview)}
            </Label>
            <span className="text-xs text-muted-foreground">
              {t(($) => $.fc_e2b_stable.runtime_summary, {
                current: currentStableRuntimes,
                active: activeCandidateRuntimes,
                total: stableRuntimes.length,
              })}
            </span>
          </div>
          <div className="max-h-52 overflow-y-auto rounded-md border">
            {stableRuntimesQuery.isLoading && (
              <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                {t(($) => $.fc_e2b_stable.runtime_loading)}
              </div>
            )}
            {stableRuntimesQuery.isError && (
              <div className="p-3 text-xs text-destructive">
                {stableRuntimesQuery.error instanceof Error
                  ? stableRuntimesQuery.error.message
                  : t(($) => $.fc_e2b_stable.runtime_failed)}
              </div>
            )}
            {!stableRuntimesQuery.isLoading &&
              !stableRuntimesQuery.isError &&
              stableRuntimes.length === 0 && (
                <div className="p-3 text-xs text-muted-foreground">
                  {t(($) => $.fc_e2b_stable.runtime_empty)}
                </div>
              )}
            {stableRuntimes.map((runtime) => (
              <div
                key={runtime.runtime_id}
                className="space-y-1 border-b p-3 text-xs last:border-b-0"
              >
                <div className="flex items-center justify-between gap-3">
                  <span className="min-w-0 truncate font-medium">
                    {runtime.workspace_name} / {runtime.runtime_name}
                  </span>
                  <span
                    className={
                      runtime.status === "online"
                        ? "shrink-0 text-emerald-600"
                        : "shrink-0 text-muted-foreground"
                    }
                  >
                    {runtime.status === "online"
                      ? t(($) => $.fc_e2b_stable.runtime_online)
                      : t(($) => $.fc_e2b_stable.runtime_offline)}
                  </span>
                </div>
                <p
                  className="truncate text-muted-foreground"
                  title={`${runtime.template_alias} · ${runtime.template_id} · ${runtime.template_build_id}`}
                >
                  {runtime.provider} · {runtime.template_alias || "-"} ·{" "}
                  {runtime.template_build_id || "-"}
                </p>
                <div className="flex flex-wrap gap-1.5">
                  {runtime.matches_current_stable && (
                    <span className="rounded bg-primary/10 px-2 py-0.5 text-primary">
                      {t(($) => $.fc_e2b_stable.runtime_current)}
                    </span>
                  )}
                  {runtime.matches_active_release && (
                    <span className="rounded bg-amber-500/10 px-2 py-0.5 text-amber-700 dark:text-amber-400">
                      {t(($) => $.fc_e2b_stable.runtime_active)}
                    </span>
                  )}
                  {!runtime.matches_current_stable &&
                    !runtime.matches_active_release && (
                      <span className="rounded bg-muted px-2 py-0.5 text-muted-foreground">
                        {runtime.template_channel === "candidate"
                          ? t(($) => $.fc_e2b_stable.runtime_candidate)
                          : t(($) => $.fc_e2b_stable.runtime_outdated)}
                      </span>
                    )}
                </div>
              </div>
            ))}
          </div>
        </div>

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
