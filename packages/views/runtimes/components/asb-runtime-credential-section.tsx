"use client";

import { useState } from "react";
import { Check, KeyRound, Loader2, RefreshCw, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import {
  type ASBRuntimeQuota,
  useASBRuntimeCredential,
  useUpdateASBRuntimeCredential,
  useValidateASBRuntimeCredential,
} from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

function ASBQuotaList({ quotas }: { quotas: ASBRuntimeQuota[] }) {
  const { t } = useT("runtimes");

  if (quotas.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.fc_e2b_runtime.quota_empty)}
      </p>
    );
  }

  return (
    <div className="max-h-40 space-y-2 overflow-y-auto">
      {quotas.map((quota) => (
        <div
          key={`${quota.network_zone}:${quota.region}`}
          className="rounded-md border bg-background/60 px-2.5 py-2 text-caption"
        >
          <p className="font-medium">
            {quota.network_zone} · {quota.region}
          </p>
          <p className="mt-0.5 text-muted-foreground">
            {t(($) => $.fc_e2b_runtime.quota_usage, {
              usage: quota.usage,
              quota: quota.quota,
              remaining: quota.remaining,
            })}
          </p>
          {quota.volume_size_quota_gib !== undefined && (
            <p className="mt-0.5 text-muted-foreground">
              {t(($) => $.fc_e2b_runtime.volume_quota_usage, {
                usage: quota.volume_usage_gib,
                quota: quota.volume_size_quota_gib,
              })}
            </p>
          )}
        </div>
      ))}
    </div>
  );
}

export function ASBRuntimeCredentialSection({
  runtimeId,
}: {
  runtimeId: string;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const credential = useASBRuntimeCredential(wsId, runtimeId);
  const validateCredential = useValidateASBRuntimeCredential();
  const updateCredential = useUpdateASBRuntimeCredential(wsId);
  const [apiKey, setAPIKey] = useState("");
  const [validatedAPIKey, setValidatedAPIKey] = useState("");
  const trimmedAPIKey = apiKey.trim();
  const apiKeyIsValidated =
    trimmedAPIKey.length > 0 &&
    validatedAPIKey === trimmedAPIKey &&
    validateCredential.data?.valid === true;

  const handleValidate = async () => {
    if (!trimmedAPIKey) return;
    setValidatedAPIKey("");
    try {
      const result = await validateCredential.mutateAsync({
        api_key: trimmedAPIKey,
      });
      if (result.valid) setValidatedAPIKey(trimmedAPIKey);
    } catch {
      setValidatedAPIKey("");
    }
  };

  const handleUpdate = async () => {
    if (!apiKeyIsValidated) return;
    try {
      const result = await updateCredential.mutateAsync({
        runtimeId,
        data: { api_key: trimmedAPIKey },
      });
      setAPIKey("");
      setValidatedAPIKey("");
      validateCredential.reset();
      toast.success(
        result.invalidated_sandbox_count
          ? t(($) => $.detail.asb_credential.updated_with_invalidated, {
              count: result.invalidated_sandbox_count,
            })
          : t(($) => $.detail.asb_credential.updated),
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.detail.asb_credential.update_failed),
      );
    }
  };

  return (
    <div className="rounded-lg border">
      <div className="flex items-center gap-1.5 border-b px-4 py-2.5">
        <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-caption font-semibold">
          {t(($) => $.detail.asb_credential.title)}
        </span>
      </div>
      <div className="space-y-3 p-4">
        {credential.isPending ? (
          <div className="flex items-center gap-2 text-caption text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            {t(($) => $.detail.asb_credential.loading)}
          </div>
        ) : credential.isError ? (
          <div className="space-y-2">
            <p className="text-caption text-destructive">
              {credential.error instanceof Error && credential.error.message
                ? credential.error.message
                : t(($) => $.detail.asb_credential.load_failed)}
            </p>
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-8"
              onClick={() => credential.refetch()}
            >
              <RefreshCw className="h-3.5 w-3.5" />
              {t(($) => $.detail.asb_credential.retry)}
            </Button>
          </div>
        ) : credential.data ? (
          <>
            <div className="space-y-1.5">
              <div className="text-micro uppercase tracking-wide text-muted-foreground">
                {t(($) => $.detail.asb_credential.configured_key)}
              </div>
              {credential.data.configured ? (
                <code className="block rounded-md border bg-muted/30 px-3 py-2 text-caption">
                  ••••••••{credential.data.api_key_hint}
                </code>
              ) : (
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.detail.asb_credential.not_configured)}
                </p>
              )}
            </div>
            <div className="space-y-2 border-t pt-3">
              <div className="text-micro uppercase tracking-wide text-muted-foreground">
                {t(($) => $.detail.asb_credential.current_quota)}
              </div>
              <ASBQuotaList quotas={credential.data.quotas} />
            </div>
          </>
        ) : null}

        <div className="space-y-2 border-t pt-3">
          <Label htmlFor={`asb-api-key-${runtimeId}`} className="text-caption">
            {t(($) => $.detail.asb_credential.replace_label)}
          </Label>
          <Input
            id={`asb-api-key-${runtimeId}`}
            type="password"
            autoComplete="off"
            value={apiKey}
            placeholder={t(($) => $.detail.asb_credential.replace_placeholder)}
            onChange={(event) => {
              setAPIKey(event.target.value);
              setValidatedAPIKey("");
              validateCredential.reset();
            }}
          />
          <p className="text-caption text-muted-foreground">
            {t(($) => $.detail.asb_credential.warning)}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-8"
              disabled={!trimmedAPIKey || validateCredential.isPending}
              onClick={handleValidate}
            >
              {validateCredential.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <ShieldCheck className="h-3.5 w-3.5" />
              )}
              {validateCredential.isPending
                ? t(($) => $.fc_e2b_runtime.api_key_validating)
                : t(($) => $.fc_e2b_runtime.api_key_validate)}
            </Button>
            <Button
              type="button"
              size="sm"
              className="h-8"
              disabled={!apiKeyIsValidated || updateCredential.isPending}
              onClick={handleUpdate}
            >
              {updateCredential.isPending && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              )}
              {updateCredential.isPending
                ? t(($) => $.detail.asb_credential.saving)
                : t(($) => $.detail.asb_credential.save)}
            </Button>
          </div>
          {validateCredential.isError && (
            <p className="text-caption text-destructive">
              {validateCredential.error instanceof Error &&
              validateCredential.error.message
                ? validateCredential.error.message
                : t(($) => $.fc_e2b_runtime.api_key_validation_failed)}
            </p>
          )}
          {apiKeyIsValidated && validateCredential.data && (
            <div className="space-y-2 rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3">
              <p className="flex items-center gap-1.5 text-caption font-medium text-emerald-700 dark:text-emerald-300">
                <Check className="h-3.5 w-3.5" />
                {t(($) => $.fc_e2b_runtime.api_key_valid)}
              </p>
              <ASBQuotaList quotas={validateCredential.data.quotas} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
