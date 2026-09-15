"use client";

import { useEffect, useRef, useState } from "react";
import { useASBRegions } from "@multica/core/runtimes";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

export function ASBRegionPicker({
  agent,
  canEdit,
  onSave,
}: {
  agent: Agent;
  canEdit: boolean;
  onSave: (updates: Record<string, unknown>) => Promise<void>;
}) {
  const { t } = useT("agents");
  const query = useASBRegions(agent.workspace_id, agent.runtime_id ?? "");
  const raw = agent.runtime_config?.asb_regions;
  const saved = Array.isArray(raw)
    ? raw.filter((v): v is string => typeof v === "string").sort()
    : [];
  const savedKey = JSON.stringify(saved);
  const previousKey = useRef(savedKey);
  const [selected, setSelected] = useState(saved);
  const [automatic, setAutomatic] = useState(saved.length === 0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const draftKey = JSON.stringify(automatic ? [] : [...selected].sort());
  const dirty = draftKey !== savedKey;
  useEffect(() => {
    if (previousKey.current !== savedKey) {
      setSelected(JSON.parse(savedKey));
      setAutomatic(savedKey === "[]");
      previousKey.current = savedKey;
    }
  }, [savedKey]);
  const regions = query.data?.regions ?? [];
  const unavailable = selected.filter((region) => !regions.includes(region));
  const disabled =
    !canEdit || saving || !query.data?.available || query.isError;
  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await onSave({
        runtime_config: {
          ...agent.runtime_config,
          asb_regions: automatic ? [] : [...selected].sort(),
        },
      });
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : t(($) => $.asb_regions.save_failed),
      );
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="w-full space-y-3">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.asb_regions.hint)}
      </p>
      {query.isPending ? (
        <p className="text-caption">{t(($) => $.asb_regions.loading)}</p>
      ) : null}
      {query.isError || (query.isSuccess && !query.data?.available) ? (
        <div className="space-y-2">
          <p className="text-caption text-destructive">
            {t(($) => $.asb_regions.load_failed)}
          </p>
          <Button size="sm" variant="outline" onClick={() => query.refetch()}>
            {t(($) => $.asb_regions.retry)}
          </Button>
        </div>
      ) : null}
      <fieldset disabled={disabled} className="space-y-2 disabled:opacity-60">
        <label className="flex items-center gap-2 text-caption">
          <input
            type="checkbox"
            checked={automatic}
            onChange={(e) => {
              setAutomatic(e.target.checked);
              if (!e.target.checked && selected.length === 0)
                setSelected(regions);
            }}
          />
          {t(($) => $.asb_regions.automatic)}
        </label>
        <div className="flex flex-wrap gap-x-4 gap-y-2">
          {regions.map((region) => (
            <label
              key={region}
              className="flex items-center gap-2 text-caption"
            >
              <input
                type="checkbox"
                checked={automatic || selected.includes(region)}
                onChange={(e) => {
                  const current = automatic ? regions : selected;
                  setAutomatic(false);
                  setSelected(
                    e.target.checked
                      ? [...new Set([...current, region])]
                      : current.filter((value) => value !== region),
                  );
                }}
              />
              {region}
            </label>
          ))}
        </div>
        {!automatic &&
          unavailable.map((region) => (
            <label
              key={region}
              className="flex items-center gap-2 text-caption text-destructive"
            >
              <input
                type="checkbox"
                checked
                onChange={() =>
                  setSelected(selected.filter((value) => value !== region))
                }
              />
              {region} — {t(($) => $.asb_regions.unavailable)}
            </label>
          ))}
        {!automatic && selected.length === 0 ? (
          <p className="text-caption text-destructive">
            {t(($) => $.asb_regions.selection_required)}
          </p>
        ) : null}
        {regions.length === 0 && query.data?.available ? (
          <p className="text-caption">{t(($) => $.asb_regions.empty)}</p>
        ) : null}
        {canEdit ? (
          <Button
            size="sm"
            onClick={save}
            disabled={
              !dirty ||
              (!automatic && (selected.length === 0 || unavailable.length > 0))
            }
          >
            {saving
              ? t(($) => $.asb_regions.saving)
              : t(($) => $.asb_regions.save)}
          </Button>
        ) : null}
      </fieldset>
      {error ? (
        <p role="alert" className="text-caption text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}
