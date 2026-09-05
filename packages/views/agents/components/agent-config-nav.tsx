"use client";

import { ChevronRight } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import type {
  AgentConfigGroup,
  ConfigGroupId,
  DetailTab,
} from "./agent-config-navigation";

interface AgentConfigNavProps {
  groups: readonly AgentConfigGroup[];
  activeView: DetailTab;
  onSelect: (view: DetailTab) => void;
}

export function AgentConfigNav({
  groups,
  activeView,
  onSelect,
}: AgentConfigNavProps) {
  const { t } = useT("agents");
  const activeGroup =
    groups.find((group) =>
      group.items.some((item) => item.id === activeView),
    ) ?? groups[0];

  if (!activeGroup) return null;

  const groupOptions = groups.map((group) => ({
    value: group.id,
    label: t(($) => $.tabs[group.labelKey]),
  }));
  const tabOptions = activeGroup.items.map((item) => ({
    value: item.id,
    label: t(($) => $.tabs[item.labelKey]),
  }));

  return (
    <>
      <aside className="hidden w-52 shrink-0 overflow-y-auto border-r border-surface-border p-3 md:block">
        <nav aria-label={t(($) => $.tabs.section_navigation_aria)}>
          {groups.map((group) => {
            const expanded = group.id === activeGroup.id;
            const firstItem = group.items[0];
            return (
              <div key={group.id} className="mb-1">
                <button
                  type="button"
                  aria-expanded={expanded}
                  onClick={() => {
                    if (!expanded && firstItem) onSelect(firstItem.id);
                  }}
                  className="flex h-8 w-full items-center rounded-md px-2 text-left text-caption font-medium text-foreground transition-colors hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <ChevronRight
                    aria-hidden="true"
                    className={cn(
                      "mr-1.5 size-3.5 transition-transform motion-reduce:transition-none",
                      expanded && "rotate-90",
                    )}
                  />
                  <span className="min-w-0 flex-1 truncate">
                    {t(($) => $.tabs[group.labelKey])}
                  </span>
                  {!expanded && (
                    <span
                      aria-hidden="true"
                      className="ml-2 tabular-nums text-muted-foreground"
                    >
                      {group.items.length}
                    </span>
                  )}
                </button>
                {expanded && (
                  <div
                    className="ml-3 border-l border-surface-border pl-2"
                    role="tablist"
                    aria-label={t(($) => $.tabs[group.labelKey])}
                  >
                    {group.items.map((item) => {
                      const active = item.id === activeView;
                      return (
                        <button
                          key={item.id}
                          type="button"
                          role="tab"
                          aria-selected={active}
                          onClick={() => onSelect(item.id)}
                          className={cn(
                            "flex min-h-8 w-full items-center rounded-md px-2 text-left text-caption transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                            active
                              ? "bg-surface-selected font-medium text-surface-selected-foreground hover:bg-surface-selected"
                              : "text-muted-foreground hover:bg-surface-hover hover:text-foreground",
                          )}
                        >
                          {t(($) => $.tabs[item.labelKey])}
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })}
        </nav>
      </aside>

      <div className="grid grid-cols-2 gap-2 border-b border-surface-border p-3 md:hidden">
        <Select
          items={groupOptions}
          value={activeGroup.id}
          onValueChange={(value: ConfigGroupId | null) => {
            const next = groups.find((group) => group.id === value)?.items[0];
            if (next) onSelect(next.id);
          }}
        >
          <SelectTrigger
            className="w-full min-w-0"
            aria-label={t(($) => $.tabs.config_group_aria)}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {groupOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          items={tabOptions}
          value={activeView}
          onValueChange={(value: DetailTab | null) => {
            if (value) onSelect(value);
          }}
        >
          <SelectTrigger
            className="w-full min-w-0"
            aria-label={t(($) => $.tabs.config_tab_aria)}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {tabOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </>
  );
}
