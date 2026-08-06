"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Loader2, Save } from "lucide-react";
import type { Agent } from "@multica/core/types";
import {
  mergeLLMTraceRuntimeConfig,
  parseLLMTraceRuntimeConfig,
  type LLMTraceRuntimeConfig,
} from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { useT } from "../../../i18n";

function configsEqual(
  left: LLMTraceRuntimeConfig,
  right: LLMTraceRuntimeConfig,
): boolean {
  return left.enabled === right.enabled && left.sinkUrl === right.sinkUrl;
}

export function LLMTraceTab({
  agent,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  onSave: (updates: { runtime_config: Record<string, unknown> }) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const original = useMemo(
    () => parseLLMTraceRuntimeConfig(agent.runtime_config),
    [agent.runtime_config],
  );
  const [state, setState] = useState<LLMTraceRuntimeConfig>(original);
  const [saving, setSaving] = useState(false);
  const previousOriginalRef = useRef(original);

  useEffect(() => {
    setState((current) =>
      configsEqual(current, previousOriginalRef.current) ? original : current,
    );
    previousOriginalRef.current = original;
  }, [original]);

  const dirty = !configsEqual(original, state);

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const handleSave = async () => {
    if (!dirty || saving) return;
    setSaving(true);
    try {
      await onSave({
        runtime_config: mergeLLMTraceRuntimeConfig(agent.runtime_config, state),
      });
      toast.success(t(($) => $.tab_body.llm_trace.saved_toast));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.tab_body.llm_trace.save_failed_toast),
      );
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex h-full flex-col space-y-4">
      <p className="text-xs text-muted-foreground">
        {t(($) => $.tab_body.llm_trace.intro)}
      </p>

      <div className="flex items-center justify-between gap-4 rounded-md border p-3">
        <div>
          <Label htmlFor="llm-trace-enabled" className="text-xs font-medium">
            {t(($) => $.tab_body.llm_trace.enabled_label)}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.llm_trace.enabled_hint)}
          </p>
        </div>
        <Switch
          id="llm-trace-enabled"
          checked={state.enabled}
          onCheckedChange={(enabled: boolean) =>
            setState((current) => ({ ...current, enabled }))
          }
        />
      </div>

      {state.enabled && (
        <div className="space-y-1.5">
          <Label htmlFor="llm-trace-sink-url" className="text-xs">
            {t(($) => $.tab_body.llm_trace.sink_url_label)}
          </Label>
          <Input
            id="llm-trace-sink-url"
            type="url"
            value={state.sinkUrl}
            onChange={(event) =>
              setState((current) => ({
                ...current,
                sinkUrl: event.target.value,
              }))
            }
            placeholder={t(($) => $.tab_body.llm_trace.sink_url_placeholder)}
            className="font-mono text-xs"
          />
        </div>
      )}

      <div className="flex items-center justify-end gap-3 pt-2">
        {dirty && (
          <span className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.common.unsaved_changes)}
          </span>
        )}
        <Button onClick={handleSave} disabled={!dirty || saving} size="sm">
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Save className="h-3.5 w-3.5" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}
