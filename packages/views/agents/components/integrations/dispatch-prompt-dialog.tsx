"use client";

import { useEffect, useMemo, useState } from "react";
import { Loader2, RotateCcw, Save } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { Agent, DispatchPromptSegment } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";

const SURFACES = ["issue", "chat", "auto"] as const;
type Surface = (typeof SURFACES)[number];

/**
 * Shows the inbound prompt an agent actually receives and lets its owner
 * replace individual segments.
 *
 * The preview is fetched rather than assembled here on purpose: the server
 * composes it with the same function the claim path uses, so what is shown
 * cannot drift from what the agent is told. Reproducing the composition in the
 * client would be a second implementation that silently starts lying.
 */
export function DispatchPromptDialog({
  agent,
  open,
  onOpenChange,
  onSave,
  readOnly = false,
}: {
  agent: Agent;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSave: (updates: {
    dispatch_prompt_overrides?: Record<string, string>;
    dispatch_always_new_issue?: boolean;
  }) => Promise<void>;
  readOnly?: boolean;
}) {
  const { t } = useT("agents");
  const [surface, setSurface] = useState<Surface>("auto");
  const [activeSegment, setActiveSegment] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  const savedOverrides = useMemo(
    () => agent.dispatch_prompt_overrides ?? {},
    [agent.dispatch_prompt_overrides],
  );

  const { data: preview, isLoading } = useQuery({
    queryKey: ["agent-dispatch-prompt-preview", agent.id, surface],
    queryFn: () => api.getAgentDispatchPromptPreview(agent.id, surface),
    enabled: open,
    staleTime: 60 * 1000,
    retry: false,
  });

  const segments = preview?.segments ?? [];
  const selected = segments.find((segment) => segment.id === activeSegment);

  // Seed the editor from the effective text so an owner edits the real content
  // rather than an empty box — dropping the managed safety and delivery clauses
  // by starting blank is the failure mode worth designing against.
  useEffect(() => {
    if (!selected) return;
    setDraft(selected.effective_text);
  }, [selected]);

  useEffect(() => {
    if (!open) setActiveSegment(null);
  }, [open]);

  const commit = async (overrides: Record<string, string>) => {
    setSaving(true);
    try {
      await onSave({ dispatch_prompt_overrides: overrides });
      setActiveSegment(null);
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  const saveSegment = () => {
    if (!selected) return;
    void commit({ ...savedOverrides, [selected.id]: draft });
  };

  const restoreSegment = () => {
    if (!selected) return;
    const next = { ...savedOverrides };
    delete next[selected.id];
    void commit(next);
  };

  const surfaceLabel = (option: Surface) => {
    switch (option) {
      case "issue":
        return t(($) => $.tab_body.dispatch.surface_issue);
      case "chat":
        return t(($) => $.tab_body.dispatch.surface_chat);
      default:
        return t(($) => $.tab_body.dispatch.surface_auto);
    }
  };

  // Explicit lookups rather than dynamic indexing: the typed i18n selector
  // cannot express `$.x[id]`, and an unknown id falls back to the raw id so a
  // segment added server-side still renders instead of blanking the row.
  const segmentLabel = (id: string) => {
    switch (id) {
      case "policy":
        return t(($) => $.tab_body.dispatch.segment_policy);
      case "context":
        return t(($) => $.tab_body.dispatch.segment_context);
      case "reply_formatting":
        return t(($) => $.tab_body.dispatch.segment_reply_formatting);
      case "enterprise_identity":
        return t(($) => $.tab_body.dispatch.segment_enterprise_identity);
      default:
        return id;
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] w-[min(56rem,92vw)] max-w-none flex-col gap-0 overflow-hidden p-0">
        <DialogHeader className="border-b px-5 py-4">
          <DialogTitle>{t(($) => $.tab_body.dispatch.dialog_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.tab_body.dispatch.dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
          <div className="mb-4 flex flex-wrap items-center gap-2">
            <span className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.dispatch.preview_mode)}
            </span>
            {SURFACES.map((option) => (
              <Button
                key={option}
                size="xs"
                variant={surface === option ? "default" : "outline"}
                onClick={() => {
                  setSurface(option);
                  setActiveSegment(null);
                }}
              >
                {surfaceLabel(option)}
              </Button>
            ))}
          </div>

          {isLoading ? (
            <div className="flex items-center gap-2 py-8 text-caption text-muted-foreground">
              <Loader2
                className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
              {t(($) => $.tab_body.dispatch.preview_loading)}
            </div>
          ) : segments.length === 0 ? (
            <p className="py-8 text-caption text-muted-foreground">
              {t(($) => $.tab_body.dispatch.preview_unavailable)}
            </p>
          ) : selected ? (
            <SegmentEditor
              segment={selected}
              draft={draft}
              onDraftChange={setDraft}
              onBack={() => setActiveSegment(null)}
              onSave={saveSegment}
              onRestore={restoreSegment}
              saving={saving}
              readOnly={readOnly}
              label={segmentLabel(selected.id)}
            />
          ) : (
            <SegmentList
              segments={segments}
              onSelect={setActiveSegment}
              segmentLabel={segmentLabel}
              readOnly={readOnly}
            />
          )}

          {!selected && (preview?.runtime_sections.length ?? 0) > 0 && (
            <div className="mt-6 rounded-lg border bg-muted/30 px-4 py-3">
              <p className="text-body font-medium">
                {t(($) => $.tab_body.dispatch.runtime_sections_title)}
              </p>
              <p className="mt-1 text-caption leading-snug text-muted-foreground">
                {t(($) => $.tab_body.dispatch.runtime_sections_hint)}
              </p>
              <ul className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1">
                {preview?.runtime_sections.map((section) => (
                  <li
                    key={section.id}
                    className="text-caption text-muted-foreground"
                  >
                    {section.id}
                    <span className="ml-1 opacity-60">({section.origin})</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>

        {!selected && (
          <div className="flex items-start gap-4 border-t px-5 py-4">
            <div className="min-w-0 flex-1 space-y-1">
              <label
                htmlFor={`agent-dispatch-always-new-issue-${agent.id}`}
                className="text-body font-medium"
              >
                {t(($) => $.tab_body.dispatch.always_new_issue_label)}
              </label>
              <p className="text-pretty text-caption leading-snug text-muted-foreground">
                {t(($) => $.tab_body.dispatch.always_new_issue_hint)}
              </p>
            </div>
            <Switch
              id={`agent-dispatch-always-new-issue-${agent.id}`}
              checked={agent.dispatch_always_new_issue === true}
              onCheckedChange={(checked) => {
                void onSave({ dispatch_always_new_issue: checked }).catch(
                  () => {
                    // toast handled by parent
                  },
                );
              }}
              disabled={readOnly}
            />
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function SegmentList({
  segments,
  onSelect,
  segmentLabel,
  readOnly,
}: {
  segments: DispatchPromptSegment[];
  onSelect: (id: string) => void;
  segmentLabel: (id: string) => string;
  readOnly: boolean;
}) {
  const { t } = useT("agents");
  const sourceLabel = (source: string) => {
    switch (source) {
      case "managed":
        return t(($) => $.tab_body.dispatch.source_managed);
      case "router":
        return t(($) => $.tab_body.dispatch.source_router);
      default:
        return t(($) => $.tab_body.dispatch.source_builtin);
    }
  };
  const excludedLabel = (reason: string | undefined) => {
    switch (reason) {
      case "no_dispatch_context":
        return t(($) => $.tab_body.dispatch.excluded_no_dispatch_context);
      case "not_a_dingtalk_task":
        return t(($) => $.tab_body.dispatch.excluded_not_a_dingtalk_task);
      case "not_an_enterprise_identity_runtime":
        return t(($) => $.tab_body.dispatch.excluded_not_enterprise_runtime);
      case "not_supplied":
        return t(($) => $.tab_body.dispatch.excluded_not_supplied);
      default:
        return t(($) => $.tab_body.dispatch.excluded_empty);
    }
  };
  return (
    <ol className="space-y-2">
      {segments.map((segment, index) => (
        <li key={segment.id}>
          <div
            className={`rounded-lg border px-4 py-3 ${
              segment.included ? "" : "opacity-60"
            }`}
          >
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="text-caption tabular-nums text-muted-foreground">
                {index + 1}
              </span>
              <span className="text-body font-medium">
                {segmentLabel(segment.id)}
              </span>
              <span className="rounded border px-1.5 py-0.5 text-caption text-muted-foreground">
                {sourceLabel(segment.source)}
              </span>
              {segment.overridden && (
                <span className="rounded bg-primary/10 px-1.5 py-0.5 text-caption font-medium text-primary">
                  {t(($) => $.tab_body.dispatch.badge_overridden)}
                </span>
              )}
              {!segment.included && (
                <span className="text-caption text-muted-foreground">
                  {excludedLabel(segment.excluded_reason)}
                </span>
              )}
              <span className="flex-1" />
              {segment.customizable && !readOnly && (
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => onSelect(segment.id)}
                >
                  {t(($) => $.tab_body.dispatch.edit_segment)}
                </Button>
              )}
              {!segment.customizable && (
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.tab_body.dispatch.not_customizable)}
                </span>
              )}
            </div>
            {segment.effective_text.trim().length > 0 && (
              <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap text-caption leading-6 text-muted-foreground">
                {segment.effective_text}
              </pre>
            )}
          </div>
        </li>
      ))}
    </ol>
  );
}

function SegmentEditor({
  segment,
  draft,
  onDraftChange,
  onBack,
  onSave,
  onRestore,
  saving,
  readOnly,
  label,
}: {
  segment: DispatchPromptSegment;
  draft: string;
  onDraftChange: (value: string) => void;
  onBack: () => void;
  onSave: () => void;
  onRestore: () => void;
  saving: boolean;
  readOnly: boolean;
  label: string;
}) {
  const { t } = useT("agents");
  const isDirty = draft !== segment.effective_text;
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="xs" variant="ghost" onClick={onBack}>
          {t(($) => $.tab_body.dispatch.back_to_structure)}
        </Button>
        <span className="text-body font-medium">{label}</span>
      </div>
      <p className="text-caption leading-snug text-muted-foreground">
        {segment.overridden
          ? t(($) => $.tab_body.dispatch.segment_override_active)
          : t(($) => $.tab_body.dispatch.segment_seeded_hint)}
      </p>
      <Textarea
        value={draft}
        onChange={(event) => onDraftChange(event.target.value)}
        rows={16}
        className="min-h-80 resize-y leading-6"
        disabled={readOnly}
        aria-label={label}
      />
      {!readOnly && (
        <div className="flex flex-wrap items-center justify-end gap-3">
          {isDirty && (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.common.unsaved_changes)}
            </span>
          )}
          {segment.overridden && (
            <Button
              size="sm"
              variant="outline"
              onClick={onRestore}
              disabled={saving}
            >
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.dispatch.restore_managed)}
            </Button>
          )}
          <Button size="sm" onClick={onSave} disabled={!isDirty || saving}>
            {saving ? (
              <Loader2
                className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
            ) : (
              <Save className="h-3.5 w-3.5" aria-hidden="true" />
            )}
            {t(($) => $.tab_body.common.save)}
          </Button>
        </div>
      )}
    </div>
  );
}
