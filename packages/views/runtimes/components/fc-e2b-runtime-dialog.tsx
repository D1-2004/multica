"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Check, Cloud, Loader2, Search } from "lucide-react";
import { toast } from "sonner";
import type { RuntimeVisibility } from "@multica/core/types/agent";
import {
  type FCE2BTemplate,
  useCreateFCE2BRuntime,
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

function templateRuntimeName(template: FCE2BTemplate): string {
  const raw = (template.name || template.template || template.id || "Hermes")
    .replace(/^multica-fc-/i, "")
    .replace(/-runtime$/i, "")
    .replace(/-template$/i, "");
  const parts = raw.split(/[-_.\s]+/).filter(Boolean);
  if (parts.length === 0) return "FC-Hermes";
  return `FC-${parts.map((part) => part[0]!.toUpperCase() + part.slice(1)).join("-")}`;
}

export function FCE2BRuntimeDialog({ onClose }: { onClose: () => void }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const createRuntime = useCreateFCE2BRuntime(wsId);
  const templatesQuery = useFCE2BTemplates(wsId);
  const templates = templatesQuery.data ?? [];
  const [query, setQuery] = useState("");
  const [selectedTemplate, setSelectedTemplate] = useState<FCE2BTemplate | null>(null);
  const [name, setName] = useState("");
  const [visibility, setVisibility] = useState<RuntimeVisibility>("private");

  const filteredTemplates = templates.filter((template) => {
    const haystack = [
      template.name,
      template.id,
      template.template,
      template.status,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return haystack.includes(query.trim().toLowerCase());
  });

  const pickTemplate = (template: FCE2BTemplate) => {
    setSelectedTemplate(template);
    if (!name.trim()) {
      setName(templateRuntimeName(template));
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selectedTemplate) return;
    try {
      await createRuntime.mutateAsync({
        template_id: selectedTemplate.template || selectedTemplate.id || selectedTemplate.name || "",
        name: name.trim() || templateRuntimeName(selectedTemplate),
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
                return (
                  <button
                    key={`${template.template}:${template.id ?? ""}:${template.name ?? ""}`}
                    type="button"
                    onClick={() => pickTemplate(template)}
                    className="flex w-full items-start justify-between gap-3 border-b p-3 text-left text-xs last:border-b-0 hover:bg-muted/50"
                  >
                    <span className="min-w-0 space-y-1">
                      <span className="block truncate font-medium">
                        {template.name || template.template}
                      </span>
                      <span className="block truncate text-muted-foreground">
                        {template.id || template.template}
                      </span>
                      {template.status && (
                        <span className="block text-muted-foreground">
                          {template.status}
                        </span>
                      )}
                    </span>
                    {selected && <Check className="mt-0.5 h-3.5 w-3.5" />}
                  </button>
                );
              })}
            </div>
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
                selectedTemplate
                  ? templateRuntimeName(selectedTemplate)
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
            disabled={createRuntime.isPending || !selectedTemplate}
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
