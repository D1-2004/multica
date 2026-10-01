"use client";

import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { errorCode } from "@multica/core/api";
import {
  ORG_ID_PATTERN,
  useCreateAgentTenant,
  useDeleteAgentTenant,
  useRenameAgentTenant,
  type AgentTenant,
} from "@multica/core/context-capabilities";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../../i18n";
import { ConfirmDialog, errorMessage } from "./connectors-ui";

/** Server limit of a tenant's display name. */
const TENANT_NAME_MAX_LENGTH = 64;

/**
 * 新建租户: an enterprise the agent serves, with its name and DingTalk
 * OrgId. Awaits the server (409 when the OrgId already has a tenant) and
 * reports the created tenant so the tree can select it.
 */
export function TenantCreateDialog({
  wsId,
  agentId,
  open,
  initialOrgId = "",
  onOpenChange,
  onCreated,
}: {
  wsId: string;
  agentId: string;
  open: boolean;
  /** Prefilled OrgId, e.g. an org seen without a tenant. */
  initialOrgId?: string;
  onOpenChange: (open: boolean) => void;
  onCreated: (tenant: AgentTenant) => void;
}) {
  const { t } = useT("agents");
  const create = useCreateAgentTenant(wsId, agentId);
  const [name, setName] = useState("");
  const [orgId, setOrgId] = useState("");
  const [touched, setTouched] = useState(false);
  const [serverError, setServerError] = useState("");

  useEffect(() => {
    if (!open) return;
    setName("");
    setOrgId(initialOrgId);
    setTouched(false);
    setServerError("");
  }, [open, initialOrgId]);

  const trimmedName = name.trim();
  const trimmedOrgId = orgId.trim();
  const error =
    trimmedName === ""
      ? t(($) => $.tab_body.scenes.tenant_name_required)
      : trimmedName.length > TENANT_NAME_MAX_LENGTH
        ? t(($) => $.tab_body.scenes.tenant_name_too_long, { max: TENANT_NAME_MAX_LENGTH })
        : !ORG_ID_PATTERN.test(trimmedOrgId)
          ? t(($) => $.tab_body.scenes.tenant_org_id_invalid)
          : "";

  const submit = async () => {
    setTouched(true);
    if (error || create.isPending) return;
    setServerError("");
    try {
      const tenant = await create.mutateAsync({ orgId: trimmedOrgId, name: trimmedName });
      toast.success(t(($) => $.tab_body.scenes.tenant_created));
      onCreated(tenant);
    } catch (e) {
      setServerError(
        errorCode(e) === "tenant_exists"
          ? t(($) => $.tab_body.scenes.tenant_exists)
          : errorMessage(e, t(($) => $.tab_body.scenes.tenant_create_failed)),
      );
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next || !create.isPending) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tab_body.scenes.tenant_create)}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <div className="space-y-1.5">
            <label htmlFor="tenant-name" className="text-label font-medium">
              {t(($) => $.tab_body.scenes.tenant_name)}
            </label>
            <Input
              id="tenant-name"
              value={name}
              maxLength={TENANT_NAME_MAX_LENGTH}
              autoComplete="off"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="tenant-org-id" className="text-label font-medium">
              {t(($) => $.tab_body.scenes.tenant_org_id)}
            </label>
            <Input
              id="tenant-org-id"
              value={orgId}
              maxLength={64}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              className="font-mono"
              onChange={(event) => setOrgId(event.target.value)}
            />
          </div>
          {(touched && error) || serverError ? (
            <p role="alert" className="text-caption text-destructive">
              {touched && error ? error : serverError}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" disabled={create.isPending} onClick={() => onOpenChange(false)}>
              {t(($) => $.tab_body.connectors.cancel)}
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending && (
                <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              )}
              {t(($) => $.tab_body.scenes.tenant_create_submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/**
 * 设置 of a tenant: rename it, or delete it with its enterprise-level
 * configuration (its groups and people stay). The agent's own DingTalk org
 * cannot be deleted.
 */
export function TenantSettings({
  wsId,
  agentId,
  tenant,
  onDeleted,
}: {
  wsId: string;
  agentId: string;
  tenant: AgentTenant;
  onDeleted: () => void;
}) {
  const { t } = useT("agents");
  const rename = useRenameAgentTenant(wsId, agentId);
  const remove = useDeleteAgentTenant(wsId, agentId);
  const [name, setName] = useState(tenant.name);
  const [confirmDelete, setConfirmDelete] = useState(false);
  // Follow a rename made elsewhere unless the field has unsaved edits.
  const [baseline, setBaseline] = useState(tenant.name);
  if (tenant.name !== baseline) {
    setBaseline(tenant.name);
    if (name === baseline) setName(tenant.name);
  }
  const trimmed = name.trim();
  const changed = trimmed !== tenant.name;
  const invalid = trimmed === "" || trimmed.length > TENANT_NAME_MAX_LENGTH;
  const identity = tenant.source === "identity";

  const saveName = async () => {
    if (!changed || invalid || rename.isPending) return;
    try {
      await rename.mutateAsync({ orgId: tenant.orgId, name: trimmed });
      toast.success(t(($) => $.tab_body.scenes.tenant_renamed));
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.scenes.tenant_rename_failed)));
    }
  };

  const doDelete = async () => {
    try {
      await remove.mutateAsync(tenant.orgId);
      setConfirmDelete(false);
      toast.success(t(($) => $.tab_body.scenes.tenant_deleted));
      onDeleted();
    } catch (error) {
      toast.error(errorMessage(error, t(($) => $.tab_body.scenes.tenant_delete_failed)));
    }
  };

  return (
    <div className="space-y-8">
      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault();
          void saveName();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor="tenant-settings-name" className="text-label font-medium">
            {t(($) => $.tab_body.scenes.tenant_name)}
          </label>
          <div className="flex gap-2">
            <Input
              id="tenant-settings-name"
              value={name}
              maxLength={TENANT_NAME_MAX_LENGTH}
              autoComplete="off"
              aria-invalid={invalid || undefined}
              onChange={(event) => setName(event.target.value)}
            />
            <Button type="submit" disabled={!changed || invalid || rename.isPending}>
              {rename.isPending && (
                <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              )}
              {t(($) => $.tab_body.context_builder.save)}
            </Button>
          </div>
        </div>
        <div className="space-y-1">
          <p className="text-label font-medium">{t(($) => $.tab_body.scenes.tenant_org_id)}</p>
          <p className="break-all font-mono text-body text-muted-foreground">{tenant.orgId}</p>
        </div>
      </form>
      <div className="space-y-2">
        {identity ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.scenes.tenant_identity_note)}</p>
        ) : (
          <Button
            variant="outline"
            className="text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(true)}
          >
            {t(($) => $.tab_body.scenes.tenant_delete)}
          </Button>
        )}
      </div>
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t(($) => $.tab_body.scenes.tenant_delete_title, { name: tenant.name || tenant.orgId })}
        description={t(($) => $.tab_body.scenes.tenant_delete_description)}
        confirmLabel={t(($) => $.tab_body.scenes.tenant_delete)}
        pending={remove.isPending}
        onConfirm={() => void doDelete()}
      />
    </div>
  );
}
