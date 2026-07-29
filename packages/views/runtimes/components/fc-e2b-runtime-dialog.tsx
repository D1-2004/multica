"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Check, Cloud, Loader2, Search } from "lucide-react";
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
}: {
  onClose: () => void;
  canPublish: boolean;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const createRuntime = useCreateCloudSandboxRuntime(wsId);
  const templatesQuery = useFCE2BTemplates(wsId);
  const [sandboxBackend, setSandboxBackend] =
    useState<SandboxBackend>("aliyun_fc");
  const fcStableChannelQuery = useCloudSandboxStableChannel("aliyun_fc");
  const asbStableChannelQuery = useCloudSandboxStableChannel("asb");
  const stableChannelQuery =
    sandboxBackend === "asb" ? asbStableChannelQuery : fcStableChannelQuery;
  const templates = (templatesQuery.data ?? []).filter(isReadyFCE2BTemplate);
  const [templateChannel, setTemplateChannel] = useState<"stable" | "candidate">(
    "stable",
  );
  const [query, setQuery] = useState("");
  const [selectedTemplate, setSelectedTemplate] = useState<FCE2BTemplate | null>(null);
  const [provider, setProvider] = useState<FCE2BRuntimeProvider>("hermes");
  const [name, setName] = useState("");
  const [visibility, setVisibility] = useState<RuntimeVisibility>("private");
  const [artifactRef, setArtifactRef] = useState("");
  const [artifactBuildId, setArtifactBuildId] = useState("");
  const [artifactAlias, setArtifactAlias] = useState("");
  const [artifactDigest, setArtifactDigest] = useState("");
  const availableProviders =
    sandboxBackend === "asb" || templateChannel === "stable"
      ? [...FC_E2B_RUNTIME_PROVIDERS]
      : selectedTemplate
        ? FC_E2B_RUNTIME_PROVIDERS.filter((candidate) =>
            selectedTemplate.providers.includes(candidate),
          )
        : [];

  const pickBackend = (nextBackend: SandboxBackend) => {
    setSandboxBackend(nextBackend);
    setSelectedTemplate(null);
    setQuery("");
    setProvider("hermes");
    setName("");
  };

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

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (
      sandboxBackend === "aliyun_fc" &&
      templateChannel === "candidate" &&
      !selectedTemplate
    ) {
      return;
    }
    if (
      sandboxBackend === "asb" &&
      templateChannel === "candidate" &&
      (!artifactRef.trim() ||
        !artifactBuildId.trim() ||
        !artifactDigest.trim())
    ) {
      return;
    }
    try {
      await createRuntime.mutateAsync({
        sandbox_backend: sandboxBackend,
        ...(sandboxBackend === "aliyun_fc" &&
        templateChannel === "candidate"
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
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_runtime.title)}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {t(($) => $.fc_e2b_runtime.description)}
          </DialogDescription>
        </DialogHeader>

        <form
          id="fc-e2b-runtime-form"
          onSubmit={handleSubmit}
          className="space-y-4"
        >
          <div className="space-y-1.5">
            <Label className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.backend)}
            </Label>
            <Select
              value={sandboxBackend}
              onValueChange={(value) => pickBackend(value as SandboxBackend)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="aliyun_fc">
                  {t(($) => $.fc_e2b_runtime.backend_aliyun_fc)}
                </SelectItem>
                <SelectItem value="asb">
                  {t(($) => $.fc_e2b_runtime.backend_asb)}
                </SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              {sandboxBackend === "asb"
                ? t(($) => $.fc_e2b_runtime.backend_asb_hint)
                : t(($) => $.fc_e2b_runtime.backend_aliyun_fc_hint)}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.channel)}
            </Label>
            <Select
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
            <p className="text-xs text-muted-foreground">
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
          <div className="space-y-2">
            <Label htmlFor="fc-e2b-template-search" className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.template)}
            </Label>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                id="fc-e2b-template-search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t(($) => $.fc_e2b_runtime.template_search_placeholder)}
                className="pl-8"
              />
            </div>
            <div className="max-h-48 overflow-y-auto rounded-md border">
              {templatesQuery.isLoading && (
                <div className="flex items-center gap-2 p-3 text-xs text-muted-foreground">
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  {t(($) => $.fc_e2b_runtime.templates_loading)}
                </div>
              )}
              {templatesQuery.isError && (
                <div className="p-3 text-xs text-destructive">
                  {templatesQuery.error instanceof Error
                    ? templatesQuery.error.message
                    : t(($) => $.fc_e2b_runtime.templates_failed)}
                </div>
              )}
              {!templatesQuery.isLoading &&
                !templatesQuery.isError &&
                filteredTemplates.length === 0 && (
                  <div className="p-3 text-xs text-muted-foreground">
                    {t(($) => $.fc_e2b_runtime.templates_empty)}
                  </div>
                )}
              {filteredTemplates.map((template) => {
                const selected =
                  selectedTemplate?.template === template.template &&
                  selectedTemplate?.id === template.id;
                const displayName = templateDisplayName(template);
                const identifier = templateIdentifier(template);
                const updatedAt = formatTemplateUpdatedAt(template.updated_at);
                return (
                  <button
                    key={`${template.template}:${template.id ?? ""}:${template.name ?? ""}`}
                    type="button"
                    onClick={() => pickTemplate(template)}
                    className="flex w-full items-start justify-between gap-3 border-b p-3 text-left text-xs last:border-b-0 hover:bg-muted/50"
                  >
                    <span className="min-w-0 space-y-1">
                      <span className="block truncate font-medium">
                        {displayName}
                      </span>
                      {identifier && identifier !== displayName && (
                        <span className="block truncate text-muted-foreground">
                          {identifier}
                        </span>
                      )}
                      {(updatedAt || template.status) && (
                        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-muted-foreground">
                          {updatedAt && (
                            <span className="truncate">
                              {t(($) => $.fc_e2b_runtime.template_updated, {
                                time: updatedAt,
                              })}
                            </span>
                          )}
                          {template.status && <span>{template.status}</span>}
                        </span>
                      )}
                      <span className="block truncate text-muted-foreground">
                        {template.providers
                          .filter((item) =>
                            (FC_E2B_RUNTIME_PROVIDERS as readonly string[]).includes(
                              item,
                            ),
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
                <Label htmlFor="asb-artifact-ref" className="text-xs">
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
                <Label htmlFor="asb-artifact-build-id" className="text-xs">
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
                <Label htmlFor="asb-artifact-digest" className="text-xs">
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
                <Label htmlFor="asb-artifact-alias" className="text-xs">
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
            <Label className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.provider)}
            </Label>
            <Select
              value={provider}
              onValueChange={(value) => pickProvider(value as FCE2BRuntimeProvider)}
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
            <p className="text-xs text-muted-foreground">
              {t(($) => $.fc_e2b_runtime.provider_hint)}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="fc-e2b-runtime-name" className="text-xs">
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
            <Label className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.visibility)}
            </Label>
            <Select
              value={visibility}
              onValueChange={(value) => setVisibility(value as RuntimeVisibility)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="private">
                  {t(($) => $.detail.visibility_label.private)}
                </SelectItem>
                <SelectItem value="public">
                  {t(($) => $.detail.visibility_label.public)}
                </SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
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
