"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { gitConnectionsOptions, useConnectGitRepository, useDeleteGitConnection } from "@multica/core/git-repo";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentMember } from "@multica/core/permissions";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

export function GitRepoTab() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { role } = useCurrentMember(wsId);
  const { t } = useT("settings");
  const connections = useQuery(gitConnectionsOptions(wsId));
  const connect = useConnectGitRepository(wsId);
  const disconnect = useDeleteGitConnection(wsId);
  const [repository, setRepository] = useState("");
  const [token, setToken] = useState("");
  const [replaceId, setReplaceId] = useState("");
  const [error, setError] = useState("");
  const canManage = role === "owner" || role === "admin";
  const save = async () => {
    setError("");
    try {
      await connect.mutateAsync({ repository_url: repository, token, connection_id: replaceId || undefined });
      setToken(""); setReplaceId(""); setRepository("");
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    finally { connect.reset(); }
  };
  return <div className="mx-auto max-w-3xl space-y-6 p-6">
    <h2 className="text-title font-medium">{t(($) => $.git_repo.title)}</h2>
    <p className="text-body text-muted-foreground">{t(($) => $.git_repo.description)}</p>
    <AppLink className="text-body underline" href={`${paths.settings()}?tab=github`}>{t(($) => $.git_repo.github)}</AppLink>
    {connections.data?.connections.map((connection) => <div key={connection.id} className="flex flex-wrap items-center gap-3 rounded-md border p-3">
      <span className="flex-1 break-all text-body">{connection.account_login} · {connection.provider}</span>
      {canManage && connection.provider === "alibaba_code" && <Button variant="outline" onClick={() => { setReplaceId(connection.id); setToken(""); }}>{t(($) => $.git_repo.replace)}</Button>}
      {canManage && <Button variant="outline" disabled={disconnect.isPending} onClick={() => { void disconnect.mutateAsync(connection.id).catch((failure: unknown) => setError(failure instanceof Error ? failure.message : String(failure))); }}>{t(($) => $.git_repo.disconnect)}</Button>}
    </div>)}
    {canManage && <fieldset disabled={!connections.data?.token_connections_available || connect.isPending} className="space-y-3 rounded-md border p-4 disabled:opacity-60">
      <p className="text-body font-medium">{t(($) => replaceId ? $.git_repo.replace : $.git_repo.connect)}</p>
      <Label htmlFor="git-identity-repository">{t(($) => $.git_repo.repository)}</Label>
      <Input id="git-identity-repository" value={repository} onChange={(event) => setRepository(event.target.value)} placeholder="https://code.alibaba-inc.com/team/repository" />
      <Label htmlFor="git-identity-token">{t(($) => $.git_repo.token_label)}</Label>
      <Input id="git-identity-token" type="password" autoComplete="new-password" value={token} onChange={(event) => setToken(event.target.value)} />
      <p className="text-caption text-muted-foreground">{t(($) => $.git_repo.token_hint)}</p>
      <Button disabled={!repository.trim() || !token.trim()} onClick={() => void save()}>{t(($) => $.git_repo.save)}</Button>
      {replaceId && <Button variant="ghost" onClick={() => { setReplaceId(""); setToken(""); }}>{t(($) => $.git_repo.cancel)}</Button>}
    </fieldset>}
    {canManage && connections.data?.token_connections_available === false && <p className="text-body text-muted-foreground">{t(($) => $.git_repo.unavailable)}</p>}
    {(error || connections.error) && <p role="alert" className="break-words text-body text-destructive">{error || connections.error?.message}</p>}
  </div>;
}
