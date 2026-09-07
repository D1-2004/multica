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
      <aside className="hidden w-56 shrink-0 overflow-y-auto border-r border-sidebar-border bg-sidebar py-2 md:block">
        <nav aria-label={t(($) => $.tabs.section_navigation_aria)}>
          {groups.map((group) => {
            return (
              <section key={group.id} className="px-2 py-0.5">
                <h3
                  className="mb-0.5 flex h-5 items-center rounded-md px-2 text-micro font-semibold text-muted-foreground/80"
                >
                  {t(($) => $.tabs[group.labelKey])}
                </h3>
                <div
                  className="flex min-w-0 flex-col gap-0.5"
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
                          "flex h-8 w-full min-w-0 items-center gap-2 overflow-hidden rounded-md p-2 text-left text-body ring-sidebar-ring outline-hidden transition-colors focus-visible:ring-2",
                          active
                            ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground hover:bg-sidebar-accent"
                            : "text-muted-foreground hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground",
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
