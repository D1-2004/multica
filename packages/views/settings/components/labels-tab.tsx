"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ChevronDown,
  MoreHorizontal,
  Pencil,
  Plus,
  Search,
  Tag,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  labelListOptions,
  useCreateLabel,
  useDeleteLabel,
  useUpdateLabel,
} from "@multica/core/labels";
import type { Label, LabelResourceType } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Label as FieldLabel } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { ColorPicker, COLOR_PICKER_PRESETS } from "../../common/color-picker";
import { useT } from "../../i18n";
import { useRowLink } from "../../navigation";
import { formatTokens, formatUsd } from "../../runtimes/utils";
import { SettingsTab } from "./settings-layout";

/**
 * Label scopes this settings tab manages. Narrower than `LabelResourceType`:
 * the backend still models agent labels, but the product no longer exposes
 * any way to create, apply, or view them, so they are not manageable here.
 */
type LabelScope = Extract<LabelResourceType, "issue" | "skill">;

const RESOURCE_TYPES: LabelScope[] = ["issue", "skill"];

export type LabelListSort =
  | "updated"
  | "cost_desc"
  | "cost_asc"
  | "tokens_desc"
  | "tokens_asc";

const COST_USD_TICKS_PER_USD = 10_000_000_000;

interface LabelDraft {
  name: string;
  description: string;
  color: string;
}

const EMPTY_DRAFT: LabelDraft = {
  name: "",
  description: "",
  color: COLOR_PICKER_PRESETS[6],
};

export function LabelsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const rowLink = useRowLink();

  const [resourceType, setResourceType] = useState<LabelScope>("issue");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<LabelListSort>("updated");
  const [editing, setEditing] = useState<Label | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Label | null>(null);

  const { data: labels = [], isLoading } = useQuery(
    labelListOptions(wsId, resourceType, {
      includeUsage: resourceType === "issue",
    }),
  );
  const filteredLabels = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    const filtered = normalized
      ? labels.filter(
      (label) =>
        label.name.toLowerCase().includes(normalized) ||
        (label.description ?? "").toLowerCase().includes(normalized),
        )
      : [...labels];
    if (resourceType !== "issue") return filtered;
    return filtered.toSorted((a, b) => compareIssueLabels(a, b, sort));
  }, [labels, query, resourceType, sort]);

  const scopeLabel = t(($) => $.labels.scopes[resourceType]);

  return (
    <SettingsTab
      title={t(($) => $.labels.title)}
      description={t(($) => $.labels.description)}
    >
      <div className="space-y-5">
        <div className="flex flex-wrap items-center gap-2 border-b border-surface-border pb-3">
          {RESOURCE_TYPES.map((type) => (
            <Button
              key={type}
              type="button"
              size="sm"
              variant={resourceType === type ? "secondary" : "ghost"}
              className={cn(
                "gap-2",
                resourceType === type && "bg-surface-selected text-surface-selected-foreground",
              )}
              onClick={() => {
                setResourceType(type);
                setQuery("");
                setSort("updated");
              }}
            >
              <Tag className="size-3.5" />
              {t(($) => $.labels.scopes[type])}
              <span className="text-caption tabular-nums text-muted-foreground">
                {type === resourceType ? labels.length : ""}
              </span>
            </Button>
          ))}
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="relative w-full sm:max-w-sm">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.labels.search_placeholder)}
              className="pl-9"
            />
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {resourceType === "issue" ? (
              <LabelUsageSort value={sort} onChange={setSort} />
            ) : null}
            <Button className="gap-2" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" />
              {t(($) => $.labels.new_label)}
            </Button>
          </div>
        </div>

        <div className="overflow-hidden rounded-lg border border-surface-border bg-card">
          {resourceType === "issue" ? (
            <div className="hidden grid-cols-[minmax(9rem,1fr)_minmax(9rem,1.1fr)_5rem_6rem_5.5rem_6rem_2rem] gap-3 border-b border-surface-border bg-muted/20 px-4 py-2.5 text-caption font-medium text-muted-foreground lg:grid">
              <span>{t(($) => $.labels.columns.name)}</span>
              <span>{t(($) => $.labels.columns.description)}</span>
              <span>{t(($) => $.labels.columns.tasks)}</span>
              <span className="text-right">{t(($) => $.labels.columns.tokens)}</span>
              <span className="text-right">{t(($) => $.labels.columns.cost)}</span>
              <span>{t(($) => $.labels.columns.updated)}</span>
              <span />
            </div>
          ) : (
            <div className="hidden grid-cols-[minmax(11rem,1fr)_minmax(12rem,1.4fr)_6rem_7rem_2rem] gap-4 border-b border-surface-border bg-muted/20 px-4 py-2.5 text-caption font-medium text-muted-foreground md:grid">
              <span>{t(($) => $.labels.columns.name)}</span>
              <span>{t(($) => $.labels.columns.description)}</span>
              <span>{t(($) => $.labels.columns.usage)}</span>
              <span>{t(($) => $.labels.columns.updated)}</span>
              <span />
            </div>
          )}

          {isLoading ? (
            <div className="px-4 py-12 text-center text-body text-muted-foreground">
              {t(($) => $.labels.loading)}
            </div>
          ) : filteredLabels.length === 0 ? (
            <div className="px-4 py-12 text-center">
              <Tag className="mx-auto size-6 text-faint-foreground" />
              <p className="mt-3 text-body font-medium">
                {query
                  ? t(($) => $.labels.no_results)
                  : t(($) => $.labels.empty, { scope: scopeLabel })}
              </p>
            </div>
          ) : (
            <div className="divide-y divide-surface-border">
              {filteredLabels.map((label) => {
                const isIssue = resourceType === "issue";
                return (
                <div
                  key={label.id}
                  className={cn(
                    "grid gap-2 px-4 py-3 transition-colors",
                    isIssue
                      ? "cursor-pointer hover:bg-surface-hover lg:grid-cols-[minmax(9rem,1fr)_minmax(9rem,1.1fr)_5rem_6rem_5.5rem_6rem_2rem] lg:items-center lg:gap-3"
                      : "md:grid-cols-[minmax(11rem,1fr)_minmax(12rem,1.4fr)_6rem_7rem_2rem] md:items-center md:gap-4",
                  )}
                  {...(isIssue
                    ? rowLink(paths.labelUsage(label.id), label.name)
                    : {})}
                >
                  <div className="flex min-w-0 items-center gap-3">
                    <span
                      className="size-2.5 shrink-0 rounded-full"
                      style={{ backgroundColor: label.color }}
                    />
                    <span className="truncate text-body font-medium">{label.name}</span>
                  </div>
                  <p className="min-w-0 truncate text-caption text-muted-foreground md:text-body">
                    {label.description || "—"}
                  </p>
                  {isIssue ? (
                    <>
                      <span className="text-caption tabular-nums text-muted-foreground">
                        {label.usage_summary
                          ? label.usage_summary.task_count.toLocaleString()
                          : "—"}
                      </span>
                      <span className="text-right text-caption tabular-nums text-muted-foreground">
                        {label.usage_summary
                          ? formatTokens(label.usage_summary.total_tokens)
                          : "—"}
                      </span>
                      <LabelCost summary={label.usage_summary} />
                    </>
                  ) : (
                    <span className="text-caption tabular-nums text-muted-foreground md:text-body">
                      {t(($) => $.labels.usage_count, { count: label.usage_count ?? 0 })}
                    </span>
                  )}
                  <span className="text-caption text-muted-foreground">
                    {new Date(label.updated_at).toLocaleDateString()}
                  </span>
                  <div
                    onClick={(event) => event.stopPropagation()}
                    onAuxClick={(event) => event.stopPropagation()}
                  >
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t(($) => $.labels.actions.open, { name: label.name })}
                          >
                            <MoreHorizontal className="size-4" />
                          </Button>
                        }
                      />
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => setEditing(label)}>
                          <Pencil className="size-4" />
                          {t(($) => $.labels.actions.edit)}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => setPendingDelete(label)}
                        >
                          <Trash2 className="size-4" />
                          {t(($) => $.labels.actions.delete)}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </div>
                );
              })}
            </div>
          )}
        </div>
      </div>

      <LabelEditorDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        resourceType={resourceType}
      />
      <LabelEditorDialog
        open={Boolean(editing)}
        onOpenChange={(open) => !open && setEditing(null)}
        resourceType={resourceType}
        label={editing}
      />
      <DeleteLabelDialog
        label={pendingDelete}
        onClose={() => setPendingDelete(null)}
      />
    </SettingsTab>
  );
}

function LabelUsageSort({
  value,
  onChange,
}: {
  value: LabelListSort;
  onChange: (value: LabelListSort) => void;
}) {
  const { t } = useT("settings");
  const labels: Record<LabelListSort, string> = {
    updated: t(($) => $.labels.sort.updated),
    cost_desc: t(($) => $.labels.sort.cost_high),
    cost_asc: t(($) => $.labels.sort.cost_low),
    tokens_desc: t(($) => $.labels.sort.tokens_high),
    tokens_asc: t(($) => $.labels.sort.tokens_low),
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button variant="outline" size="sm" className="gap-1 px-2.5">
            {labels[value]}
            <ChevronDown className="size-3 text-muted-foreground" />
          </Button>
        }
      />
      <DropdownMenuContent align="end" className="min-w-44">
        <DropdownMenuRadioGroup
          value={value}
          onValueChange={(next) => onChange(next as LabelListSort)}
        >
          {(Object.keys(labels) as LabelListSort[]).map((option) => (
            <DropdownMenuRadioItem key={option} value={option}>
              {labels[option]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function LabelCost({ summary }: { summary: Label["usage_summary"] }) {
  const { t } = useT("settings");
  if (!summary) {
    return (
      <span className="text-right text-caption text-muted-foreground">—</span>
    );
  }
  const partial =
    summary.unpriced_task_count > 0 || summary.uncosted_tokens > 0;
  const value =
    partial && summary.total_cost_usd_ticks === 0
      ? "—"
      : formatUsd(summary.total_cost_usd_ticks / COST_USD_TICKS_PER_USD);
  return (
    <span
      className="text-right text-caption tabular-nums text-muted-foreground"
      title={
        partial
          ? t(($) => $.labels.cost_partial, {
              count: summary.unpriced_task_count,
              tokens: formatTokens(summary.uncosted_tokens),
            })
          : undefined
      }
    >
      {value}
      {partial ? <AlertTriangle className="ml-1 inline size-3 text-warning" /> : null}
    </span>
  );
}

export function compareIssueLabels(
  a: Label,
  b: Label,
  sort: LabelListSort,
): number {
  if (sort === "updated") {
    return Date.parse(b.updated_at) - Date.parse(a.updated_at);
  }
  const aSummary = a.usage_summary;
  const bSummary = b.usage_summary;
  if (Boolean(aSummary) !== Boolean(bSummary)) return aSummary ? -1 : 1;
  if (!aSummary || !bSummary) return a.name.localeCompare(b.name);
  if (sort.startsWith("cost")) {
    const aComplete =
      aSummary.unpriced_task_count === 0 && aSummary.uncosted_tokens === 0;
    const bComplete =
      bSummary.unpriced_task_count === 0 && bSummary.uncosted_tokens === 0;
    if (aComplete !== bComplete) return aComplete ? -1 : 1;
  }
  const direction = sort.endsWith("_asc") ? 1 : -1;
  const metric = sort.startsWith("cost")
    ? (label: Label) => label.usage_summary!.total_cost_usd_ticks
    : (label: Label) => label.usage_summary!.total_tokens;
  return (metric(a) - metric(b)) * direction || a.name.localeCompare(b.name);
}

function LabelEditorDialog({
  open,
  onOpenChange,
  resourceType,
  label,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  resourceType: LabelScope;
  label?: Label | null;
}) {
  const { t } = useT("settings");
  const create = useCreateLabel();
  const update = useUpdateLabel();
  const [draft, setDraft] = useState<LabelDraft>(EMPTY_DRAFT);

  useEffect(() => {
    if (!open) return;
    setDraft(
      label
        ? {
            name: label.name,
            description: label.description ?? "",
            color: label.color,
          }
        : EMPTY_DRAFT,
    );
  }, [label, open]);

  const submit = () => {
    const name = draft.name.trim();
    if (!name) return;
    if (label) {
      update.mutate(
        {
          id: label.id,
          resource_type: label.resource_type ?? resourceType,
          name,
          description: draft.description.trim(),
          color: draft.color,
        },
        {
          onSuccess: () => onOpenChange(false),
          onError: (error) =>
            toast.error(error instanceof Error ? error.message : t(($) => $.labels.save_failed)),
        },
      );
      return;
    }
    create.mutate(
      {
        resource_type: resourceType,
        name,
        description: draft.description.trim(),
        color: draft.color,
      },
      {
        onSuccess: () => onOpenChange(false),
        onError: (error) =>
          toast.error(error instanceof Error ? error.message : t(($) => $.labels.save_failed)),
      },
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {label ? t(($) => $.labels.editor.edit_title) : t(($) => $.labels.editor.create_title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.labels.editor.scope_hint, {
              scope: t(($) => $.labels.scopes[resourceType]),
            })}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-5 py-2">
          <div className="space-y-2">
            <FieldLabel htmlFor="label-name">{t(($) => $.labels.editor.name)}</FieldLabel>
            <Input
              id="label-name"
              autoFocus
              maxLength={32}
              value={draft.name}
              onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
              placeholder={t(($) => $.labels.editor.name_placeholder)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel htmlFor="label-description">
              {t(($) => $.labels.editor.description)}
            </FieldLabel>
            <Textarea
              id="label-description"
              rows={3}
              value={draft.description}
              onChange={(event) =>
                setDraft((current) => ({ ...current, description: event.target.value }))
              }
              placeholder={t(($) => $.labels.editor.description_placeholder)}
            />
          </div>
          <div className="space-y-2">
            <FieldLabel>{t(($) => $.labels.editor.color)}</FieldLabel>
            <ColorPicker
              value={draft.color}
              onChange={(color) => setDraft((current) => ({ ...current, color }))}
              trigger={
                <button
                  type="button"
                  aria-label={t(($) => $.labels.editor.color)}
                  className="flex h-9 items-center gap-2.5 rounded-md border border-surface-border px-2.5 transition-colors hover:bg-surface-hover"
                >
                  <span
                    className="size-5 rounded-full"
                    style={{ backgroundColor: draft.color }}
                  />
                  <span className="font-mono text-caption uppercase text-muted-foreground">
                    {draft.color}
                  </span>
                </button>
              }
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t(($) => $.labels.editor.cancel)}
          </Button>
          <Button
            onClick={submit}
            disabled={!draft.name.trim() || create.isPending || update.isPending}
          >
            {create.isPending || update.isPending
              ? t(($) => $.labels.editor.saving)
              : t(($) => $.labels.editor.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeleteLabelDialog({
  label,
  onClose,
}: {
  label: Label | null;
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const remove = useDeleteLabel();
  return (
    <AlertDialog open={Boolean(label)} onOpenChange={(open) => !open && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.labels.delete_dialog.title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.labels.delete_dialog.description, {
              name: label?.name ?? "",
              count: label?.usage_count ?? 0,
            })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t(($) => $.labels.delete_dialog.cancel)}</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              if (!label) return;
              remove.mutate(
                { id: label.id, resource_type: label.resource_type ?? "issue" },
                {
                  onSuccess: onClose,
                  onError: (error) =>
                    toast.error(
                      error instanceof Error
                        ? error.message
                        : t(($) => $.labels.delete_dialog.failed),
                    ),
                },
              );
            }}
          >
            {t(($) => $.labels.delete_dialog.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
