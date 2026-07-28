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
  useFCE2BTemplates,
  useMutateFCE2BStableRelease,
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
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

function displayTemplate(template: FCE2BTemplate): string {
  return template.name || template.template || template.id || "";
}

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
  const rollbackRelease = useMutateFCE2BStableRelease("rollback");
  const templates = (templatesQuery.data ?? []).filter(isReadyFCE2BTemplate);
  const [selected, setSelected] = useState<FCE2BTemplate | null>(null);
  const [gitCommit, setGitCommit] = useState("");
  const [acrDigest, setACRDigest] = useState("");
  const [note, setNote] = useState("");
  const active = channelQuery.data?.active_release ?? null;
  const bootstrap = channelQuery.data?.current == null;

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selected?.id || !selected.build_id) return;
    try {
      await createRelease.mutateAsync({
        idempotencyKey: crypto.randomUUID(),
        data: {
          template_id: selected.id,
          expected_build_id: selected.build_id,
          git_commit: gitCommit.trim().toLowerCase(),
          acr_digest: acrDigest.trim().toLowerCase(),
          note: note.trim(),
          bootstrap,
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
    action: "pause" | "resume" | "rollback",
    releaseId: string,
  ) => {
    const mutation =
      action === "pause"
        ? pauseRelease
        : action === "resume"
          ? resumeRelease
          : rollbackRelease;
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
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <ShieldCheck className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_stable.title)}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {t(($) => $.fc_e2b_stable.description)}
          </DialogDescription>
        </DialogHeader>

        {active ? (
          <div className="space-y-4">
            <div className="space-y-2 rounded-md border p-3 text-xs">
              <div className="flex items-center justify-between gap-3">
                <span className="font-medium">{active.template_alias}</span>
                <span className="rounded bg-muted px-2 py-0.5">{active.status}</span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full bg-primary transition-[width]"
                  style={{ width: `${active.target_percentage}%` }}
                />
              </div>
              <div className="flex justify-between text-muted-foreground">
                <span>
                  {t(($) => $.fc_e2b_stable.progress, {
                    updated: active.updated_targets,
                    total: active.total_targets,
                  })}
                </span>
                <span>{active.target_percentage}%</span>
              </div>
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
                ["validating", "rolling_out", "observing"].includes(
                  active.status,
                ) && (
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
              {!active.bootstrap &&
                ["rolling_out", "observing", "paused", "failed"].includes(
                  active.status,
                ) && (
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => mutate("rollback", active.id)}
                    disabled={rollbackRelease.isPending}
                  >
                    {t(($) => $.fc_e2b_stable.rollback)}
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
            <div className="space-y-1.5">
              <Label className="text-xs">{t(($) => $.fc_e2b_stable.template)}</Label>
              <div className="max-h-44 overflow-y-auto rounded-md border">
                {templatesQuery.isLoading && (
                  <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    {t(($) => $.fc_e2b_runtime.templates_loading)}
                  </div>
                )}
                {templates.map((template) => {
                  const isSelected =
                    selected?.id === template.id &&
                    selected?.build_id === template.build_id;
                  return (
                    <button
                      key={`${template.id}:${template.build_id}`}
                      type="button"
                      onClick={() => setSelected(template)}
                      className="flex w-full items-start justify-between gap-3 border-b p-3 text-left text-xs last:border-b-0 hover:bg-muted/50"
                    >
                      <span className="min-w-0">
                        <span className="block truncate font-medium">
                          {displayTemplate(template)}
                        </span>
                        <span className="block truncate text-muted-foreground">
                          {template.id} · {template.build_id}
                        </span>
                      </span>
                      {isSelected && <Check className="h-3.5 w-3.5 shrink-0" />}
                    </button>
                  );
                })}
              </div>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="stable-git-commit" className="text-xs">
                {t(($) => $.fc_e2b_stable.git_commit)}
              </Label>
              <Input
                id="stable-git-commit"
                value={gitCommit}
                onChange={(event) => setGitCommit(event.target.value)}
                placeholder="40-character Git SHA"
                pattern="[0-9a-fA-F]{40}"
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="stable-acr-digest" className="text-xs">
                {t(($) => $.fc_e2b_stable.acr_digest)}
              </Label>
              <Input
                id="stable-acr-digest"
                value={acrDigest}
                onChange={(event) => setACRDigest(event.target.value)}
                placeholder="sha256:..."
                pattern="sha256:[0-9a-fA-F]{64}"
                required
              />
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
                !selected.build_id ||
                gitCommit.trim().length !== 40 ||
                !/^sha256:[0-9a-fA-F]{64}$/.test(acrDigest.trim())
              }
            >
              {createRelease.isPending && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              )}
              {bootstrap
                ? t(($) => $.fc_e2b_stable.initialize)
                : t(($) => $.fc_e2b_stable.publish)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
