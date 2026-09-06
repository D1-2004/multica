"use client";

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
      <aside className="hidden w-56 shrink-0 overflow-y-auto border-r border-surface-border bg-muted/20 px-3 py-6 md:block">
        <nav aria-label={t(($) => $.tabs.section_navigation_aria)}>
          {groups.map((group) => {
            return (
              <section key={group.id} className="mb-6 last:mb-0">
                <h3
                  className="px-3 pb-1 text-micro font-semibold leading-5 text-muted-foreground/75 text-pretty"
                >
                  {t(($) => $.tabs[group.labelKey])}
                </h3>
                <div
                  className="mt-1 space-y-1"
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
                          "relative flex min-h-10 w-full min-w-0 items-center rounded-lg px-3 text-left text-body transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                          active
                            ? "bg-surface-selected font-semibold text-surface-selected-foreground shadow-xs before:absolute before:inset-y-2.5 before:left-0 before:w-0.5 before:rounded-full before:bg-brand hover:bg-surface-selected"
                            : "text-foreground/70 hover:bg-surface-hover hover:text-foreground",
                        )}
                      >
                        <span className="min-w-0 truncate">
                          {t(($) => $.tabs[item.labelKey])}
                        </span>
                      </button>
                    );
                  })}
                </div>
              </section>
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
