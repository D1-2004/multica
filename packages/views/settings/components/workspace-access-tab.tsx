"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, KeyRound, Pencil, Power, PowerOff, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { copyText } from "@multica/ui/lib/clipboard";
import type {
  WorkspaceAccessCapability,
  WorkspaceAccessGrant,
  WorkspaceAccessResourceScope,
} from "@multica/core/types";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import {
  workspaceAccessGrantListOptions,
  workspaceAccessTokenListOptions,
} from "@multica/core/workspace-access/queries";
import {
  useCreateWorkspaceAccessGrant,
  useCreateWorkspaceAccessToken,
  useRevokeWorkspaceAccessToken,
  useSetWorkspaceAccessGrantEnabled,
  useUpdateWorkspaceAccessGrant,
  useUpdateWorkspaceAccessTokenExpiry,
} from "@multica/core/workspace-access/mutations";
import { useT } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";

const ALL_CAPABILITIES: WorkspaceAccessCapability[] = [
  "deployment.manage",
  "deployment.retire",
  "trace.read",
];
const EXPIRY_OPTIONS = ["30", "90", "365", "never"] as const;

function expiryFromOption(option: string): string | null {
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
  const { data: grants = [], isLoading } = useQuery({
    ...workspaceAccessGrantListOptions(wsId),
    enabled: !!wsId && role === "owner",
  });
  const [editorOpen, setEditorOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);

  if (!workspace || roleLoading) return null;
  if (role !== "owner") {
    return (
      <SettingsTab title={t(($) => $.workspace_access.title)}>
        <Alert><AlertDescription>{t(($) => $.workspace_access.owner_only)}</AlertDescription></Alert>
      </SettingsTab>
    );
  }

  return (
    <SettingsTab
      title={t(($) => $.workspace_access.title)}
      description={t(($) => $.workspace_access.description)}
    >
      <SettingsSection
        title={t(($) => $.workspace_access.grants_title)}
        description={t(($) => $.workspace_access.grants_description)}
        action={
          <Button size="sm" onClick={() => setEditorOpen(true)}>
            <Plus className="size-4" />{t(($) => $.workspace_access.create)}
          </Button>
        }
      >
        {isLoading ? (
          <p className="text-sm text-muted-foreground">{t(($) => $.workspace_access.loading)}</p>
        ) : grants.length === 0 ? (
          <Card><CardContent className="text-sm text-muted-foreground">{t(($) => $.workspace_access.empty)}</CardContent></Card>
        ) : (
          <div className="space-y-3">
            {grants.map((grant) => (
              <GrantCard key={grant.id} wsId={wsId} grant={grant} onSecret={setCreatedSecret} />
            ))}
          </div>
        )}
      </SettingsSection>

      <GrantEditorDialog
        wsId={wsId}
        open={editorOpen}
        onOpenChange={setEditorOpen}
        onSecret={setCreatedSecret}
      />
      <SecretDialog secret={createdSecret} onClose={() => setCreatedSecret(null)} />
    </SettingsTab>
  );
}

function GrantCard({
  wsId,
  grant,
  onSecret,
}: {
  wsId: string;
  grant: WorkspaceAccessGrant;
  onSecret: (secret: string) => void;
}) {
  const { t } = useT("settings");
  const { data: tokens = [] } = useQuery(workspaceAccessTokenListOptions(wsId, grant.id));
  const setEnabled = useSetWorkspaceAccessGrantEnabled(wsId);
  const updateExpiry = useUpdateWorkspaceAccessTokenExpiry(wsId, grant.id);
  const revoke = useRevokeWorkspaceAccessToken(wsId, grant.id);
  const [editOpen, setEditOpen] = useState(false);
  const [tokenOpen, setTokenOpen] = useState(false);

  const capabilityLabel = (capability: WorkspaceAccessCapability) => {
    if (capability === "deployment.manage") return t(($) => $.workspace_access.capabilities.manage);
    if (capability === "deployment.retire") return t(($) => $.workspace_access.capabilities.retire);
    return t(($) => $.workspace_access.capabilities.trace);
  };

  const toggleStatus = async () => {
    try {
      await setEnabled.mutateAsync({ grantId: grant.id, enabled: grant.status !== "active" });
      toast.success(t(($) => $.workspace_access.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace_access.save_failed));
    }
  };

  return (
    <Card>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="font-medium">{grant.name}</h3>
              <span className={`rounded-full px-2 py-0.5 text-xs ${grant.status === "active" ? "bg-success/10 text-success" : "bg-muted text-muted-foreground"}`}>
                {grant.status === "active" ? t(($) => $.workspace_access.active) : t(($) => $.workspace_access.disabled)}
              </span>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {grant.resource_scope === "own_agents"
                ? t(($) => $.workspace_access.scope.own_agents)
                : t(($) => $.workspace_access.scope.workspace)}
              {" · "}{grant.capabilities.map(capabilityLabel).join(" · ")}
            </p>
          </div>
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => setEditOpen(true)}>
              <Pencil className="size-4" />{t(($) => $.workspace_access.edit)}
            </Button>
            <Button size="sm" variant="outline" onClick={toggleStatus} disabled={setEnabled.isPending}>
              {grant.status === "active" ? <PowerOff className="size-4" /> : <Power className="size-4" />}
              {grant.status === "active" ? t(($) => $.workspace_access.disable) : t(($) => $.workspace_access.enable)}
            </Button>
            <Button size="sm" onClick={() => setTokenOpen(true)}>
              <KeyRound className="size-4" />{t(($) => $.workspace_access.issue_token)}
            </Button>
          </div>
        </div>

        <div className="space-y-2 border-t border-surface-border pt-3">
          {tokens.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t(($) => $.workspace_access.no_tokens)}</p>
          ) : tokens.map((token) => (
            <div key={token.id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-surface-border p-3">
              <div className="min-w-0">
                <div className="text-sm font-medium">{token.name} <span className="font-mono text-xs text-muted-foreground">{token.token_prefix}…</span></div>
                <div className="text-xs text-muted-foreground">
                  {t(($) => $.workspace_access.expires)}: {formatDate(token.expires_at)} · {t(($) => $.workspace_access.last_used)}: {formatDate(token.last_used_at)}
                  {token.revoked_at ? ` · ${t(($) => $.workspace_access.revoked)}` : ""}
                </div>
              </div>
              {!token.revoked_at ? (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => updateExpiry.mutate({ tokenId: token.id, expiresAt: expiryFromOption("90") })}
                  >{t(($) => $.workspace_access.extend_90)}</Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      if (window.confirm(t(($) => $.workspace_access.revoke_confirm))) revoke.mutate(token.id);
                    }}
                    aria-label={t(($) => $.workspace_access.revoke)}
                  ><Trash2 className="size-4 text-destructive" /></Button>
                </div>
              ) : null}
            </div>
          ))}
        </div>
      </CardContent>

      <GrantEditorDialog key={`${grant.id}:${grant.version}`} wsId={wsId} grant={grant} open={editOpen} onOpenChange={setEditOpen} onSecret={onSecret} />
      <TokenDialog wsId={wsId} grantId={grant.id} open={tokenOpen} onOpenChange={setTokenOpen} onSecret={onSecret} />
    </Card>
  );
}

function GrantEditorDialog({
  wsId,
  grant,
  open,
  onOpenChange,
  onSecret,
}: {
  wsId: string;
  grant?: WorkspaceAccessGrant;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSecret: (secret: string) => void;
}) {
  const { t } = useT("settings");
  const createGrant = useCreateWorkspaceAccessGrant(wsId);
  const updateGrant = useUpdateWorkspaceAccessGrant(wsId);
  const createToken = useCreateWorkspaceAccessToken(wsId);
  const [name, setName] = useState(grant?.name ?? "");
  const [capabilities, setCapabilities] = useState<WorkspaceAccessCapability[]>(grant?.capabilities ?? ["deployment.manage", "trace.read"]);
  const [scope, setScope] = useState<WorkspaceAccessResourceScope>(grant?.resource_scope ?? "own_agents");
  const [tokenName, setTokenName] = useState("");
  const [expiry, setExpiry] = useState("90");

  const capabilityLabels = useMemo(() => ({
    "deployment.manage": t(($) => $.workspace_access.capabilities.manage),
    "deployment.retire": t(($) => $.workspace_access.capabilities.retire),
    "trace.read": t(($) => $.workspace_access.capabilities.trace),
  }), [t]);

  const toggleCapability = (capability: WorkspaceAccessCapability, checked: boolean) => {
    setCapabilities((current) => checked
      ? Array.from(new Set([...current, capability]))
      : current.filter((item) => item !== capability));
  };

  const save = async () => {
    try {
      if (grant) {
        await updateGrant.mutateAsync({
          grantId: grant.id,
          data: { name: name.trim(), capabilities, resource_scope: scope, version: grant.version },
        });
      } else {
        const created = await createGrant.mutateAsync({ name: name.trim(), capabilities, resource_scope: scope });
        await createToken.mutateAsync({
          grantId: created.id,
          data: { name: tokenName.trim(), expires_at: expiryFromOption(expiry) },
          onToken: onSecret,
        });
      }
      onOpenChange(false);
      toast.success(t(($) => $.workspace_access.saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace_access.save_failed));
    }
  };

  const pending = createGrant.isPending || updateGrant.isPending || createToken.isPending;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{grant ? t(($) => $.workspace_access.edit_title) : t(($) => $.workspace_access.create_title)}</DialogTitle></DialogHeader>
        <div className="space-y-5">
          <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={t(($) => $.workspace_access.name_placeholder)} />
          <div className="space-y-2">
            <div className="text-sm font-medium">{t(($) => $.workspace_access.permissions)}</div>
            {ALL_CAPABILITIES.map((capability) => (
              <label key={capability} className="flex items-center gap-2 text-sm">
                <Checkbox checked={capabilities.includes(capability)} onCheckedChange={(value) => toggleCapability(capability, value === true)} />
                {capabilityLabels[capability]}
              </label>
            ))}
          </div>
          <div className="space-y-2">
            <div className="text-sm font-medium">{t(($) => $.workspace_access.scope_title)}</div>
            <Select value={scope} onValueChange={(value) => setScope(value as WorkspaceAccessResourceScope)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="own_agents">{t(($) => $.workspace_access.scope.own_agents)}</SelectItem>
                <SelectItem value="workspace">{t(($) => $.workspace_access.scope.workspace)}</SelectItem>
              </SelectContent>
            </Select>
            {scope === "workspace" ? <Alert><AlertDescription>{t(($) => $.workspace_access.workspace_warning)}</AlertDescription></Alert> : null}
          </div>
          {!grant ? (
            <div className="space-y-2 border-t border-surface-border pt-4">
              <div className="text-sm font-medium">{t(($) => $.workspace_access.first_token)}</div>
              <Input value={tokenName} onChange={(event) => setTokenName(event.target.value)} />
              <ExpirySelect value={expiry} onChange={setExpiry} />
              {expiry === "never" ? <Alert><AlertDescription>{t(($) => $.workspace_access.permanent_warning)}</AlertDescription></Alert> : null}
            </div>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{t(($) => $.workspace_access.cancel)}</Button>
          <Button onClick={save} disabled={pending || !name.trim() || capabilities.length === 0 || (!grant && !tokenName.trim())}>{t(($) => $.workspace_access.save)}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function TokenDialog({ wsId, grantId, open, onOpenChange, onSecret }: { wsId: string; grantId: string; open: boolean; onOpenChange: (open: boolean) => void; onSecret: (secret: string) => void }) {
  const { t } = useT("settings");
  const createToken = useCreateWorkspaceAccessToken(wsId);
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState("90");
  const create = async () => {
    try {
      await createToken.mutateAsync({
        grantId,
        data: { name: name.trim(), expires_at: expiryFromOption(expiry) },
        onToken: onSecret,
      });
      onOpenChange(false);
      setName("");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace_access.save_failed));
    }
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader><DialogTitle>{t(($) => $.workspace_access.issue_token)}</DialogTitle></DialogHeader>
        <div className="space-y-3">
          <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={t(($) => $.workspace_access.token_name_placeholder)} />
          <ExpirySelect value={expiry} onChange={setExpiry} />
          {expiry === "never" ? <Alert><AlertDescription>{t(($) => $.workspace_access.permanent_warning)}</AlertDescription></Alert> : null}
        </div>
        <DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>{t(($) => $.workspace_access.cancel)}</Button><Button onClick={create} disabled={!name.trim() || createToken.isPending}>{t(($) => $.workspace_access.issue_token)}</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ExpirySelect({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const { t } = useT("settings");
  return (
    <Select value={value} onValueChange={(next) => next && onChange(next)}>
      <SelectTrigger><SelectValue /></SelectTrigger>
      <SelectContent>
        {EXPIRY_OPTIONS.map((option) => <SelectItem key={option} value={option}>{option === "never" ? t(($) => $.workspace_access.expiry.never) : t(($) => $.workspace_access.expiry.days, { count: Number(option) })}</SelectItem>)}
      </SelectContent>
    </Select>
  );
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
  const close = () => {
    setCopied(false);
    setConfirmed(false);
    onClose();
  };
  return (
    <Dialog open={!!secret} onOpenChange={(open) => { if (!open && confirmed) close(); }}>
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
