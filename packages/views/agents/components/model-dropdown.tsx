"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Cpu, Loader2, Plus, Check, Info } from "lucide-react";
import {
  isCloudSandboxRuntime,
  runtimeModelsOptions,
} from "@multica/core/runtimes";
import type {
  AgentRuntime,
  RuntimeModel,
  RuntimeModelPricing,
} from "@multica/core/types";
import { findModelCapabilityEntry } from "./inspector/model-capability";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

// ModelDropdown renders a searchable, creatable model picker for an agent.
// It fetches the supported-model catalog from the selected runtime — the
// daemon enumerates models on demand via heartbeat piggyback. Providers
// whose runtime ignores per-agent model selection return supported=false,
// and the dropdown renders disabled with an explanation instead of silently
// accepting a value the backend would ignore. No built-in provider does so
// today — Antigravity gained `--model` in agy 1.0.6 — but the path stays for
// any future model-less runtime.
export function ModelDropdown({
  runtimeId,
  runtime,
  runtimeOnline,
  value,
  thinkingValue = "",
  onChange,
  onThinkingChange,
  disabled,
}: {
  runtimeId: string | null;
  runtime: AgentRuntime | null;
  runtimeOnline: boolean;
  value: string;
  thinkingValue?: string;
  onChange: (value: string) => void;
  onThinkingChange?: (value: string) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const fixedCatalog = isCloudSandboxRuntime(runtime);

  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );

  const supported = modelsQuery.data?.supported ?? true;
  // Stable reference for the model list — `?? []` would mint a fresh
  // array each render and force every downstream useMemo to invalidate.
  const models = useMemo(
    () => modelsQuery.data?.models ?? [],
    [modelsQuery.data],
  );
  const grouped = useMemo(() => groupByProvider(models), [models]);

  // When the selected runtime reports it doesn't support per-agent
  // model selection, clear any previously-saved value so we don't
  // persist a ghost configuration that never takes effect.
  useEffect(() => {
    if (!supported && value !== "") {
      onChange("");
    }
  }, [supported, value, onChange]);

  const filtered = useMemo(() => {
    if (!search.trim()) return grouped;
    const needle = search.toLowerCase();
    const out: Record<string, RuntimeModel[]> = {};
    for (const [provider, list] of Object.entries(grouped)) {
      const matches = list.filter(
        (m) =>
          m.id.toLowerCase().includes(needle) ||
          m.label.toLowerCase().includes(needle),
      );
      if (matches.length > 0) out[provider] = matches;
    }
    return out;
  }, [grouped, search]);

  const trimmedSearch = search.trim();
  const exactMatch = models.some(
    (m) => m.id === trimmedSearch || m.label === trimmedSearch,
  );
  const canCreate = !fixedCatalog && trimmedSearch.length > 0 && !exactMatch;

  const selectedEntry = findModelCapabilityEntry(
    models,
    value,
    runtime?.provider ?? "",
  );
  const thinkingLevels = selectedEntry?.thinking?.supported_levels ?? [];

  const select = (id: string) => {
    const nextEntry = findModelCapabilityEntry(
      models,
      id,
      runtime?.provider ?? "",
    );
    const hasThinking =
      (nextEntry?.thinking?.supported_levels.length ?? 0) > 0;
    onChange(id);
    if (!hasThinking || !onThinkingChange) setOpen(false);
    setSearch("");
  };

  const selectThinking = (next: string) => {
    onThinkingChange?.(next);
    setOpen(false);
  };

  const triggerLabel =
    value ||
    (disabled
      ? t(($) => $.model_dropdown.select_runtime_first)
      : runtimeOnline
        ? t(($) => $.model_dropdown.default_provider)
        : t(($) => $.model_dropdown.runtime_offline_manual));
  const selectedPricing = selectedEntry?.pricing;
  const pricingLabel = (pricing: RuntimeModelPricing): string => {
    const values = {
      input: formatModelPrice(pricing.input),
      output: formatModelPrice(pricing.output),
    };
    return pricing.base_tier_max_input_tokens
      ? t(($) => $.model_dropdown.price_from, values)
      : t(($) => $.model_dropdown.price, values);
  };

  if (!supported && !modelsQuery.isLoading) {
    return (
      <div className="flex flex-col min-w-0">
        <div className="flex h-6 items-center">
          <Label className="text-caption text-muted-foreground">{t(($) => $.model_dropdown.label)}</Label>
        </div>
        <div className="mt-1.5 flex items-start gap-2 rounded-lg border border-dashed border-border bg-muted/30 px-3 py-2.5 text-body text-muted-foreground">
          <Info className="mt-0.5 h-4 w-4 shrink-0" />
          <div className="min-w-0">
            <div>{t(($) => $.model_dropdown.managed_by_runtime_title)}</div>
            <div className="mt-0.5 text-caption">
              {t(($) => $.model_dropdown.managed_by_runtime_hint)}
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col min-w-0">
      <div className="flex h-6 items-center justify-between">
        <Label className="text-caption text-muted-foreground">{t(($) => $.model_dropdown.label)}</Label>
        {modelsQuery.isError && (
          <span className="text-caption text-muted-foreground">{t(($) => $.model_dropdown.discovery_failed)}</span>
        )}
      </div>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          disabled={disabled}
          className="flex w-full min-w-0 items-center gap-3 rounded-lg border border-border bg-background px-3 py-2.5 mt-1.5 text-left text-body transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
        >
          <Cpu className="h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1">
            {/* Wrapped in flex to mirror RuntimePicker's trigger DOM. The
                two pickers sit side-by-side; inline-in-flex vs block-line-
                box height calc would otherwise leave them ~1px misaligned. */}
            <div className="flex items-center gap-2">
              <span className="truncate font-medium">{triggerLabel}</span>
            </div>
            {value && (
              <div className="truncate text-caption text-muted-foreground">
                {modelLabel(models, value)}
                {selectedPricing ? ` · ${pricingLabel(selectedPricing)}` : ""}
              </div>
            )}
          </div>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${open ? "rotate-180" : ""}`}
          />
        </PopoverTrigger>
        <PopoverContent
          align="start"
          className="w-[var(--anchor-width)] p-0 overflow-hidden"
        >
          <div className="border-b border-border p-2">
            <Input
              autoFocus
              placeholder={t(($) => $.pickers.model_search_placeholder)}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="h-8"
            />
          </div>
          <div className="max-h-72 overflow-y-auto p-1">
            {modelsQuery.isLoading && (
              <div className="flex items-center gap-2 px-3 py-6 text-body text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                {t(($) => $.pickers.model_discovering)}
              </div>
            )}

            {!modelsQuery.isLoading &&
              Object.entries(filtered).map(([provider, list]) => (
                <div key={provider} className="mb-1">
                  {provider && (
                    <div className="px-2 pt-1.5 pb-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                      {provider}
                    </div>
                  )}
                  {list.map((m) => (
                    <button
                      type="button"
                      key={m.id}
                      onClick={() => select(m.id)}
                      className={`flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-body transition-colors ${
                        m.id === value ? "bg-accent" : "hover:bg-accent/50"
                      }`}
                    >
                      <div className="min-w-0 flex-1">
                        <div className="truncate font-medium">{m.label}</div>
                        {m.label !== m.id && (
                          <div className="truncate text-caption text-muted-foreground">
                            {m.id}
                          </div>
                        )}
                        {m.pricing && (
                          <div className="truncate text-caption text-muted-foreground">
                            {pricingLabel(m.pricing)}
                          </div>
                        )}
                      </div>
                      {m.id === value && (
                        <Check className="h-4 w-4 shrink-0 text-primary" />
                      )}
                    </button>
                  ))}
                </div>
              ))}

            {!modelsQuery.isLoading &&
              Object.keys(filtered).length === 0 &&
              !canCreate && (
                <div className="px-3 py-6 text-center text-body text-muted-foreground">
                  {t(($) => $.pickers.model_empty_with_dot)}
                </div>
              )}

            {canCreate && (
              <button
                type="button"
                onClick={() => select(trimmedSearch)}
                className="flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-body text-primary transition-colors hover:bg-accent/50"
              >
                <Plus className="h-4 w-4 shrink-0" />
                <span className="truncate">
                  {t(($) => $.pickers.model_custom_use, { value: trimmedSearch })}
                </span>
              </button>
            )}

            {value && (
              <button
                type="button"
                onClick={() => select("")}
                className="mt-1 flex w-full items-center gap-2 border-t border-border px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
              >
                {t(($) => $.model_dropdown.clear_full)}
              </button>
            )}

            {onThinkingChange && thinkingLevels.length > 0 ? (
              <div className="mt-1 border-t border-border pt-1">
                <div className="px-3 pt-1.5 pb-0.5 text-caption font-medium uppercase tracking-wide text-muted-foreground">
                  {t(($) => $.pickers.thinking_in_model)}
                </div>
                {thinkingLevels.map((level) => (
                  <button
                    type="button"
                    key={level.value}
                    onClick={() => selectThinking(level.value)}
                    className={`flex w-full items-center gap-2 rounded-md px-3 py-2 text-left text-body transition-colors ${
                      level.value === thinkingValue
                        ? "bg-accent"
                        : "hover:bg-accent/50"
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{level.label}</div>
                      {level.description ? (
                        <div className="truncate text-caption text-muted-foreground">
                          {level.description}
                        </div>
                      ) : null}
                    </div>
                    {level.value === thinkingValue ? (
                      <Check className="h-4 w-4 shrink-0 text-primary" />
                    ) : null}
                  </button>
                ))}
                {thinkingValue ? (
                  <button
                    type="button"
                    onClick={() => selectThinking("")}
                    className="flex w-full items-center px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
                  >
                    {t(($) => $.pickers.thinking_clear)}
                  </button>
                ) : null}
              </div>
            ) : null}
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

function groupByProvider(models: RuntimeModel[]): Record<string, RuntimeModel[]> {
  const out: Record<string, RuntimeModel[]> = {};
  for (const m of models) {
    const key = m.provider ?? "";
    if (!out[key]) out[key] = [];
    out[key].push(m);
  }
  return out;
}

function modelLabel(models: RuntimeModel[], id: string): string {
  const found = models.find((m) => m.id === id);
  if (!found) return "custom";
  return found.provider ? found.provider : "model";
}

function formatModelPrice(value: number): string {
  return value.toLocaleString(undefined, {
    minimumFractionDigits: value < 0.1 ? 2 : 0,
    maximumFractionDigits: value < 0.1 ? 4 : 2,
  });
}
