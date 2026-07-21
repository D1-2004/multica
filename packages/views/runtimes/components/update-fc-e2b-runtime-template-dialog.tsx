"use client";

import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { AlertTriangle, Check, Cloud, Loader2, Search } from "lucide-react";
import { toast } from "sonner";
import type { AgentRuntime } from "@multica/core/types";
import {
  isReadyFCE2BTemplate,
  parseFCE2BRuntimeMetadata,
  type FCE2BTemplate,
  useFCE2BTemplates,
  useUpdateFCE2BRuntimeTemplate,
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
import { useT } from "../../i18n";

function templateDisplayName(template: FCE2BTemplate): string {
  return template.name || template.template || template.id || "";
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

function providerDisplayName(provider: string): string {
  switch (provider.toLowerCase()) {
    case "hermes":
      return "Hermes";
    case "opencode":
      return "OpenCode";
    case "pi":
      return "Pi";
    default:
      return provider;
  }
}

export function UpdateFCE2BRuntimeTemplateDialog({
  runtime,
  onClose,
}: {
  runtime: AgentRuntime;
  onClose: () => void;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const metadata = parseFCE2BRuntimeMetadata(runtime);
  const templatesQuery = useFCE2BTemplates(wsId);
  const updateTemplate = useUpdateFCE2BRuntimeTemplate(wsId);
  const [query, setQuery] = useState("");
  const [selectedTemplate, setSelectedTemplate] =
    useState<FCE2BTemplate | null>(null);

  const filteredTemplates = useMemo(() => {
    const templates = templatesQuery.data ?? [];
    const normalizedQuery = query.trim().toLowerCase();
    if (!normalizedQuery) return templates;
    return templates.filter((template) =>
      [
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
        .toLowerCase()
        .includes(normalizedQuery),
    );
  }, [query, templatesQuery.data]);

  const isCurrentTemplate = (template: FCE2BTemplate): boolean => {
    const templateId = template.id?.trim();
    const buildId = template.build_id?.trim();
    return Boolean(
      templateId &&
        buildId &&
        metadata?.templateId === templateId &&
        metadata.templateBuildId === buildId,
    );
  };

  const supportsRuntimeProvider = (template: FCE2BTemplate): boolean =>
    template.providers.includes(runtime.provider.trim().toLowerCase());

  const selectedTemplateId = selectedTemplate?.id?.trim() ?? "";
  const canSubmit = Boolean(
    selectedTemplate &&
      selectedTemplateId &&
      isReadyFCE2BTemplate(selectedTemplate) &&
      supportsRuntimeProvider(selectedTemplate) &&
      !isCurrentTemplate(selectedTemplate),
  );
  const currentTemplateName =
    metadata?.templateName ||
    metadata?.template ||
    metadata?.templateId ||
    t(($) => $.detail.cloud_image.unknown_template);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canSubmit) return;
    try {
      await updateTemplate.mutateAsync({
        runtimeId: runtime.id,
        data: { template_id: selectedTemplateId },
      });
      toast.success(t(($) => $.fc_e2b_template_update.toast_updated));
      onClose();
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.fc_e2b_template_update.toast_failed),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_template_update.title)}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {t(($) => $.fc_e2b_template_update.description)}
          </DialogDescription>
        </DialogHeader>

        <form
          id="fc-e2b-template-update-form"
          onSubmit={handleSubmit}
          className="space-y-4"
        >
          <div className="grid grid-cols-1 gap-3 rounded-md border bg-muted/30 p-3 sm:grid-cols-[minmax(0,1fr)_120px]">
            <div className="min-w-0">
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                {t(($) => $.fc_e2b_template_update.current_template)}
              </div>
              <div className="mt-1 truncate font-mono text-xs">
                {currentTemplateName}
              </div>
            </div>
            <div className="min-w-0">
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                {t(($) => $.fc_e2b_template_update.provider)}
              </div>
              <div className="mt-1 truncate text-xs font-medium">
                {providerDisplayName(runtime.provider)}
              </div>
            </div>
          </div>

          <div className="flex items-start gap-2 rounded-md border px-3 py-2.5 text-xs text-muted-foreground">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <p>{t(($) => $.fc_e2b_template_update.cutover_notice)}</p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="fc-e2b-template-update-search" className="text-xs">
              {t(($) => $.fc_e2b_template_update.new_template)}
            </Label>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
              <Input
                id="fc-e2b-template-update-search"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder={t(
                  ($) => $.fc_e2b_runtime.template_search_placeholder,
                )}
                className="pl-8"
              />
            </div>
            <div className="max-h-56 overflow-y-auto rounded-md border">
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
                const templateId = template.id?.trim() ?? "";
                const ready = isReadyFCE2BTemplate(template);
                const compatible = supportsRuntimeProvider(template);
                const current = isCurrentTemplate(template);
                const selectable = ready && compatible && !current;
                const selected = selectedTemplate === template;
                const displayName = templateDisplayName(template);
                const updatedAt = formatTemplateUpdatedAt(template.updated_at);
                const stateLabel = current
                  ? t(($) => $.fc_e2b_template_update.template_current)
                  : !templateId
                    ? t(($) => $.fc_e2b_template_update.template_missing_id)
                    : !ready || !compatible
                      ? t(($) => $.fc_e2b_template_update.template_unavailable)
                      : template.status;
                return (
                  <button
                    key={`${template.template}:${template.id ?? ""}:${template.name ?? ""}`}
                    type="button"
                    disabled={!selectable}
                    onClick={() => setSelectedTemplate(template)}
                    className="flex w-full items-start justify-between gap-3 border-b p-3 text-left text-xs transition-colors last:border-b-0 enabled:hover:bg-muted/50 disabled:cursor-not-allowed disabled:opacity-55"
                  >
                    <span className="min-w-0 space-y-1">
                      <span className="block truncate font-medium">
                        {displayName}
                      </span>
                      {templateId && templateId !== displayName && (
                        <span className="block truncate font-mono text-[11px] text-muted-foreground">
                          {templateId}
                        </span>
                      )}
                      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-muted-foreground">
                        {updatedAt && (
                          <span className="truncate">
                            {t(($) => $.fc_e2b_runtime.template_updated, {
                              time: updatedAt,
                            })}
                          </span>
                        )}
                        {stateLabel && <span>{stateLabel}</span>}
                      </span>
                    </span>
                    {selected && (
                      <Check className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                    )}
                  </button>
                );
              })}
            </div>
          </div>
        </form>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onClose}
            disabled={updateTemplate.isPending}
          >
            {t(($) => $.fc_e2b_template_update.cancel)}
          </Button>
          <Button
            type="submit"
            form="fc-e2b-template-update-form"
            disabled={updateTemplate.isPending || !canSubmit}
          >
            {updateTemplate.isPending && (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            )}
            {updateTemplate.isPending
              ? t(($) => $.fc_e2b_template_update.updating)
              : t(($) => $.fc_e2b_template_update.update)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
