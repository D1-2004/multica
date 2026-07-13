"use client";

import { Bot, GitFork, Loader2, RefreshCw, Server } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  AgentSource,
  MemberWithUser,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { VisibilityBadge } from "./visibility-badge";
import { AgentPerformanceSummary } from "./tabs/activity-tab";

interface AgentOverviewSummaryProps {
  agent: Agent;
  runtime: AgentRuntime | null;
  owner: MemberWithUser | null;
  source?: AgentSource | null;
  canSyncSource?: boolean;
  sourceSyncing?: boolean;
  onSourceSync?: () => void;
}

/**
 * Read-only context for the workbench Overview. Editing lives under Settings;
 * keeping this surface non-interactive lets users scan identity, execution,
 * and capability context without mistaking every value for a control.
 */
export function AgentOverviewSummary({
  agent,
  runtime,
  owner,
  source = null,
  canSyncSource = false,
  sourceSyncing = false,
  onSourceSync,
}: AgentOverviewSummaryProps) {
  const { t } = useT("agents");
  const runtimeOnline = runtime?.status === "online";

  return (
    <aside className="self-start rounded-xl border border-surface-border bg-surface p-5 shadow-[var(--surface-shadow)] xl:sticky xl:top-6">
      <section>
        <h2 className="text-sm font-medium">
          {t(($) => $.overview.agent_context)}
        </h2>
        <dl className="mt-4 space-y-3 text-xs">
          {owner && (
            <SummaryRow label={t(($) => $.inspector.prop_owner)}>
              <span className="flex min-w-0 items-center gap-1.5">
                <ActorAvatar
                  actorType="member"
                  actorId={owner.user_id}
                  size="xs"
                />
                <span className="truncate text-foreground">{owner.name}</span>
              </span>
            </SummaryRow>
          )}
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
              <Server className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="truncate">
                {runtime?.name ?? t(($) => $.pickers.runtime_none)}
              </span>
            </span>
          </SummaryRow>
          <SummaryRow label={t(($) => $.inspector.prop_model)}>
            <span className="flex min-w-0 items-center gap-1.5 text-foreground">
              <Bot className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
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
          <h2 className="text-sm font-medium">
            {t(($) => $.inspector.section_skills)}
          </h2>
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {agent.skills.length}
          </span>
        </div>
        {agent.skills.length > 0 ? (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {agent.skills.map((skill) => (
              <span
                key={skill.id}
                className="max-w-full truncate rounded-md border border-surface-border bg-surface-hover px-2 py-1 text-xs text-muted-foreground"
              >
                {skill.name}
              </span>
            ))}
          </div>
        ) : (
          <p className="mt-3 text-xs text-muted-foreground">
            {t(($) => $.tab_body.skills.empty_title)}
          </p>
        )}
      </section>

      {source && (
        <section className="mt-5 border-t pt-5">
          <div className="flex items-center justify-between gap-3">
            <h2 className="flex items-center gap-1.5 text-sm font-medium">
              <GitFork className="size-3.5" aria-hidden="true" />
              {t(($) => $.overview.source_title)}
            </h2>
            <span
              className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${
                source.sync_status === "ready"
                  ? "bg-success/10 text-success"
                  : "bg-destructive/10 text-destructive"
              }`}
            >
              {t(($) => $.overview.source_status[source.sync_status === "ready" ? "ready" : source.sync_status === "disconnected" ? "disconnected" : "failed"])}
            </span>
          </div>
          <a
            href={`https://github.com/${source.repository}`}
            target="_blank"
            rel="noreferrer"
            className="mt-3 block truncate text-xs font-medium text-foreground underline-offset-4 hover:underline"
          >
            {source.repository}
          </a>
          <dl className="mt-2 space-y-2 text-xs">
            <SummaryRow label={t(($) => $.overview.source_ref)}>
              <span className="font-mono text-foreground">{source.ref}</span>
            </SummaryRow>
            <SummaryRow label={t(($) => $.overview.source_commit)}>
              <span className="font-mono text-foreground">{source.synced_commit_sha.slice(0, 12)}</span>
            </SummaryRow>
            {source.last_synced_at && (
              <SummaryRow label={t(($) => $.overview.source_synced_at)}>
                <span className="text-foreground">
                  {new Date(source.last_synced_at).toLocaleString()}
                </span>
              </SummaryRow>
            )}
          </dl>
          {source.last_sync_error && (
            <p className="mt-3 break-words text-xs leading-5 text-destructive">
              {source.last_sync_error}
            </p>
          )}
          {canSyncSource && onSourceSync && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="mt-3 w-full"
              disabled={sourceSyncing || !source.github_connected}
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
