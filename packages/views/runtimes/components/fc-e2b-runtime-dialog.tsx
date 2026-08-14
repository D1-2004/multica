"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Check, Cloud, Loader2, Search, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import type { RuntimeVisibility } from "@multica/core/types/agent";
import {
  FC_E2B_RUNTIME_PROVIDERS,
  fcE2BProviderForTemplate,
  isReadyFCE2BTemplate,
  type FCE2BRuntimeProvider,
  type FCE2BTemplate,
  type SandboxBackend,
  useCloudSandboxStableChannel,
  useCreateCloudSandboxRuntime,
  useFCE2BTemplates,
  useValidateASBRuntimeCredential,
} from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

const PROVIDER_LABELS: Record<FCE2BRuntimeProvider, string> = {
  hermes: "Hermes",
  opencode: "OpenCode",
  pi: "Pi",
};

const ASB_API_KEY_DOCS_URL =
  "https://sandbox.aone.alibaba-inc.com/docs/tenant-ops.html#api-keys-%E7%AE%A1%E7%90%86";

// Mirrors the server-side default name: the provider is prefixed when the
// template name does not mention it, so two runtimes created from the same
// dual-CLI template get distinct defaults.
function templateRuntimeName(
  template: FCE2BTemplate,
  provider: FCE2BRuntimeProvider,
): string {
  const providerPart = provider[0]!.toUpperCase() + provider.slice(1);
  const raw = (templateDisplayName(template) || providerPart)
    .replace(/^multica-fc-/i, "")
    .replace(/-runtime$/i, "")
    .replace(/-template$/i, "");
  const parts = raw.split(/[-_.\s]+/).filter(Boolean);
  if (parts.length === 0) return `FC-${providerPart}`;
  const clean = parts.map((part) => part[0]!.toUpperCase() + part.slice(1));
  if (!parts.some((part) => part.toLowerCase() === provider)) {
    clean.unshift(providerPart);
  }
  return `FC-${clean.join("-")}`;
}

function templateDisplayName(template: FCE2BTemplate): string {
  return template.name || template.template || template.id || "";
}

function templateIdentifier(template: FCE2BTemplate): string {
  return template.id || template.template || template.name || "";
}

function formatTemplateUpdatedAt(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}

export function FCE2BRuntimeDialog({
  onClose,
  canPublish,
  canCreatePublic,
  sandboxBackend,
}: {
  onClose: () => void;
  canPublish: boolean;
  canCreatePublic: boolean;
  sandboxBackend: SandboxBackend;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const createRuntime = useCreateCloudSandboxRuntime(wsId);
  const validateASBCredential = useValidateASBRuntimeCredential();
  const templatesQuery = useFCE2BTemplates(wsId, canPublish);
  const stableChannelQuery = useCloudSandboxStableChannel(sandboxBackend);
  const templates = (templatesQuery.data ?? []).filter(isReadyFCE2BTemplate);
  const [templateChannel, setTemplateChannel] = useState<
    "stable" | "candidate"
  >("stable");
  const [query, setQuery] = useState("");
  const [selectedTemplate, setSelectedTemplate] =
    useState<FCE2BTemplate | null>(null);
  const [provider, setProvider] = useState<FCE2BRuntimeProvider>("hermes");
  const [name, setName] = useState("");
  const [visibility, setVisibility] = useState<RuntimeVisibility>("private");
  const [artifactRef, setArtifactRef] = useState("");
  const [artifactBuildId, setArtifactBuildId] = useState("");
  const [artifactAlias, setArtifactAlias] = useState("");
  const [artifactDigest, setArtifactDigest] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [validatedAPIKey, setValidatedAPIKey] = useState("");
  const apiKeyIsValidated =
    Boolean(apiKey.trim()) &&
    validatedAPIKey === apiKey.trim() &&
    validateASBCredential.data?.valid === true;
  const availableProviders =
    sandboxBackend === "asb" || templateChannel === "stable"
      ? [...FC_E2B_RUNTIME_PROVIDERS]
      : selectedTemplate
        ? FC_E2B_RUNTIME_PROVIDERS.filter((candidate) =>
            selectedTemplate.providers.includes(candidate),
          )
        : [];

  const filteredTemplates = templates.filter((template) => {
    const haystack = [
      template.name,
      template.id,
      template.template,
      template.status,
      template.updated_at,
      ...template.providers,
      ...template.capabilities,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });

  const pickTemplate = (template: FCE2BTemplate) => {
    const nextProvider = fcE2BProviderForTemplate(template);
    if (!nextProvider) return;
    setSelectedTemplate(template);
    setProvider(nextProvider);
    if (!name.trim()) {
      setName(templateRuntimeName(template, nextProvider));
    }
  };

  const pickProvider = (nextProvider: FCE2BRuntimeProvider) => {
    // Re-seed the name only while it still matches the previous default, so a
    // user-typed name never gets clobbered.
    if (
      selectedTemplate &&
      (!name.trim() || name === templateRuntimeName(selectedTemplate, provider))
    ) {
      setName(templateRuntimeName(selectedTemplate, nextProvider));
    }
    setProvider(nextProvider);
  };

  const handleValidateASBCredential = async () => {
    const nextAPIKey = apiKey.trim();
    if (!nextAPIKey) return;
    setValidatedAPIKey("");
    try {
      const result = await validateASBCredential.mutateAsync({
        api_key: nextAPIKey,
      });
      if (result.valid) setValidatedAPIKey(nextAPIKey);
    } catch {
      setValidatedAPIKey("");
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (
      sandboxBackend === "aliyun_fc" &&
      templateChannel === "candidate" &&
      !selectedTemplate
    ) {
      return;
    }
    if (sandboxBackend === "asb" && !apiKeyIsValidated) {
      return;
    }
    if (
      sandboxBackend === "asb" &&
      templateChannel === "candidate" &&
      (!artifactRef.trim() || !artifactBuildId.trim() || !artifactDigest.trim())
    ) {
      return;
    }
    try {
      await createRuntime.mutateAsync({
        sandbox_backend: sandboxBackend,
        ...(sandboxBackend === "asb" ? { api_key: apiKey.trim() } : {}),
        ...(sandboxBackend === "aliyun_fc" && templateChannel === "candidate"
          ? { template_id: selectedTemplate!.id }
          : {}),
        ...(sandboxBackend === "asb" && templateChannel === "candidate"
          ? {
              artifact_ref: artifactRef.trim(),
              artifact_build_id: artifactBuildId.trim(),
              artifact_alias: artifactAlias.trim() || undefined,
              artifact_digest: artifactDigest.trim(),
            }
          : {}),
        artifact_channel: templateChannel,
        name:
          name.trim() ||
          (sandboxBackend === "asb"
            ? `ASB-${PROVIDER_LABELS[provider]}`
            : selectedTemplate
              ? templateRuntimeName(selectedTemplate, provider)
              : `FC-${PROVIDER_LABELS[provider]}-Stable`),
        provider,
        visibility,
      });
      toast.success(t(($) => $.fc_e2b_runtime.toast_created));
      onClose();
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.fc_e2b_runtime.toast_create_failed),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[90vh] min-w-0 overflow-x-hidden overflow-y-auto sm:max-w-[min(48rem,calc(100vw-2rem))]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-title-sm">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_runtime.title)}
          </DialogTitle>
          <DialogDescription className="text-caption">
            {t(($) => $.fc_e2b_runtime.description)}
          </DialogDescription>
        </DialogHeader>

        <form
          id="fc-e2b-runtime-form"
          onSubmit={handleSubmit}
          className="min-w-0 space-y-4"
        >
          <p className="rounded-md border bg-muted/30 px-3 py-2 text-caption text-muted-foreground">
            {sandboxBackend === "asb"
              ? t(($) => $.fc_e2b_runtime.backend_asb_hint)
              : t(($) => $.fc_e2b_runtime.backend_aliyun_fc_hint)}
          </p>

          {sandboxBackend === "asb" && (
            <div className="space-y-1.5">
              <Label htmlFor="asb-api-key" className="text-caption">
                {t(($) => $.fc_e2b_runtime.fields.api_key)}
              </Label>
              <Input
                id="asb-api-key"
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(event) => {
                  setAPIKey(event.target.value);
                  setValidatedAPIKey("");
                  validateASBCredential.reset();
                }}
                required
              />
              <p className="text-caption text-muted-foreground">
                {t(($) => $.fc_e2b_runtime.api_key_hint)}
              </p>
              <a
                href={ASB_API_KEY_DOCS_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex text-xs text-primary underline underline-offset-4 hover:text-primary/80"
              >
                {t(($) => $.fc_e2b_runtime.api_key_docs)}
              </a>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-2"
                disabled={!apiKey.trim() || validateASBCredential.isPending}
                onClick={handleValidateASBCredential}
              >
                {validateASBCredential.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <ShieldCheck className="h-3.5 w-3.5" />
                )}
                {validateASBCredential.isPending
                  ? t(($) => $.fc_e2b_runtime.api_key_validating)
                  : t(($) => $.fc_e2b_runtime.api_key_validate)}
              </Button>
              {validateASBCredential.isError && (
                <p className="text-caption text-destructive">
                  {validateASBCredential.error instanceof Error
                    ? validateASBCredential.error.message
                    : t(($) => $.fc_e2b_runtime.api_key_validation_failed)}
                </p>
              )}
              {apiKeyIsValidated && validateASBCredential.data && (
                <div className="mt-2 space-y-2 rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3">
                  <p className="flex items-center gap-1.5 text-caption font-medium text-emerald-700 dark:text-emerald-300">
                    <Check className="h-3.5 w-3.5" />
                    {t(($) => $.fc_e2b_runtime.api_key_valid)}
                  </p>
                  {validateASBCredential.data.quotas.length === 0 ? (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.fc_e2b_runtime.quota_empty)}
                    </p>
                  ) : (
                    <div className="max-h-36 space-y-2 overflow-y-auto">
                      {validateASBCredential.data.quotas.map((quota) => (
                        <div
                          key={`${quota.network_zone}:${quota.region}`}
                          className="rounded border bg-background/60 px-2.5 py-2 text-caption"
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
                  )}
                </div>
              )}
            </div>
          )}

          <div className="space-y-1.5">
            <Label className="text-caption">
              {t(($) => $.fc_e2b_runtime.fields.channel)}
            </Label>
            <Select
              items={[
                {
                  value: "stable",
                  label: t(($) => $.fc_e2b_runtime.channel_stable),
                },
                ...(canPublish
                  ? [
                      {
                        value: "candidate",
                        label: t(($) => $.fc_e2b_runtime.channel_candidate),
                      },
                    ]
                  : []),
              ]}
              value={templateChannel}
              onValueChange={(value) =>
                setTemplateChannel(value as "stable" | "candidate")
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stable">
                  {t(($) => $.fc_e2b_runtime.channel_stable)}
                </SelectItem>
                {canPublish && (
                  <SelectItem value="candidate">
                    {t(($) => $.fc_e2b_runtime.channel_candidate)}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {templateChannel === "stable"
                ? stableChannelQuery.data?.current
                  ? stableChannelQuery.data.current.artifact_alias ||
                    stableChannelQuery.data.current.template_alias
                  : t(($) => $.fc_e2b_runtime.stable_uninitialized)
                : sandboxBackend === "asb"
                  ? t(($) => $.fc_e2b_runtime.candidate_asb_hint)
                  : t(($) => $.fc_e2b_runtime.candidate_hint)}
            </p>
          </div>

          {sandboxBackend === "aliyun_fc" &&
            templateChannel === "candidate" && (
              <div className="min-w-0 space-y-2">
                <Label htmlFor="fc-e2b-template-search" className="text-caption">
                  {t(($) => $.fc_e2b_runtime.fields.template)}
                </Label>
                <div className="relative">
                  <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
                  <Input
                    id="fc-e2b-template-search"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder={t(
                      ($) => $.fc_e2b_runtime.template_search_placeholder,
                    )}
                    className="pl-8"
                  />
                </div>
                <div className="max-h-48 min-w-0 overflow-y-auto rounded-md border">
                  {templatesQuery.isLoading && (
                    <div className="flex items-center gap-2 p-3 text-caption text-muted-foreground">
                      <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      {t(($) => $.fc_e2b_runtime.templates_loading)}
                    </div>
                  )}
                  {templatesQuery.isError && (
                    <div className="p-3 text-caption text-destructive">
                      {templatesQuery.error instanceof Error
                        ? templatesQuery.error.message
                        : t(($) => $.fc_e2b_runtime.templates_failed)}
                    </div>
                  )}
                  {!templatesQuery.isLoading &&
                    !templatesQuery.isError &&
                    filteredTemplates.length === 0 && (
                      <div className="p-3 text-caption text-muted-foreground">
                        {t(($) => $.fc_e2b_runtime.templates_empty)}
                      </div>
                    )}
                  {filteredTemplates.map((template) => {
                    const selected =
                      selectedTemplate?.template === template.template &&
                      selectedTemplate?.id === template.id;
                    const displayName = templateDisplayName(template);
                    const identifier = templateIdentifier(template);
                    const updatedAt = formatTemplateUpdatedAt(
                      template.updated_at,
                    );
                    return (
                      <button
                        key={`${template.template}:${template.id ?? ""}:${template.name ?? ""}`}
                        type="button"
                        onClick={() => pickTemplate(template)}
                        className="flex w-full min-w-0 items-start justify-between gap-3 border-b p-3 text-left text-caption last:border-b-0 hover:bg-muted/50"
                      >
                        <span className="min-w-0 space-y-1">
                          <span className="block break-all font-medium">
                            {displayName}
                          </span>
                          {identifier && identifier !== displayName && (
                            <span className="block break-all text-muted-foreground">
                              {identifier}
                            </span>
                          )}
                          {(updatedAt || template.status) && (
                            <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-muted-foreground">
                              {updatedAt && (
                                <span>
                                  {t(($) => $.fc_e2b_runtime.template_updated, {
                                    time: updatedAt,
                                  })}
                                </span>
                              )}
                              {template.status && (
                                <span>{template.status}</span>
                              )}
                            </span>
                          )}
                          <span className="block break-words whitespace-normal text-muted-foreground">
                            {template.providers
                              .filter((item) =>
                                (
                                  FC_E2B_RUNTIME_PROVIDERS as readonly string[]
                                ).includes(item),
                              )
                              .map(
                                (item) =>
                                  PROVIDER_LABELS[item as FCE2BRuntimeProvider],
                              )
                              .join(" · ")}
                            {template.capabilities.length > 0
                              ? ` · ${template.capabilities.join(" · ")}`
                              : ""}
                          </span>
                        </span>
                        {selected && <Check className="mt-0.5 h-3.5 w-3.5" />}
                      </button>
                    );
                  })}
                </div>
              </div>
            )}

          {sandboxBackend === "asb" && templateChannel === "candidate" ? (
            <div className="space-y-3 rounded-md border p-3">
              <div className="space-y-1.5">
                <Label htmlFor="asb-artifact-ref" className="text-caption">
                  {t(($) => $.fc_e2b_runtime.fields.artifact_ref)}
                </Label>
                <Input
                  id="asb-artifact-ref"
                  value={artifactRef}
                  onChange={(event) => setArtifactRef(event.target.value)}
                  placeholder="registry/repository@sha256:..."
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="asb-artifact-build-id" className="text-caption">
                  {t(($) => $.fc_e2b_runtime.fields.artifact_build_id)}
                </Label>
                <Input
                  id="asb-artifact-build-id"
                  value={artifactBuildId}
                  onChange={(event) => setArtifactBuildId(event.target.value)}
                  placeholder="asb-abcdef0"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="asb-artifact-digest" className="text-caption">
                  {t(($) => $.fc_e2b_runtime.fields.artifact_digest)}
                </Label>
                <Input
                  id="asb-artifact-digest"
                  value={artifactDigest}
                  onChange={(event) => setArtifactDigest(event.target.value)}
                  placeholder="sha256:..."
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="asb-artifact-alias" className="text-caption">
                  {t(($) => $.fc_e2b_runtime.fields.artifact_alias)}
                </Label>
                <Input
                  id="asb-artifact-alias"
                  value={artifactAlias}
                  onChange={(event) => setArtifactAlias(event.target.value)}
                  placeholder="multica-asb-runtime:build"
                />
              </div>
            </div>
          ) : null}

          <div className="space-y-1.5">
            <Label className="text-caption">
              {t(($) => $.fc_e2b_runtime.fields.provider)}
            </Label>
            <Select
              items={availableProviders.map((option) => ({
                value: option,
                label: PROVIDER_LABELS[option],
              }))}
              value={provider}
              onValueChange={(value) =>
                pickProvider(value as FCE2BRuntimeProvider)
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {availableProviders.map((option) => (
                  <SelectItem key={option} value={option}>
                    {PROVIDER_LABELS[option]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.fc_e2b_runtime.provider_hint)}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="fc-e2b-runtime-name" className="text-caption">
              {t(($) => $.fc_e2b_runtime.fields.name)}
            </Label>
            <Input
              id="fc-e2b-runtime-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={
                sandboxBackend === "asb"
                  ? `ASB-${PROVIDER_LABELS[provider]}`
                  : selectedTemplate
                    ? templateRuntimeName(selectedTemplate, provider)
                    : templateChannel === "stable"
                      ? `FC-${PROVIDER_LABELS[provider]}-Stable`
                      : t(($) => $.fc_e2b_runtime.name_placeholder)
              }
            />
          </div>

          <div className="space-y-1.5">
            <Label className="text-caption">
              {t(($) => $.fc_e2b_runtime.fields.visibility)}
            </Label>
            <Select
              items={[
                {
                  value: "private",
                  label: t(($) => $.detail.visibility_label.private),
                },
                ...(canCreatePublic
                  ? [
                      {
                        value: "public",
                        label: t(($) => $.detail.visibility_label.public),
                      },
                    ]
                  : []),
              ]}
              value={visibility}
              onValueChange={(value) =>
                setVisibility(value as RuntimeVisibility)
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="private">
                  {t(($) => $.detail.visibility_label.private)}
                </SelectItem>
                {canCreatePublic && (
                  <SelectItem value="public">
                    {t(($) => $.detail.visibility_label.public)}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.fc_e2b_runtime.visibility_hint)}
            </p>
          </div>
        </form>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onClose}
            disabled={createRuntime.isPending}
          >
            {t(($) => $.fc_e2b_runtime.cancel)}
          </Button>
          <Button
            type="submit"
            form="fc-e2b-runtime-form"
            disabled={
              createRuntime.isPending ||
              (sandboxBackend === "asb" && !apiKeyIsValidated) ||
              (templateChannel === "stable"
                ? !stableChannelQuery.data?.current
                : sandboxBackend === "aliyun_fc"
                  ? !selectedTemplate
                  : !artifactRef.trim() ||
                    !artifactBuildId.trim() ||
                    !artifactDigest.trim()) ||
              !availableProviders.includes(provider)
            }
          >
            {createRuntime.isPending && (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            )}
            {createRuntime.isPending
              ? t(($) => $.fc_e2b_runtime.creating)
              : t(($) => $.fc_e2b_runtime.create)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
