"use client";

import { useQuery } from "@tanstack/react-query";
import { dshHomeOptions, useEnsureDSHHome } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2 } from "lucide-react";
import { useT } from "../../../i18n";

export function DshHomeTab({ workspaceId, agentId }: {
  workspaceId: string;
  agentId: string;
}) {
  const { t } = useT("agents");
  const home = useQuery(dshHomeOptions(workspaceId, agentId));
  const prepare = useEnsureDSHHome(workspaceId, agentId);
  const status = home.data;
  const unavailable = home.isError || (!home.isPending && status == null);
  const ready = status?.provisioned === true && status.step === 6;
  const pending = status?.provisioned === false && status.state !== "unprovisioned";
  const busy = prepare.isPending;

  return (
    <section className="space-y-4">
      <p className="text-body leading-6 text-muted-foreground">
        {t(($) => $.tab_body.dsh_home.intro)}
      </p>
      <div className="rounded-lg border p-4" role="status" aria-live="polite">
        {home.isPending ? t(($) => $.tab_body.dsh_home.loading) :
          unavailable ? t(($) => $.tab_body.dsh_home.unavailable) :
          ready ? t(($) => $.tab_body.dsh_home.ready) :
          pending ? t(($) => $.tab_body.dsh_home.pending) :
          t(($) => $.tab_body.dsh_home.unprovisioned)}
        {ready && !unavailable && (
          <p className="mt-2 text-caption text-muted-foreground">
            {status?.state === "running" ? t(($) => $.tab_body.dsh_home.running) :
              status?.state === "retiring" ? t(($) => $.tab_body.dsh_home.retiring) :
              status?.state === "creating" ? t(($) => $.tab_body.dsh_home.starting) :
              t(($) => $.tab_body.dsh_home.offline)}
          </p>
        )}
      </div>
      {prepare.isError && (!ready || unavailable) && (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.tab_body.dsh_home.unconfirmed)}
        </p>
      )}
      <div className="flex gap-2">
        {!ready && (
          <Button size="sm" disabled={busy || home.isFetching || unavailable || !status}
            onClick={() => prepare.mutate()}>
            {busy && <Loader2 className="size-3.5 animate-spin" />}
            {busy ? t(($) => $.tab_body.dsh_home.preparing) :
              pending ? t(($) => $.tab_body.dsh_home.continue) :
              t(($) => $.tab_body.dsh_home.prepare)}
          </Button>
        )}
        <Button size="sm" variant="outline" disabled={busy || home.isFetching}
          onClick={() => { void home.refetch(); }}>
          {t(($) => $.tab_body.dsh_home.refresh)}
        </Button>
      </div>
    </section>
  );
}
