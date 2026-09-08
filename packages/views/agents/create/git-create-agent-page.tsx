"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { buildCreateAgentRequest } from "@multica/core/agents";
import { githubInstallationsOptions, githubAgentRepositoriesOptions, githubAgentBranchesOptions, usePreviewGitHubAgent, useCreateGitHubAgent } from "@multica/core/github";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { AppLink, useBackOrReplace, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { RuntimePicker } from "../components/runtime-picker";
import { AgentCreateShell } from "./create-shell";
import { CreateAgentFooter } from "./create-agent-footer";
import { useCreateAgentForm } from "./use-create-agent-form";
import { withSquadParam } from "./squad-param";

export function GitCreateAgentPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const back = useBackOrReplace();
  const squadId = navigation.searchParams.get("squad");
  const form = useCreateAgentForm();
  const [installation, setInstallation] = useState("");
  const [repository, setRepository] = useState("");
  const [ref, setRef] = useState("");
  const installations = useQuery(githubInstallationsOptions(wsId));
  const installationId = installation || installations.data?.installations?.[0]?.id || "";
  const canManage = installations.data?.can_manage === true;
  const repositories = useQuery({ ...githubAgentRepositoriesOptions(wsId, installationId), enabled: canManage && !!installationId });
  const branches = useQuery({ ...githubAgentBranchesOptions(wsId, installationId, repository.trim()), enabled: canManage && !!installationId && !!repository.trim() });
  const previewMutation = usePreviewGitHubAgent(wsId);
  const createMutation = useCreateGitHubAgent(wsId, squadId);
  const preview = previewMutation.data;
  const pending = previewMutation.isPending || createMutation.isPending;
  const canCreate = canManage && !!preview?.preview_id && !preview.blockers?.length && form.draftReady && !!form.draft.name.trim() && !pending && !createMutation.isSuccess;
  const error = createMutation.error ?? previewMutation.error ?? installations.error;

  const invalidatePreview = () => { previewMutation.reset(); createMutation.reset(); };
  const handlePreview = async () => {
    try {
      const result = await previewMutation.mutateAsync({ installation_id: installationId, repository: repository.trim(), ref: ref.trim() || undefined });
      form.setDraft((current) => ({ ...current, name: result.name, description: result.description }));
    } catch { /* The form renders the mutation error. */ }
  };
  const handleCreate = async () => {
    if (!canCreate || !preview?.preview_id || !form.selectedRuntime) return;
    try {
      const result = await createMutation.mutateAsync({
        ...buildCreateAgentRequest({ draft: form.draft, runtimeId: form.selectedRuntime.id }),
        preview_id: preview.preview_id,
      });
      result.warnings?.forEach((warning) => toast.warning(warning));
      navigation.push(paths.agentDetail(result.agent.id));
    } catch { /* Keep the preview ID for an idempotent retry. */ }
  };

  return (
    <AgentCreateShell title={t(($) => $.creation_studio.modes.git.title)} step={t(($) => $.creation_studio.git.step)} onBack={() => back(withSquadParam(paths.newAgent(), squadId))}>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl space-y-6 px-5 py-8">
          <p className="text-body text-muted-foreground">{t(($) => $.creation_studio.git.description)}</p>
          {installations.isLoading && <p>{t(($) => $.tab_body.publish.loading)}</p>}
          {!installations.isLoading && (!canManage || !installationId) && (
            <p className="text-body text-muted-foreground">{t(($) => $.creation_studio.git.connection_required)} <AppLink className="underline" href={paths.settingsIntegrations()}>{t(($) => $.creation_studio.git.manage_connections)}</AppLink></p>
          )}
          <fieldset disabled={!canManage || pending || createMutation.isSuccess} className="space-y-4 disabled:opacity-60">
            <div className="space-y-2">
              <Label htmlFor="git-installation">{t(($) => $.creation_studio.git.connection)}</Label>
              <select id="git-installation" value={installationId} className="h-9 w-full rounded-md border bg-background px-3 text-body" onChange={(event) => { setInstallation(event.target.value); setRepository(""); setRef(""); invalidatePreview(); }}>
                <option value="" disabled>{t(($) => $.creation_studio.git.connection)}</option>
                {installations.data?.installations?.map((item) => <option key={item.id} value={item.id}>{item.account_login}</option>)}
              </select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="git-repository">{t(($) => $.tab_body.publish.repository)}</Label>
              <Input id="git-repository" list="git-repositories" placeholder="https://github.com/owner/agent" value={repository} onChange={(event) => { setRepository(event.target.value); setRef(""); invalidatePreview(); }} />
              <datalist id="git-repositories">{repositories.data?.repositories?.map((item) => <option key={item.full_name} value={item.full_name} />)}</datalist>
              {repositories.isError && <p role="alert" className="text-caption text-destructive">{repositories.error.message}</p>}
            </div>
            <div className="space-y-2">
              <Label htmlFor="git-ref">{t(($) => $.tab_body.publish.branch)}</Label>
              <Input id="git-ref" list="git-branches" placeholder={branches.data?.default_branch || t(($) => $.creation_studio.git.default_branch)} value={ref} onChange={(event) => { setRef(event.target.value); invalidatePreview(); }} />
              <datalist id="git-branches">{branches.data?.branches?.map((item) => <option key={item.name} value={item.name} />)}</datalist>
              {branches.isError && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.branches_failed)}</p>}
            </div>
            <Button variant="outline" disabled={!installationId || !repository.trim() || pending} onClick={() => void handlePreview()}>{t(($) => $.creation_studio.git.preview)}</Button>
          </fieldset>
          {preview && (
            <div className="space-y-4 rounded-lg border p-4">
              <p className="break-all font-mono text-caption">{preview.repository} · {preview.ref} · {preview.resolved_sha}</p>
              {preview.warnings?.map((warning) => <p key={warning} className="text-caption text-muted-foreground">{warning}</p>)}
              {preview.blockers?.map((blocker) => <p key={blocker} role="alert" className="text-caption text-destructive">{blocker}</p>)}
              <div className="space-y-2">
                <Label htmlFor="git-agent-name">{t(($) => $.create_dialog.name_label)}</Label>
                <Input id="git-agent-name" value={form.draft.name} disabled={pending} onChange={(event) => form.setDraft((current) => ({ ...current, name: event.target.value }))} />
              </div>
              <RuntimePicker runtimes={form.runtimes} runtimesLoading={form.runtimesLoading} members={form.members} currentUserId={form.currentUserId} selectedRuntimeId={form.draft.runtimeId} disabled={pending} onSelect={(runtimeId) => form.setDraft((current) => ({ ...current, runtimeId }))} />
              <p className="text-caption text-muted-foreground">{t(($) => $.creation_studio.git.confirm_hint)}</p>
              <details><summary className="cursor-pointer text-body">{t(($) => $.tabs.instructions)}</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-3 text-caption">{preview.instructions}</pre></details>
              <div className="space-y-2"><h3 className="text-body font-medium">{t(($) => $.tabs.skills)} ({preview.skills?.length ?? 0})</h3>{preview.skills?.map((skill) => <div key={skill.source_path} className="break-words text-caption"><span className="font-medium">{skill.name}</span>{skill.enabled === false && <span> · {t(($) => $.creation_studio.git.disabled_skill)}</span>} · {skill.description} ({skill.file_count})</div>)}</div>
            </div>
          )}
        </div>
        <CreateAgentFooter canCreate={canCreate} creating={createMutation.isPending} squad={!!squadId} error={error instanceof Error ? error.message : null} onCreate={() => void handleCreate()} />
      </div>
    </AgentCreateShell>
  );
}
