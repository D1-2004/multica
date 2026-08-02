"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { copyText } from "@multica/ui/lib/clipboard";
import type { WorkspaceAccessCapability, WorkspaceAccessResourceScope, WorkspaceAccessToken } from "@multica/core/types";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { workspaceAccessTokenListOptions } from "@multica/core/workspace-access/queries";
import {
  useCreateWorkspaceAccessToken,
  useRegenerateWorkspaceAccessToken,
  useRevokeWorkspaceAccessToken,
  useUpdateWorkspaceAccessToken,
} from "@multica/core/workspace-access/mutations";
import { useT } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";

const ALL_CAPABILITIES: WorkspaceAccessCapability[] = ["deployment.manage", "deployment.retire", "trace.read"];
const EXPIRY_OPTIONS = ["30", "90", "365", "never"] as const;

function expiryFromOption(option: string, current: string | null = null): string | null {
  if (option === "keep") return current;
  if (option === "never") return null;
  return new Date(Date.now() + Number(option) * 24 * 60 * 60 * 1000).toISOString();
}

function formatDate(value: string | null): string {
  return value ? new Date(value).toLocaleString() : "—";
}

export function WorkspaceAccessTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role, isLoading: roleLoading } = useCurrentMember(wsId);
  const { data: tokens = [], isLoading } = useQuery({
    ...workspaceAccessTokenListOptions(wsId),
    enabled: !!wsId && role === "owner",
  });
  const [editorOpen, setEditorOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);

  if (!workspace || roleLoading) return null;
  if (role !== "owner") {
    return <SettingsTab title={t(($) => $.workspace_access.title)}><Alert><AlertDescription>{t(($) => $.workspace_access.owner_only)}</AlertDescription></Alert></SettingsTab>;
  }

  return (
    <SettingsTab title={t(($) => $.workspace_access.title)} description={t(($) => $.workspace_access.description)}>
      <SettingsSection
        title={t(($) => $.workspace_access.tokens_title)}
        description={t(($) => $.workspace_access.tokens_description)}
        action={<Button size="sm" onClick={() => setEditorOpen(true)}><Plus className="size-4" />{t(($) => $.workspace_access.create)}</Button>}
      >
        {isLoading ? (
          <p className="text-sm text-muted-foreground">{t(($) => $.workspace_access.loading)}</p>
        ) : tokens.length === 0 ? (
          <Card><CardContent className="text-sm text-muted-foreground">{t(($) => $.workspace_access.empty)}</CardContent></Card>
        ) : (
          <div className="space-y-3">
            {tokens.map((token) => <TokenCard key={`${token.id}:${token.version}`} wsId={wsId} token={token} onSecret={setCreatedSecret} />)}
          </div>
        )}
      </SettingsSection>
      <TokenEditorDialog wsId={wsId} open={editorOpen} onOpenChange={setEditorOpen} onSecret={setCreatedSecret} />
      <SecretDialog secret={createdSecret} onClose={() => setCreatedSecret(null)} />
    </SettingsTab>
  );
}

function TokenCard({ wsId, token, onSecret }: { wsId: string; token: WorkspaceAccessToken; onSecret: (secret: string) => void }) {
  const { t } = useT("settings");
  const revoke = useRevokeWorkspaceAccessToken(wsId);
  const [editOpen, setEditOpen] = useState(false);
  const [regenerateOpen, setRegenerateOpen] = useState(false);
  const capabilityLabel = (capability: WorkspaceAccessCapability) => capability === "deployment.manage"
    ? t(($) => $.workspace_access.capabilities.manage)
    : capability === "deployment.retire"
      ? t(($) => $.workspace_access.capabilities.retire)
      : t(($) => $.workspace_access.capabilities.trace);

  return (
    <Card>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="font-medium">{token.name}</h3>
              {token.revoked_at ? <span className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">{t(($) => $.workspace_access.revoked)}</span> : null}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {token.resource_scope === "own_agents" ? t(($) => $.workspace_access.scope.own_agents) : t(($) => $.workspace_access.scope.workspace)}
              {" · "}{token.capabilities.map(capabilityLabel).join(" · ")}
            </p>
            <p className="mt-2 text-xs text-muted-foreground">
              <span className="font-mono">{token.token_prefix}…</span>
              {" · "}{t(($) => $.workspace_access.expires)}: {formatDate(token.expires_at)}
              {" · "}{t(($) => $.workspace_access.last_used)}: {formatDate(token.last_used_at)}
            </p>
          </div>
          {!token.revoked_at ? (
            <div className="flex gap-2">
              <Button size="sm" variant="outline" onClick={() => setEditOpen(true)}><Pencil className="size-4" />{t(($) => $.workspace_access.edit)}</Button>
              <Button size="sm" variant="outline" onClick={() => setRegenerateOpen(true)}><RefreshCw className="size-4" />{t(($) => $.workspace_access.regenerate)}</Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={revoke.isPending}
                onClick={() => { if (window.confirm(t(($) => $.workspace_access.revoke_confirm))) revoke.mutate(token.id); }}
                aria-label={t(($) => $.workspace_access.revoke)}
              ><Trash2 className="size-4 text-destructive" /></Button>
            </div>
          ) : null}
        </div>
      </CardContent>
      <TokenEditorDialog wsId={wsId} token={token} open={editOpen} onOpenChange={setEditOpen} onSecret={onSecret} />
      <RegenerateTokenDialog wsId={wsId} token={token} open={regenerateOpen} onOpenChange={setRegenerateOpen} onSecret={onSecret} />
    </Card>
  );
}

function TokenEditorDialog({ wsId, token, open, onOpenChange, onSecret }: {
  wsId: string; token?: WorkspaceAccessToken; open: boolean; onOpenChange: (open: boolean) => void; onSecret: (secret: string) => void;
}) {
  const { t } = useT("settings");
  const createToken = useCreateWorkspaceAccessToken(wsId);
  const updateToken = useUpdateWorkspaceAccessToken(wsId);
  const [name, setName] = useState(token?.name ?? "");
  const [capabilities, setCapabilities] = useState<WorkspaceAccessCapability[]>(token?.capabilities ?? ["deployment.manage", "trace.read"]);
  const [scope, setScope] = useState<WorkspaceAccessResourceScope>(token?.resource_scope ?? "own_agents");
  const [expiry, setExpiry] = useState(token ? "keep" : "90");
  const capabilityLabels = useMemo(() => ({
    "deployment.manage": t(($) => $.workspace_access.capabilities.manage),
    "deployment.retire": t(($) => $.workspace_access.capabilities.retire),
    "trace.read": t(($) => $.workspace_access.capabilities.trace),
  }), [t]);
  const toggleCapability = (capability: WorkspaceAccessCapability, checked: boolean) => setCapabilities((current) => checked
    ? Array.from(new Set([...current, capability]))
    : current.filter((item) => item !== capability));
  const save = async () => {
    try {
      const expiresAt = expiryFromOption(expiry, token?.expires_at ?? null);
      if (token) {
        await updateToken.mutateAsync({ tokenId: token.id, data: { name: name.trim(), capabilities, resource_scope: scope, expires_at: expiresAt, version: token.version } });
      } else {
        await createToken.mutateAsync({ data: { name: name.trim(), capabilities, resource_scope: scope, expires_at: expiresAt }, onToken: onSecret });
      }
      onOpenChange(false);
      toast.success(t(($) => $.workspace_access.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace_access.save_failed));
    }
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{token ? t(($) => $.workspace_access.edit_title) : t(($) => $.workspace_access.create_title)}</DialogTitle></DialogHeader>
        <div className="space-y-5">
          <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={t(($) => $.workspace_access.name_placeholder)} />
          <div className="space-y-2">
            <div className="text-sm font-medium">{t(($) => $.workspace_access.permissions)}</div>
            {ALL_CAPABILITIES.map((capability) => <label key={capability} className="flex items-center gap-2 text-sm">
              <Checkbox checked={capabilities.includes(capability)} onCheckedChange={(value) => toggleCapability(capability, value === true)} />
              {capabilityLabels[capability]}
            </label>)}
          </div>
          <div className="space-y-2">
            <div className="text-sm font-medium">{t(($) => $.workspace_access.scope_title)}</div>
            <Select value={scope} onValueChange={(value) => setScope(value as WorkspaceAccessResourceScope)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="own_agents">{t(($) => $.workspace_access.scope.own_agents)}</SelectItem><SelectItem value="workspace">{t(($) => $.workspace_access.scope.workspace)}</SelectItem></SelectContent>
            </Select>
            {scope === "workspace" ? <Alert><AlertDescription>{t(($) => $.workspace_access.workspace_warning)}</AlertDescription></Alert> : null}
          </div>
          <ExpirySelect value={expiry} onChange={setExpiry} includeKeep={!!token} />
          {expiry === "never" ? <Alert><AlertDescription>{t(($) => $.workspace_access.permanent_warning)}</AlertDescription></Alert> : null}
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t(($) => $.workspace_access.cancel)}</Button><Button onClick={save} disabled={!name.trim() || capabilities.length === 0 || createToken.isPending || updateToken.isPending}>{t(($) => $.workspace_access.save)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RegenerateTokenDialog({ wsId, token, open, onOpenChange, onSecret }: {
  wsId: string; token: WorkspaceAccessToken; open: boolean; onOpenChange: (open: boolean) => void; onSecret: (secret: string) => void;
}) {
  const { t } = useT("settings");
  const regenerate = useRegenerateWorkspaceAccessToken(wsId);
  const [expiry, setExpiry] = useState("90");
  const submit = async () => {
    try {
      await regenerate.mutateAsync({ tokenId: token.id, data: { expires_at: expiryFromOption(expiry), version: token.version }, onToken: onSecret });
      onOpenChange(false);
      toast.success(t(($) => $.workspace_access.regenerated));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace_access.save_failed));
    }
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{t(($) => $.workspace_access.regenerate_title)}</DialogTitle></DialogHeader>
        <Alert><AlertDescription>{t(($) => $.workspace_access.regenerate_warning)}</AlertDescription></Alert>
        <ExpirySelect value={expiry} onChange={setExpiry} />
        {expiry === "never" ? <Alert><AlertDescription>{t(($) => $.workspace_access.permanent_warning)}</AlertDescription></Alert> : null}
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t(($) => $.workspace_access.cancel)}</Button><Button onClick={submit} disabled={regenerate.isPending}>{t(($) => $.workspace_access.regenerate)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ExpirySelect({ value, onChange, includeKeep = false }: { value: string; onChange: (value: string) => void; includeKeep?: boolean }) {
  const { t } = useT("settings");
  return <Select value={value} onValueChange={(next) => next && onChange(next)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{includeKeep ? <SelectItem value="keep">{t(($) => $.workspace_access.expiry.keep)}</SelectItem> : null}{EXPIRY_OPTIONS.map((option) => <SelectItem key={option} value={option}>{option === "never" ? t(($) => $.workspace_access.expiry.never) : t(($) => $.workspace_access.expiry.days, { count: Number(option) })}</SelectItem>)}</SelectContent></Select>;
}

function SecretDialog({ secret, onClose }: { secret: string | null; onClose: () => void }) {
  const { t } = useT("settings");
  const [copied, setCopied] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const copy = async () => {
    if (secret && await copyText(secret)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };
  const close = () => { setCopied(false); setConfirmed(false); onClose(); };
  return (
    <Dialog open={!!secret} onOpenChange={(next) => { if (!next && confirmed) close(); }}>
      <DialogContent>
        <DialogHeader><DialogTitle>{t(($) => $.workspace_access.secret_title)}</DialogTitle></DialogHeader>
        <Alert><AlertDescription>{t(($) => $.workspace_access.secret_warning)}</AlertDescription></Alert>
        <div className="flex gap-2"><Input readOnly value={secret ?? ""} className="font-mono" /><Button variant="outline" onClick={copy}>{copied ? <Check className="size-4" /> : <Copy className="size-4" />}</Button></div>
        <label className="flex items-center gap-2 text-sm"><Checkbox checked={confirmed} onCheckedChange={(value) => setConfirmed(value === true)} />{t(($) => $.workspace_access.secret_confirm)}</label>
        <DialogFooter><Button onClick={close} disabled={!confirmed}>{t(($) => $.workspace_access.close)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
