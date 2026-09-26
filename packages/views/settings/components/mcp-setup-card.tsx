"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Link2, Loader2, ShieldCheck, Unplug } from "lucide-react";
import { useCurrentMember } from "@multica/core/permissions";
import { useFeatureEnabled } from "@multica/core/config";
import {
  workspaceMCPConnectionsOptions,
  useCreateWorkspaceMCPConnection,
  useRevokeWorkspaceMCPConnection,
} from "@multica/core/workspace";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { copyText } from "@multica/ui/lib/clipboard";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { SettingsSection } from "./settings-layout";

export function MCPSetupCard({
  workspaceId,
  workspaceName,
}: {
  workspaceId: string;
  workspaceName: string;
}) {
  const { t } = useT("settings");
  const { role } = useCurrentMember(workspaceId);
  const enabled = useFeatureEnabled("workspace_mcp_endpoint_enabled", false);
  const canManage = role === "owner" || role === "admin";
  const connections = useQuery({
    ...workspaceMCPConnectionsOptions(workspaceId),
    enabled: enabled && canManage,
  });
  const create = useCreateWorkspaceMCPConnection(workspaceId);
  const revoke = useRevokeWorkspaceMCPConnection(workspaceId);
  const [name, setName] = useState("");
  const [permission, setPermission] = useState("read");
  const [days, setDays] = useState("7");
  const [link, setLink] = useState<{ id: string; url: string } | null>(null);
  const [copied, setCopied] = useState(false);
  const [revoking, setRevoking] = useState<{ id: string; name: string } | null>(
    null,
  );

  const generate = async () => {
    try {
      const result = await create.mutateAsync({
        name: name.trim(),
        scopes: permission === "read" ? ["read"] : ["read", "write"],
        expires_at: new Date(
          Date.now() + Number(days) * 86400000,
        ).toISOString(),
      });
      setLink(result);
      setCopied(false);
      setName("");
    } catch {
      toast.error(t(($) => $.mcp.workspace.create_failed));
    } finally {
      create.reset();
    }
  };
  const copy = async () => {
    if (!link) return;
    const ok = await copyText(link.url);
    setCopied(ok);
    if (!ok) toast.error(t(($) => $.mcp.copy_failed));
  };

  return (
    <>
      <Card className="gap-0 py-0 shadow-none">
        <CardContent className="space-y-5 p-5">
          <div className="flex items-start gap-3">
            <ShieldCheck className="mt-0.5 size-5 shrink-0 text-muted-foreground" />
            <div className="min-w-0">
              <p className="text-body font-medium">
                {t(($) => $.mcp.workspace.scope, { workspace: workspaceName })}
              </p>
              <p className="mt-1 text-caption leading-5 text-muted-foreground">
                {t(($) => $.mcp.workspace.scope_hint)}
              </p>
            </div>
          </div>
          {!enabled ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.mcp.workspace.disabled)}
            </p>
          ) : !canManage ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.mcp.workspace.admin_only)}
            </p>
          ) : (
            <form
              className="space-y-4"
              onSubmit={(e) => {
                e.preventDefault();
                void generate();
              }}
            >
              <label className="block space-y-2 text-caption font-medium">
                <span>{t(($) => $.mcp.workspace.name)}</span>
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  maxLength={120}
                  placeholder={t(($) => $.mcp.workspace.name_placeholder)}
                  required
                />
              </label>
              <div className="grid gap-4 sm:grid-cols-2">
                <label className="block space-y-2 text-caption font-medium">
                  <span>{t(($) => $.mcp.workspace.permission)}</span>
                  <select
                    className="h-9 w-full rounded-md border border-input bg-background px-3 text-body"
                    value={permission}
                    onChange={(e) => setPermission(e.target.value)}
                  >
                    <option value="read">
                      {t(($) => $.mcp.workspace.read)}
                    </option>
                    <option value="write">
                      {t(($) => $.mcp.workspace.write)}
                    </option>
                  </select>
                </label>
                <label className="block space-y-2 text-caption font-medium">
                  <span>{t(($) => $.mcp.workspace.expiry)}</span>
                  <select
                    className="h-9 w-full rounded-md border border-input bg-background px-3 text-body"
                    value={days}
                    onChange={(e) => setDays(e.target.value)}
                  >
                    {[1, 7, 30].map((n) => (
                      <option key={n} value={n}>
                        {t(($) => $.mcp.workspace.days, { count: n })}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <Button type="submit" disabled={!name.trim() || create.isPending}>
                {create.isPending ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <Link2 className="size-4" />
                )}
                {t(($) => $.mcp.workspace.create)}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
      {enabled && canManage && (
        <SettingsSection title={t(($) => $.mcp.workspace.connections)}>
          {connections.isPending ? (
            <Loader2 className="size-4 animate-spin" />
          ) : connections.isError ? (
            <Button
              variant="outline"
              onClick={() => void connections.refetch()}
            >
              {t(($) => $.mcp.workspace.retry)}
            </Button>
          ) : (
            <Card className="gap-0 overflow-hidden py-0 shadow-none">
              <CardContent className="divide-y divide-surface-border p-0">
                {!connections.data?.length && (
                  <p className="p-5 text-body text-muted-foreground">
                    {t(($) => $.mcp.workspace.empty)}
                  </p>
                )}
                {connections.data?.map((item) => {
                  const expired =
                    new Date(item.expiresAt).getTime() <= Date.now();
                  return (
                    <div
                      key={item.id}
                      className="flex items-center justify-between gap-3 px-5 py-4"
                    >
                      <div className="min-w-0 space-y-1">
                        <p className="truncate text-body font-medium">
                          {item.name}
                        </p>
                        <p className="text-caption text-muted-foreground">
                          {item.revokedAt
                            ? t(($) => $.mcp.workspace.revoked)
                            : expired
                              ? t(($) => $.mcp.workspace.expired)
                              : t(($) => $.mcp.workspace.active)}{" "}
                          ·{" "}
                          {item.scopes.includes("write")
                            ? t(($) => $.mcp.workspace.write)
                            : t(($) => $.mcp.workspace.read)}
                        </p>
                        <p className="text-caption text-muted-foreground">
                          {t(($) => $.mcp.workspace.expires, {
                            date: new Date(item.expiresAt).toLocaleString(),
                          })}
                        </p>
                      </div>
                      {!item.revokedAt && !expired && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setRevoking(item)}
                        >
                          <Unplug className="size-3.5" />
                          {t(($) => $.mcp.workspace.revoke)}
                        </Button>
                      )}
                    </div>
                  );
                })}
              </CardContent>
            </Card>
          )}
        </SettingsSection>
      )}
      <Dialog
        open={link !== null}
        onOpenChange={(open) => {
          if (!open) setLink(null);
        }}
      >
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{t(($) => $.mcp.workspace.ready)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.mcp.workspace.one_time)}
            </DialogDescription>
          </DialogHeader>
          <Input
            readOnly
            type="password"
            aria-label={t(($) => $.mcp.workspace.link)}
            value={link?.url ?? ""}
            className="font-mono text-caption"
          />
          <p className="text-caption leading-5 text-muted-foreground">
            {t(($) => $.mcp.workspace.client_hint)}
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setLink(null)}>
              {t(($) => $.mcp.dialog.done)}
            </Button>
            <Button onClick={() => void copy()}>
              {copied ? (
                <Check className="size-4" />
              ) : (
                <Copy className="size-4" />
              )}
              {copied
                ? t(($) => $.mcp.workspace.copied)
                : t(($) => $.mcp.workspace.copy)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open && !revoke.isPending) setRevoking(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.mcp.workspace.revoke)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.mcp.workspace.revoke_hint, {
                name: revoking?.name ?? "",
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              disabled={revoke.isPending}
              onClick={() => setRevoking(null)}
            >
              {t(($) => $.mcp.dialog.cancel)}
            </Button>
            <Button
              variant="destructive"
              disabled={revoke.isPending}
              onClick={async () => {
                if (!revoking) return;
                try {
                  await revoke.mutateAsync(revoking.id);
                  if (link?.id === revoking.id) setLink(null);
                  setRevoking(null);
                } catch {
                  toast.error(t(($) => $.mcp.workspace.revoke_failed));
                }
              }}
            >
              {t(($) => $.mcp.workspace.revoke)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
