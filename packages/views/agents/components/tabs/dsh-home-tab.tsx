"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { dshHomeOptions, useEnsureDSHHome, useDSHNativeEntry, type DSHNativeEntry } from "@multica/core/agents";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Loader2 } from "lucide-react";
import { useT } from "../../../i18n";

export function DshHomeTab({ workspaceId, agentId }: {
  workspaceId: string;
  agentId: string;
}) {
  const { t } = useT("agents");
  const home = useQuery(dshHomeOptions(workspaceId, agentId));
  const prepare = useEnsureDSHHome(workspaceId, agentId);
  const native = useDSHNativeEntry(workspaceId, agentId);
  const { reset } = native;
  const [nativeEntry, setNativeEntry] = useState<(DSHNativeEntry & { workspaceId: string; agentId: string }) | null>(null);
  const entry = nativeEntry?.workspaceId === workspaceId && nativeEntry?.agentId === agentId ? nativeEntry : undefined;
  useEffect(() => { reset(); setNativeEntry(null); }, [workspaceId, agentId, reset]);
  useEffect(() => {
    if (!nativeEntry) return;
    const timer = setTimeout(() => setNativeEntry(null), Math.max(0, Date.parse(nativeEntry.expiresAt) - Date.now()));
    return () => clearTimeout(timer);
  }, [nativeEntry]);
  const status = home.data;
  const unavailable = home.isError || (!home.isPending && status == null);
  const ready = status?.provisioned === true && status.step === 6;
  const pending = status?.provisioned === false && status.state !== "unprovisioned";
  const busy = prepare.isPending || native.isPending;

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
      {native.isError && (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.tab_body.dsh_home.native_unconfirmed)}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {ready && !unavailable && (
          entry && Date.parse(entry.expiresAt) > Date.now() ? (
            <a className={buttonVariants({ size: "sm" })} href={entry.entryUrl}
              target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer"
              onClick={() => { setTimeout(() => setNativeEntry(null), 0); }}
              onAuxClick={(event) => { if (event.button === 1) setTimeout(() => setNativeEntry(null), 0); }}>
              {t(($) => $.tab_body.dsh_home.native_enter)}
            </a>
          ) : (
            <Button size="sm" disabled={busy || home.isFetching}
              onClick={() => native.mutate({ onEntry: setNativeEntry })}>
              {native.isPending && <Loader2 className="size-3.5 animate-spin" />}
              {native.isPending ? t(($) => $.tab_body.dsh_home.native_preparing) :
                t(($) => $.tab_body.dsh_home.native_prepare)}
            </Button>
          )
        )}
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
