"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { AgentSource, AgentSourceFileChange } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { AppLink } from "../../../navigation";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentSourceBranchesOptions, usePreviewAgentSourceSync, useSyncAgentSource } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { PackageRequirementsForm } from "../../create/package-requirements-form";
import { useT } from "../../../i18n";

export function PublishTab({ source, canEdit }: { source: AgentSource | null; canEdit: boolean }) {
  return source?.source_type === "github" ? <GitPublishTab source={source} canEdit={canEdit} /> : <PublishEmptyState />;
}

function PublishEmptyState() {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  return <div className="space-y-4"><p className="text-body text-muted-foreground">{t(($) => $.tab_body.publish.no_source)}</p><AppLink className="text-body underline" href={paths.newAgentGit()}>{t(($) => $.creation_studio.modes.git.title)}</AppLink></div>;
}

function GitPublishTab({ source, canEdit }: { source: AgentSource; canEdit: boolean }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const [ref, setRef] = useState(source.ref);
  const canSync = canEdit && source.github_connected === true && source.can_sync !== false;
  const branches = useQuery({ ...agentSourceBranchesOptions(workspaceId, source.agent_id), enabled: canSync });
  const previewMutation = usePreviewAgentSourceSync(source.agent_id);
  const syncMutation = useSyncAgentSource(workspaceId, source.agent_id);
  const preview = previewMutation.data;
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const requirementsReady = (preview?.requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!preview?.requirements?.deferred_bindings.length || deferBindings);
  const pending = previewMutation.isPending || syncMutation.isPending;

  const handlePreview = async () => {
    try {
      setSecrets({}); setDeferBindings(false);
      await previewMutation.mutateAsync(ref.trim());
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.publish.preview_failed));
    }
  };

  const handleConfirm = async () => {
    if (!preview?.preview_id || pending || !canSync || !requirementsReady) return;
    try {
      const result = await syncMutation.mutateAsync({ previewId: preview.preview_id, secrets, deferredBindings: deferBindings ? preview.requirements?.deferred_bindings : [] });
      setSecrets({});
      toast.success(t(($) => $.detail.source_sync_succeeded));
      result.warnings?.forEach((warning) => toast.warning(warning));
      previewMutation.reset();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.detail.source_sync_failed));
      previewMutation.reset();
    }
  };

  return (
    <div className="space-y-6">
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.description)}</p>
      <dl className="grid gap-3 text-caption">
        <div className="space-y-1">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.publish.repository)}</dt>
          <dd className="break-all"><a className="underline underline-offset-4" href={`https://github.com/${source.repository}`} target="_blank" rel="noreferrer">{`https://github.com/${source.repository}`}</a></dd>
        </div>
        <div className="space-y-1">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.publish.deployed)}</dt>
          <dd className="break-all font-mono">{source.ref} · {source.synced_commit_sha}</dd>
        </div>
      </dl>
      {source.last_sync_error && <p role="alert" className="text-caption text-destructive">{source.last_sync_error}</p>}
      {!canSync && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.unavailable)}</p>}
      {canSync && (
        <div className="space-y-3">
          <Label htmlFor="agent-source-ref">{t(($) => $.tab_body.publish.branch)}</Label>
          <div className="flex flex-wrap items-center gap-2">
            <Input id="agent-source-ref" list="agent-source-branches" value={ref} disabled={pending} className="min-w-0 flex-1"
              onChange={(event) => { setRef(event.target.value); previewMutation.reset(); }} />
            <datalist id="agent-source-branches">
              {branches.data?.branches?.map((branch) => <option key={branch.name} value={branch.name} />)}
            </datalist>
            <Button variant="outline" disabled={pending || !ref.trim()} onClick={handlePreview}>
              {previewMutation.isPending ? t(($) => $.tab_body.publish.loading) : t(($) => $.tab_body.publish.preview)}
            </Button>
          </div>
          {branches.isError && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.branches_failed)}</p>}
        </div>
      )}
      {preview && (
        <div className="space-y-4">
          <p className="break-all font-mono text-caption">{preview.base_sha.slice(0, 12)} → {preview.resolved_sha.slice(0, 12)} · {preview.ref}</p>
          {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
          <PackageRequirementsForm requirements={preview.requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={pending} />
          <ChangeList title={t(($) => $.tab_body.publish.git_changes)} changes={preview.git_changes ?? []} />
          <ChangeList title={t(($) => $.tab_body.publish.configuration_changes)} changes={preview.configuration_changes ?? []} />
          {preview.changed === true ? (
            <div className="space-y-3">
              <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.confirm_hint)}</p>
              <Button disabled={pending || !canSync || !requirementsReady} onClick={handleConfirm}>
                {syncMutation.isPending ? t(($) => $.tab_body.publish.publishing) : t(($) => $.tab_body.publish.confirm)}
              </Button>
            </div>
          ) : <p className="text-caption text-muted-foreground">{t(($) => $.detail.source_already_current)}</p>}
        </div>
      )}
    </div>
  );
}

function ChangeList({ title, changes }: { title: string; changes: AgentSourceFileChange[] }) {
  const { t } = useT("agents");
  return (
    <div className="space-y-2">
      <h3 className="text-body font-medium">{title} ({changes.length})</h3>
      {changes.map((change) => (
        <details key={change.path} className="rounded-md border">
          <summary className="cursor-pointer break-all px-3 py-2 font-mono text-caption">{change.status} · {change.path}</summary>
          {change.before_mode !== change.after_mode && <p className="px-3 text-caption text-muted-foreground">{change.before_mode || "—"} → {change.after_mode || "—"}</p>}
          <div className="grid min-w-0 gap-3 p-3 md:grid-cols-2">
            <div className="min-w-0 space-y-1"><p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.before)}</p><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-caption">{change.before ?? "—"}</pre></div>
            <div className="min-w-0 space-y-1"><p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.after)}</p><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-caption">{change.after ?? "—"}</pre></div>
          </div>
        </details>
      ))}
    </div>
  );
}
