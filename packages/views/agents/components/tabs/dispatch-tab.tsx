"use client";

import { useEffect, useState } from "react";
import { Loader2, RotateCcw, Save } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../../i18n";

/**
 * Dispatch policy for messages that reach this agent from an external channel
 * (today: DingTalk, through Agent Dispatch V2).
 *
 * Two independent knobs live here because both only affect dispatched runs and
 * neither touches the agent's persona:
 *
 * - `dispatch_prompt` replaces the deployment-managed instruction. Empty is the
 *   default and keeps the managed one.
 * - `dispatch_always_new_issue` turns off conversational Issue threading.
 */
export function DispatchTab({
  agent,
  onSave,
  onDirtyChange,
  readOnly = false,
}: {
  agent: Agent;
  // An inline object type rather than UpdateAgentRequest: the pane's onUpdate
  // takes Record<string, unknown>, and an interface has no implicit index
  // signature. Same shape McpConfigTab uses.
  onSave: (updates: {
    dispatch_prompt?: string;
    dispatch_always_new_issue?: boolean;
  }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
  readOnly?: boolean;
}) {
  const { t } = useT("agents");
  const savedPrompt = agent.dispatch_prompt ?? "";
  const savedAlwaysNew = agent.dispatch_always_new_issue === true;
  const hasOverride = savedPrompt.trim().length > 0;

  // The managed policy this override would replace. The editor opens on it
  // instead of on a blank field: saving a prompt drops the managed safety,
  // credential-handling, and truthfulness clauses wholesale, and starting from
  // the real text makes keeping them the default and removing one a deliberate
  // edit. Failure is not fatal — a blank seed only means an empty editor.
  const { data: managedDefault } = useQuery({
    queryKey: ["agent-dispatch-prompt-default", agent.id],
    queryFn: () => api.getAgentDispatchPromptDefault(agent.id),
    enabled: !readOnly,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
  const seed = managedDefault?.prompt ?? "";

  // Baseline is what "no unsaved change" means. With no override that is the
  // managed text the editor was seeded with, so simply opening the tab is not
  // dirty and does not block a tab switch.
  const baseline = hasOverride ? savedPrompt : seed;

  const [prompt, setPrompt] = useState(baseline);
  const [saving, setSaving] = useState(false);
  const isDirty = prompt !== baseline;

  // Sync when switching between agents, when a save round-trips, and when the
  // managed default arrives after the first render.
  useEffect(() => {
    setPrompt(baseline);
  }, [agent.id, baseline]);

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  const handleSavePrompt = async () => {
    setSaving(true);
    try {
      await onSave({ dispatch_prompt: prompt });
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  // Drops the override so the agent follows the managed policy again — and
  // keeps following later updates to it. An empty string is an explicit clear
  // on the server, not an omitted field.
  const handleRestoreManaged = async () => {
    setSaving(true);
    try {
      await onSave({ dispatch_prompt: "" });
      setPrompt(seed);
    } catch {
      // toast handled by parent
    } finally {
      setSaving(false);
    }
  };

  // The switch is a determinate single-field toggle, so it commits on change
  // rather than joining the prompt's explicit save. Reverting on failure is the
  // parent's job — it rolls back the fields each call wrote.
  const handleToggleAlwaysNew = (checked: boolean) => {
    void onSave({ dispatch_always_new_issue: checked }).catch(() => {
      // toast handled by parent
    });
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <label
          htmlFor={`agent-dispatch-prompt-${agent.id}`}
          className="text-body font-medium"
        >
          {t(($) => $.tab_body.dispatch.prompt_label)}
        </label>
        <p className="max-w-2xl text-pretty text-body leading-6 text-muted-foreground">
          {t(($) => $.tab_body.dispatch.prompt_intro)}
        </p>
        <Textarea
          id={`agent-dispatch-prompt-${agent.id}`}
          name="agent-dispatch-prompt"
          autoComplete="off"
          value={prompt}
          onChange={(event) => setPrompt(event.target.value)}
          placeholder={t(($) => $.tab_body.dispatch.prompt_placeholder)}
          rows={18}
          className="min-h-96 resize-y leading-6"
          disabled={readOnly}
        />
        <p className="text-caption leading-snug text-muted-foreground">
          {hasOverride
            ? t(($) => $.tab_body.dispatch.prompt_override_active)
            : seed.length > 0
              ? t(($) => $.tab_body.dispatch.prompt_seeded_hint)
              : t(($) => $.tab_body.dispatch.prompt_override_inactive)}
        </p>
      </div>

      {!readOnly && (
        <div className="flex flex-wrap items-center justify-end gap-3">
          {isDirty && (
            <span className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.common.unsaved_changes)}
            </span>
          )}
          {hasOverride && (
            <Button
              size="sm"
              variant="outline"
              onClick={handleRestoreManaged}
              disabled={saving}
            >
              <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
              {t(($) => $.tab_body.dispatch.restore_managed)}
            </Button>
          )}
          <Button
            size="sm"
            onClick={handleSavePrompt}
            disabled={!isDirty || saving}
          >
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

      <div className="flex items-start gap-4 rounded-lg border px-4 py-3.5">
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
          checked={savedAlwaysNew}
          onCheckedChange={handleToggleAlwaysNew}
          disabled={readOnly}
        />
      </div>
    </div>
  );
}
