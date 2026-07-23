"use client";

import { useEffect, useState } from "react";
import { ChevronDown, FileText, Plus, X } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { GitHubAgentSkillPreview, SkillSummary } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { skillListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { useT } from "../../i18n";
import { SkillPickerList } from "./skill-picker-list";

interface SkillMultiSelectProps {
  /** Currently-selected skill IDs (controlled). */
  selectedIds: ReadonlySet<string>;
  /** Replaces the selection on every toggle. */
  onChange: (next: Set<string>) => void;
  /** Repository-managed skills are materialized by the GitHub source.
   *  They are shown selected but never submitted as workspace skill IDs. */
  managedSkills?: readonly GitHubAgentSkillPreview[];
}

/**
 * Multi-select wrapper for the create-agent form. Collapsed by default;
 * expands into a SkillPickerList configured for toggle behaviour
 * (click adds to / removes from the local selection set).
 *
 * Shares its visual surface with SkillAddDialog via SkillPickerList —
 * one component owns search + row rendering + indicators, so a tweak
 * to either appears identically in both flows.
 */
export function SkillMultiSelect({
  selectedIds,
  onChange,
  managedSkills = [],
}: SkillMultiSelectProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { data: workspaceSkills = [], isLoading } = useQuery(skillListOptions(wsId));
  const [expanded, setExpanded] = useState(
    selectedIds.size > 0 || managedSkills.length > 0,
  );

  useEffect(() => {
    if (managedSkills.length > 0) setExpanded(true);
  }, [managedSkills.length]);

  const label = t(($) => $.create_dialog.skills_section.label);
  const selectedCount = selectedIds.size + managedSkills.length;

  const toggle = (skill: SkillSummary) => {
    const next = new Set(selectedIds);
    if (next.has(skill.id)) next.delete(skill.id);
    else next.add(skill.id);
    onChange(next);
  };

  if (!expanded) {
    return (
      <div>
        <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="mt-1.5 flex w-full items-center gap-2.5 rounded-lg border bg-card px-3 py-3 text-left transition-colors hover:border-primary/40 hover:bg-accent/40"
        >
          <Plus className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
            {selectedCount > 0
              ? t(($) => $.create_dialog.skills_section.selected, {
                  count: selectedCount,
                })
              : t(($) => $.create_dialog.skills_section.placeholder)}
          </div>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
        </button>
      </div>
    );
  }

  return (
    <div>
      <div className="flex items-center justify-between">
        <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
          {selectedCount > 0 ? (
            <span className="ml-2 text-foreground/60">({selectedCount})</span>
          ) : null}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => setExpanded(false)}
          className="h-6 gap-1 px-2 text-xs"
        >
          <X className="h-3 w-3" />
          {t(($) => $.create_dialog.skills_section.collapse)}
        </Button>
      </div>

      <div className="mt-1.5 space-y-2">
        {managedSkills.length > 0 && (
          <div className="overflow-hidden rounded-lg border bg-card">
            <div className="space-y-0.5 p-1.5">
              {managedSkills.map((skill) => (
                <button
                  key={skill.source_path}
                  type="button"
                  disabled
                  aria-pressed="true"
                  className="flex w-full items-center gap-2.5 rounded-md bg-accent px-2.5 py-2 text-left disabled:cursor-default disabled:opacity-100"
                >
                  <Checkbox
                    checked
                    disabled
                    tabIndex={-1}
                    className="pointer-events-none"
                  />
                  <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">{skill.name}</div>
                    {skill.description ? (
                      <div className="truncate text-xs text-muted-foreground">
                        {skill.description}
                      </div>
                    ) : null}
                  </div>
                  <span className="rounded border px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
                    {t(($) => $.creation_studio.github.managed_badge)}
                  </span>
                </button>
              ))}
            </div>
          </div>
        )}
        <SkillPickerList
          skills={workspaceSkills}
          selectedIds={selectedIds}
          onToggle={toggle}
          loading={isLoading}
          emptyMessage={t(($) => $.create_dialog.skills_section.list_empty_multi)}
        />
      </div>
    </div>
  );
}
