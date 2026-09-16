"use client";

import { useQuery } from "@tanstack/react-query";
import { dshProfileOptions, usePrepareDSHProfile, useRetryDSHProfileBuild } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

export function DshProfileStatus({ workspaceId, agentId }: { workspaceId: string; agentId: string }) {
  const { t } = useT("agents");
  const query = useQuery(dshProfileOptions(workspaceId, agentId));
  const prepare = usePrepareDSHProfile(workspaceId, agentId);
  const retry = useRetryDSHProfileBuild(workspaceId, agentId);
  const status = query.data;
  const unavailable = query.isError || (!query.isPending && !status);
  const description = () => {
    if (query.isPending) return t(($) => $.tab_body.dsh_profile.loading);
    if (unavailable) return t(($) => $.tab_body.dsh_profile.unavailable);
    switch (status?.state) {
      case "unprepared": return t(($) => $.tab_body.dsh_profile.unprepared);
      case "configuration_changed": return t(($) => $.tab_body.dsh_profile.changed);
      case "waiting_for_builds": return t(($) => $.tab_body.dsh_profile.building);
      case "build_failed": return t(($) => $.tab_body.dsh_profile.failed);
      case "apply_failed": return t(($) => $.tab_body.dsh_profile.apply_failed);
      case "pending_host": return t(($) => $.tab_body.dsh_profile.pending);
      case "applied": return status.current ? t(($) => $.tab_body.dsh_profile.applied) : t(($) => $.tab_body.dsh_profile.unavailable);
      default: return t(($) => $.tab_body.dsh_profile.unavailable);
    }
  };
  const buildLabel = (state: string) => {
    switch (state) {
      case "ready": return t(($) => $.tab_body.dsh_profile.build_ready);
      case "failed": return t(($) => $.tab_body.dsh_profile.build_failed);
      default: return t(($) => $.tab_body.dsh_profile.build_waiting);
    }
  };
  return <section className="space-y-3 rounded-lg border p-4">
    <h3 className="text-body font-medium">{t(($) => $.tab_body.dsh_profile.title)}</h3>
    <p role="status" aria-live="polite" className="text-caption text-muted-foreground">{description()}</p>
    {status && !unavailable && <>
      <dl className="grid grid-cols-2 gap-2 text-caption">
        <dt>{t(($) => $.tab_body.dsh_profile.desired)}</dt><dd>{status.desiredRevision || "—"}</dd>
        <dt>{t(($) => $.tab_body.dsh_profile.last_applied)}</dt><dd>{status.appliedRevision || "—"}</dd>
      </dl>
      {!!status.builds?.length && <ul className="space-y-1 text-caption">
        {(status.builds ?? []).map((build) => <li key={build.packageName} className="flex flex-wrap justify-between gap-2">
          <span>{build.packageName} · {build.version}</span><span>{buildLabel(build.state)}</span>
          {build.state === "failed" && <span className="w-full text-destructive">{
            build.errorCode === "source_archive_invalid" ? t(($) => $.tab_body.dsh_profile.source_invalid) :
            build.errorCode === "build_prerequisites_timeout" ? t(($) => $.tab_body.dsh_profile.prerequisites_failed) :
            t(($) => $.tab_body.dsh_profile.dependency_failed)
          }</span>}
          {status.state === "build_failed" && build.state === "failed" && build.canRetry === true && build.id && status.desiredRevision &&
            <Button size="sm" variant="outline" disabled={retry.isPending || prepare.isPending || query.isFetching}
              onClick={() => retry.mutate({ revision: status.desiredRevision, buildId: build.id })}>
              {t(($) => $.tab_body.dsh_profile.retry_build)}
            </Button>}
        </li>)}
      </ul>}
    </>}
    {prepare.isError && <p role="alert" className="text-caption text-destructive">{t(($) => $.tab_body.dsh_profile.prepare_unconfirmed)}</p>}
    {retry.isError && <p role="alert" className="text-caption text-destructive">{t(($) => $.tab_body.dsh_profile.retry_unconfirmed)}</p>}
    <div className="flex gap-2">
      {(status?.state === "unprepared" || status?.state === "configuration_changed" || status?.state === "apply_failed") && !unavailable &&
        <Button size="sm" onClick={() => prepare.mutate()} disabled={prepare.isPending || query.isFetching}>
          {status?.state === "apply_failed" ? t(($) => $.tab_body.dsh_profile.retry_apply) : t(($) => $.tab_body.dsh_profile.prepare)}
        </Button>}
      <Button size="sm" variant="outline" onClick={() => { void query.refetch(); }} disabled={prepare.isPending || query.isFetching}>
        {t(($) => $.tab_body.dsh_profile.refresh)}
      </Button>
    </div>
  </section>;
}
