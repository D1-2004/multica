"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { dshHomeOptions, useEnsureDSHHome } from "@multica/core/agents";
import {
  filesystemEntriesOptions,
  filesystemGrantsOptions,
  useFilesystemGrant,
} from "@multica/core/filesystem";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { File, Folder, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { AppLink } from "../../../navigation";
import { useT } from "../../../i18n";

export function DshHomeTab({ workspaceId, agentId, canEdit = false }: {
  workspaceId: string;
  agentId: string;
  canEdit?: boolean;
}) {
  const { t } = useT("agents");
  const [phase, setPhase] = useState<"idle" | "preparing" | "error">("idle");
  const busy = phase === "preparing";
  const identity = `${workspaceId}/${agentId}`;
  const active = useRef(identity);
  const homeOptions = dshHomeOptions(workspaceId, agentId);
  const home = useQuery({ ...homeOptions, refetchInterval: busy ? 2000 : homeOptions.refetchInterval });
  const prepare = useEnsureDSHHome(workspaceId, agentId);
  useEffect(() => {
    active.current = identity; setPhase("idle");
    return () => { active.current = ""; };
  }, [identity]);
  useEffect(() => {
    if (!busy) return;
    const timer = setTimeout(() => { setPhase("error"); }, 10 * 60 * 1000);
    return () => clearTimeout(timer);
  }, [busy]);
  const start = async () => {
    if (busy) return;
    setPhase("preparing");
    try {
      let storage = home.data;
      const deadline = Date.now() + 10 * 60 * 1000;
      while (!storage?.provisioned && active.current === identity) {
        if (Date.now() >= deadline) throw new Error("Filesystem preparation timed out");
        try { storage = await prepare.mutateAsync(); }
        catch {
          storage = (await home.refetch()).data;
          if (!storage?.provisioned) throw new Error("Filesystem preparation was not confirmed");
        }
        if (!storage?.provisioned) await new Promise((resolve) => setTimeout(resolve, 2000));
      }
      if (active.current !== identity) return;
      setPhase("idle");
    } catch { if (active.current === identity) setPhase("error"); }
  };
  const status = home.data;
  const unavailable = home.isError || (!home.isPending && status == null);
  const ready = status?.provisioned === true && status.step === 6;
  const progress = t(($) => $.tab_body.dsh_home.preparing);
  return <section className="space-y-8">
    <SharedDiskPanel workspaceId={workspaceId} agentId={agentId} canEdit={canEdit} />
    <div className="space-y-4">
    <h2 className="text-title font-medium">{t(($) => $.tab_body.dsh_home.private_title)}</h2>
    <p className="text-body leading-6 text-muted-foreground">{t(($) => $.tab_body.dsh_home.intro)}</p>
    <p role="status" aria-live="polite" className="text-caption text-muted-foreground">
      {busy ? progress : home.isPending ? t(($) => $.tab_body.dsh_home.loading) :
        unavailable ? t(($) => $.tab_body.dsh_home.unavailable) :
        ready ? t(($) => $.tab_body.dsh_home.ready) : t(($) => $.tab_body.dsh_home.unprovisioned)}
    </p>
    {phase === "error" && <p role="alert" className="text-caption text-destructive">{t(($) => $.tab_body.dsh_home.unconfirmed)}</p>}
    {!ready && <Button size="sm" disabled={busy || home.isPending} aria-busy={busy} onClick={() => { void start(); }}>
      {busy && <Loader2 className="size-3.5 animate-spin" />}
      {busy ? progress : t(($) => $.tab_body.dsh_home.prepare)}
    </Button>}
    </div>
  </section>;
}

function SharedDiskPanel({
  workspaceId,
  agentId,
  canEdit,
}: {
  workspaceId: string;
  agentId: string;
  canEdit: boolean;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const listing = useQuery(filesystemEntriesOptions(workspaceId, "shared", ".", 0, true));
  const grants = useQuery(filesystemGrantsOptions(workspaceId));
  const saveGrant = useFilesystemGrant(workspaceId);
  const access =
    grants.data?.grants.find((grant) => grant.agent_id === agentId)?.access ?? "none";
  const entries = listing.data?.entries ?? [];
  const grantItems = [
    { value: "none", label: t(($) => $.tab_body.dsh_home.grant_none) },
    { value: "read", label: t(($) => $.tab_body.dsh_home.grant_read) },
    { value: "write", label: t(($) => $.tab_body.dsh_home.grant_write) },
  ];
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <h2 className="text-title font-medium">{t(($) => $.tab_body.dsh_home.shared_title)}</h2>
          <p className="text-body leading-6 text-muted-foreground">
            {t(($) => $.tab_body.dsh_home.shared_intro)}
          </p>
        </div>
        <AppLink
          href={paths.files()}
          className="shrink-0 text-caption text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          {t(($) => $.tab_body.dsh_home.open_files)}
        </AppLink>
      </div>
      {canEdit && !grants.isError ? (
        <div className="flex max-w-xs flex-col gap-1.5">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.tab_body.dsh_home.grant_label)}
          </p>
          <Select
            items={grantItems}
            value={access}
            onValueChange={(value) => {
              if (!value) return;
              void saveGrant
                .mutateAsync({ agent_id: agentId, access: value })
                .then(() => toast.success(t(($) => $.tab_body.dsh_home.grant_saved)))
                .catch(() => toast.error(t(($) => $.tab_body.dsh_home.grant_failed)));
            }}
          >
            <SelectTrigger className="h-8" aria-label={t(($) => $.tab_body.dsh_home.grant_label)}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {grantItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      ) : null}
      <p className="text-caption text-muted-foreground">
        {t(($) => $.tab_body.dsh_home.access_path)}
      </p>
      {entries.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.dsh_home.shared_empty)}</p>
      ) : (
        <ul className="divide-y divide-border rounded-md border">
          {entries.map((entry) => (
            <li key={entry.path} className="flex items-center gap-2 px-3 py-2 text-body">
              {entry.is_dir ? (
                <Folder aria-hidden="true" className="size-3.5 text-muted-foreground" />
              ) : (
                <File aria-hidden="true" className="size-3.5 text-muted-foreground" />
              )}
              <span className="min-w-0 truncate">{entry.path || entry.name}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
