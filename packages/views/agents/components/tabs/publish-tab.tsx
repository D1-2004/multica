"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { AgentSource } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentSourceBranchesOptions, usePreviewAgentSourceSync, usePreviewAgentPublicationRollback, useSyncAgentSource } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { PackageRequirementsForm } from "../../create/package-requirements-form";
import { useT } from "../../../i18n";
import { PackageError } from "../../create/package-error";
import { ZIPPublishTab } from "./zip-publish-tab";
import { SourceChangesDialog } from "./source-change-list";
import { GitRevisionSelect } from "../git-revision-select";
import { AgentPublicationHistory } from "./agent-publication-history";

export function PublishTab({ agentId, source, canEdit }: { agentId: string; source: AgentSource | null; canEdit: boolean }) {
  return source?.source_type === "github" ? <GitPublishTab source={source} canEdit={canEdit} /> : <ZIPPublishTab agentId={agentId} canEdit={canEdit} />;
}

function GitPublishTab({ source, canEdit }: { source: AgentSource; canEdit: boolean }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const [ref, setRef] = useState(source.ref);
  const [revisionReady, setRevisionReady] = useState(!!source.ref);
  const canSync = canEdit && source.github_connected === true && source.can_sync !== false;
  const branches = useQuery({ ...agentSourceBranchesOptions(workspaceId, source.agent_id), enabled: canSync });
  const previewMutation = usePreviewAgentSourceSync(source.agent_id);
  const rollbackMutation = usePreviewAgentPublicationRollback(source.agent_id);
  const syncMutation = useSyncAgentSource(workspaceId, source.agent_id);
  const preview = rollbackMutation.data ?? previewMutation.data;
  const [previewOpen, setPreviewOpen] = useState(false);
  const [pluginBindings, setPluginBindings] = useState<Record<string,string>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [deferBindings, setDeferBindings] = useState(false);
  const requirementsReady = (preview?.requirements?.dshPlugins ?? []).every((plugin) => !!pluginBindings[plugin.ref]) && (preview?.requirements?.secrets ?? []).every((ref) => Object.hasOwn(secrets, ref)) && (!preview?.requirements?.deferred_bindings.length || deferBindings);
  const pending = previewMutation.isPending || rollbackMutation.isPending || syncMutation.isPending;

  const resetPreview = () => { previewMutation.reset(); rollbackMutation.reset(); syncMutation.reset(); setSecrets({}); setPluginBindings({}); setDeferBindings(false); setPreviewOpen(false); };
  const handleRestore = async (publicationId: string) => {
    if (pending || !canSync) return;
    resetPreview();
    try { await rollbackMutation.mutateAsync(publicationId); setPreviewOpen(true); } catch { /* Retain the complete error for review. */ }
  };

  const handlePreview = async () => {
    try {
      resetPreview();
      await previewMutation.mutateAsync(ref.trim());
      setPreviewOpen(true);
    } catch { /* The persistent error panel retains the complete response. */ }
  };

  const handleConfirm = async () => {
    if (!preview?.preview_id || pending || !canSync || !requirementsReady) return;
    try {
      const result = await syncMutation.mutateAsync({ previewId: preview.preview_id, secrets, dshPluginBindings: pluginBindings, deferredBindings: deferBindings ? preview.requirements?.deferred_bindings : [] });
      setSecrets({}); setPluginBindings({});
      toast.success(t(($) => $.detail.source_sync_succeeded));
      result.warnings?.forEach((warning) => toast.warning(warning));
      previewMutation.reset();
      rollbackMutation.reset();
    } catch {
      previewMutation.reset();
      rollbackMutation.reset();
    }
  };

  return (
    <div className="space-y-6">
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.description)}</p>
      <dl className="grid gap-3 text-caption">
        <div className="space-y-1">
          <dt className="text-muted-foreground">{t(($) => $.tab_body.publish.repository)}</dt>
          <dd className="break-all"><a className="underline underline-offset-4" href={source.repository_url || `https://github.com/${source.repository}`} target="_blank" rel="noreferrer">{source.repository_url || `https://github.com/${source.repository}`}</a></dd>
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
          <GitRevisionSelect id="agent-source-ref" value={ref} disabled={pending} revisions={branches.data} onReadyChange={setRevisionReady} onChange={(value) => { setRef(value); resetPreview(); }} />
          <div>
            <Button variant="outline" disabled={pending || !ref.trim() || !revisionReady} onClick={handlePreview}>
              {previewMutation.isPending ? t(($) => $.tab_body.publish.loading) : t(($) => $.tab_body.publish.preview)}
            </Button>
          </div>
          {branches.isError && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.branches_failed)}</p>}
        </div>
      )}
      <PackageError error={rollbackMutation.error ?? previewMutation.error ?? syncMutation.error} />
      {preview && (
        <div className="space-y-4">
          <p className="break-all font-mono text-caption">{preview.base_sha.slice(0, 12)} → {preview.resolved_sha.slice(0, 12)} · {preview.ref}</p>
          {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
          <PackageRequirementsForm workspaceId={workspaceId} pluginBindings={pluginBindings} onPluginBindingsChange={setPluginBindings} requirements={preview.requirements} secrets={secrets} onSecretsChange={setSecrets} deferBindings={deferBindings} onDeferChange={setDeferBindings} disabled={pending} />
          <SourceChangesDialog key={preview.preview_id} open={previewOpen} onOpenChange={setPreviewOpen} groups={[
            { id: "configuration", title: t(($) => $.tab_body.publish.configuration_changes), changes: preview.configuration_changes ?? [] },
            { id: "git", title: t(($) => $.tab_body.publish.git_changes), changes: preview.git_changes ?? [] },
          ]} />
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
      {canEdit && <AgentPublicationHistory agentId={source.agent_id} disabled={!canSync || pending} onRestore={(id) => void handleRestore(id)} />}
    </div>
  );
}
