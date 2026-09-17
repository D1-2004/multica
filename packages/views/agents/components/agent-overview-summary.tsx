"use client";

import { useMemo, useState } from "react";
import { Bot, GitFork, Loader2, RefreshCw, Server, Upload } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  AgentSource,
  MemberWithUser,
} from "@multica/core/types";
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
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { VisibilityBadge } from "./visibility-badge";
import { AgentPerformanceSummary } from "./tabs/activity-tab";

interface AgentOverviewSummaryProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  owner: MemberWithUser | null;
  members?: MemberWithUser[];
  canTransferOwner?: boolean;
  source?: AgentSource | null;
  canSyncSource?: boolean;
  sourceSyncing?: boolean;
  onSourceSync?: () => void;
  onTransferOwner?: (userId: string) => Promise<void>;
}

/**
 * Context for the workbench Overview. Most rows stay read-only so users can
 * scan identity and execution without treating every value as a control.
 * The owner row is interactive when the caller may transfer ownership.
 */
export function AgentOverviewSummary({
  agent,
  runtime,
  owner,
  members = [],
  canTransferOwner = false,
  source = null,
  canSyncSource = false,
  sourceSyncing = false,
  onSourceSync,
  onTransferOwner,
}: AgentOverviewSummaryProps) {
  const { t } = useT("agents");
  const runtimeOnline = runtime?.status === "online";
  const [ownerOpen, setOwnerOpen] = useState(false);
  const [ownerFilter, setOwnerFilter] = useState("");
  const [pendingOwner, setPendingOwner] = useState<MemberWithUser | null>(null);
  const [transferring, setTransferring] = useState(false);

  const filteredMembers = useMemo(() => {
    const q = ownerFilter.trim().toLowerCase();
    if (!q) return members;
    return members.filter((m) => m.name.toLowerCase().includes(q));
  }, [members, ownerFilter]);

  const ownerLabel = owner
    ? owner.name
    : agent.owner_id
      ? t(($) => $.detail.owner_left)
      : t(($) => $.detail.owner_none);

  const pickOwner = (member: MemberWithUser) => {
    setOwnerOpen(false);
    setOwnerFilter("");
    if (member.user_id === agent.owner_id) return;
    setPendingOwner(member);
  };

  const confirmTransfer = async () => {
    if (!pendingOwner || !onTransferOwner) return;
    setTransferring(true);
    try {
      await onTransferOwner(pendingOwner.user_id);
      setPendingOwner(null);
    } finally {
      setTransferring(false);
    }
  };

  const ownerValue = (
    <span className="flex min-w-0 items-center gap-1.5">
      {owner ? (
        <ActorAvatar actorType="member" actorId={owner.user_id} size="xs" />
      ) : null}
      <span
        className={
          owner ? "truncate text-foreground" : "truncate text-muted-foreground"
        }
      >
        {ownerLabel}
      </span>
    </span>
  );

  return (
    <aside className="self-start rounded-xl border border-surface-border bg-surface p-5 shadow-[var(--surface-shadow)] xl:sticky xl:top-6">
      <section>
        <h2 className="text-body font-medium">
          {t(($) => $.overview.agent_context)}
        </h2>
        <dl className="mt-4 space-y-3 text-caption">
          <SummaryRow label={t(($) => $.inspector.prop_owner)}>
            {canTransferOwner && onTransferOwner ? (
              <Popover
                open={ownerOpen}
                onOpenChange={(open) => {
                  setOwnerOpen(open);
                  if (!open) setOwnerFilter("");
                }}
              >
                <PopoverTrigger
                  render={
                    <button
                      type="button"
                      className="inline-flex min-w-0 items-center text-left hover:text-foreground"
                    >
                      {ownerValue}
                    </button>
                  }
                />
                <PopoverContent align="start" className="w-52 p-0">
                  <div className="border-b px-2 py-1.5">
                    <input
                      type="text"
                      value={ownerFilter}
                      onChange={(e) => setOwnerFilter(e.target.value)}
                      placeholder={t(($) => $.detail.owner_search_placeholder)}
                      className="w-full bg-transparent text-body outline-none placeholder:text-muted-foreground"
                    />
                  </div>
                  <div className="max-h-60 overflow-y-auto p-1">
                    {filteredMembers.map((m) => (
                      <button
                        type="button"
                        key={m.user_id}
                        onClick={() => pickOwner(m)}
                        className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body transition-colors hover:bg-accent"
                      >
                        <ActorAvatar
                          actorType="member"
                          actorId={m.user_id}
                          size="sm"
                        />
                        <span className="truncate">{m.name}</span>
                      </button>
                    ))}
                    {filteredMembers.length === 0 && (
                      <div className="px-2 py-3 text-center text-body text-muted-foreground">
                        {t(($) => $.detail.owner_no_results)}
                      </div>
                    )}
                  </div>
                </PopoverContent>
              </Popover>
            ) : (
              ownerValue
            )}
          </SummaryRow>
          <SummaryRow label={t(($) => $.overview.access)}>
            <VisibilityBadge value={agent.visibility} />
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_runtime)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <span
                className={`h-1.5 w-1.5 shrink-0 rounded-full ${
                  runtimeOnline ? "bg-success" : "bg-muted-foreground/40"
                }`}
                aria-hidden="true"
              />
              <Server
                className="h-3 w-3 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span className="truncate">
                {runtime
                  ? runtimeDisplayLabel(runtime)
                  : t(($) => $.pickers.runtime_none)}
              </span>
            </span>
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_model)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <Bot
                className="h-3 w-3 shrink-0 text-muted-foreground"
                aria-hidden="true"
              />
              <span className="truncate">
                {agent.model || t(($) => $.pickers.model_default)}
              </span>
            </span>
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_concurrency)}>
            <span className="font-mono tabular-nums text-foreground">
              {agent.max_concurrent_tasks}
            </span>
          </SummaryRow>
        </dl>
      </section>

      <section className="mt-5 border-t pt-5">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-body font-medium">
            {t(($) => $.inspector.section_skills)}
          </h2>
          <span className="font-mono text-caption tabular-nums text-muted-foreground">
            {agent.skills.length}
          </span>
        </div>
        {agent.skills.length > 0 ? (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {agent.skills.map((skill) => (
              <span
                key={skill.id}
                className="max-w-full truncate rounded-md border border-surface-border bg-surface-hover px-2 py-1 text-caption text-muted-foreground"
              >
                {skill.name}
              </span>
            ))}
          </div>
        ) : (
          <p className="mt-3 text-caption text-muted-foreground">
            {t(($) => $.tab_body.skills.empty_title)}
          </p>
        )}
      </section>

      {source && (
        <section className="mt-5 border-t pt-5">
          <div className="flex items-center justify-between gap-3">
            <h2 className="flex items-center gap-1.5 text-body font-medium">
              {source.source_type === "local" ? <Upload className="size-3.5" aria-hidden="true" /> : <GitFork className="size-3.5" aria-hidden="true" />}
              {source.source_type === "local" ? t(($) => $.creation_studio.modes.local.title) : t(($) => $.overview.source_title)}
            </h2>
            <span
              className={`rounded-full px-2 py-0.5 text-micro font-medium ${
                source.sync_status === "ready"
                  ? "bg-success/10 text-success"
                  : "bg-destructive/10 text-destructive"
              }`}
            >
              {t(
                ($) =>
                  $.overview.source_status[
                    source.sync_status === "ready"
                      ? "ready"
                      : source.sync_status === "disconnected"
                        ? "disconnected"
                        : "failed"
                  ],
              )}
            </span>
          </div>
          {source.source_type === "git" && <a
            href={source.repository_url ?? ""}
            target="_blank"
            rel="noreferrer"
            className="mt-3 block truncate text-caption font-medium text-foreground underline-offset-4 hover:underline"
          >
            {source.repository}
          </a>}
          <dl className="mt-2 space-y-2 text-caption">
            {source.source_type === "git" && <SummaryRow label={t(($) => $.overview.source_ref)}>
              <span className="font-mono text-foreground">{source.ref}</span>
            </SummaryRow>}
            <SummaryRow label={source.source_type === "local" ? t(($) => $.overview.package_hash) : t(($) => $.overview.source_commit)}>
              <span className="font-mono text-foreground">
                {source.synced_commit_sha.slice(0, 12)}
              </span>
            </SummaryRow>
            {source.last_synced_at && (
              <SummaryRow label={source.source_type === "local" ? t(($) => $.overview.package_imported_at) : t(($) => $.overview.source_synced_at)}>
                <span className="text-foreground">
                  {new Date(source.last_synced_at).toLocaleString()}
                </span>
              </SummaryRow>
            )}
          </dl>
          {source.last_sync_error && (
            <p className="mt-3 break-words text-caption leading-5 text-destructive">
              {source.last_sync_error}
            </p>
          )}
          {source.source_type === "git" && canSyncSource && onSourceSync && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="mt-3 w-full"
              disabled={sourceSyncing || !source.connected}
              onClick={onSourceSync}
            >
              {sourceSyncing ? (
                <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              ) : (
                <RefreshCw className="size-3.5" aria-hidden="true" />
              )}
              {t(($) => $.overview.source_sync)}
            </Button>
          )}
        </section>
      )}

      <AgentPerformanceSummary agent={agent} />

      <Dialog
        open={pendingOwner !== null}
        onOpenChange={(open) => {
          if (!open && !transferring) setPendingOwner(null);
        }}
      >
        <DialogContent className="max-w-sm" showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="text-body font-semibold">
              {t(($) => $.detail.transfer_owner_title)}
            </DialogTitle>
            <DialogDescription className="text-caption">
              {t(($) => $.detail.transfer_owner_description, {
                name: agent.name,
                owner: pendingOwner?.name ?? "",
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              disabled={transferring}
              onClick={() => setPendingOwner(null)}
            >
              {t(($) => $.detail.transfer_owner_cancel)}
            </Button>
            <Button size="sm" disabled={transferring} onClick={() => void confirmTransfer()}>
              {t(($) => $.detail.transfer_owner_confirm)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </aside>
  );
}

function SummaryRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[88px_minmax(0,1fr)] items-center gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
