"use client";

import { ChevronDown, Search } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";
import type { RuntimeOwnershipScope } from "./runtime-machines";

const RUNTIME_SCOPES: RuntimeOwnershipScope[] = ["mine", "all"];
const ALL_OWNERS = "__all_owners__";

export interface RuntimeOwnerOption {
  id: string;
  name: string;
  count: number;
}

export function RuntimeListToolbar({
  scope,
  onScopeChange,
  scopeCounts,
  search,
  onSearchChange,
  ownerId,
  onOwnerChange,
  ownerOptions,
  visibleCount,
}: {
  scope: RuntimeOwnershipScope;
  onScopeChange: (scope: RuntimeOwnershipScope) => void;
  scopeCounts: Record<RuntimeOwnershipScope, number>;
  search: string;
  onSearchChange: (value: string) => void;
  ownerId: string | null;
  onOwnerChange: (ownerId: string | null) => void;
  ownerOptions: RuntimeOwnerOption[];
  visibleCount: number;
}) {
  const { t } = useT("runtimes");
  const labels: Record<RuntimeOwnershipScope, string> = {
    mine: t(($) => $.page.scope_mine),
    all: t(($) => $.page.scope_all),
  };
  const scopeTotal = scopeCounts[scope];
  const isNarrowed = search.trim().length > 0 || !!ownerId;
  const ownerItems = [
    { value: ALL_OWNERS, label: t(($) => $.page.all_owners) },
    ...ownerOptions.map((owner) => ({
      value: owner.id,
      label: `${owner.name} ${owner.count}`,
    })),
  ];

  return (
    <div className="h-12 shrink-0 overflow-x-auto border-b px-5 [-webkit-overflow-scrolling:touch]">
      <div className="flex h-full w-max min-w-full items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <div className="relative shrink-0">
            <Search
              aria-hidden="true"
              className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              type="search"
              value={search}
              onChange={(event) => onSearchChange(event.target.value)}
              aria-label={t(($) => $.page.search_placeholder)}
              placeholder={t(($) => $.page.search_placeholder)}
              className="h-8 w-52 pl-8 text-body sm:w-56"
            />
          </div>

          <div className="hidden shrink-0 items-center gap-1 md:flex">
            {RUNTIME_SCOPES.map((value) => (
              <Button
                key={value}
                type="button"
                variant="outline"
                size="sm"
                className={
                  scope === value
                    ? "gap-1.5 bg-accent text-accent-foreground hover:bg-accent/80"
                    : "gap-1.5 text-muted-foreground"
                }
                onClick={() => onScopeChange(value)}
              >
                {labels[value]}
                <span className="tabular-nums text-caption text-muted-foreground">
                  {scopeCounts[value]}
                </span>
              </Button>
            ))}
          </div>

          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="shrink-0 gap-1 text-muted-foreground md:hidden"
                >
                  {labels[scope]}
                  <ChevronDown aria-hidden="true" className="size-3" />
                </Button>
              }
            />
            <DropdownMenuContent align="start">
              <DropdownMenuRadioGroup
                value={scope}
                onValueChange={(value) =>
                  onScopeChange(value as RuntimeOwnershipScope)
                }
              >
                {RUNTIME_SCOPES.map((value) => (
                  <DropdownMenuRadioItem key={value} value={value}>
                    {labels[value]}
                    <span className="ml-2 tabular-nums text-caption text-muted-foreground">
                      {scopeCounts[value]}
                    </span>
                  </DropdownMenuRadioItem>
                ))}
              </DropdownMenuRadioGroup>
            </DropdownMenuContent>
          </DropdownMenu>

          {isNarrowed && (
            <span className="hidden shrink-0 text-caption tabular-nums text-muted-foreground md:inline">
              {visibleCount} / {scopeTotal}
            </span>
          )}
        </div>

        {scope === "all" && (
          <Select
            items={ownerItems}
            value={ownerId ?? ALL_OWNERS}
            onValueChange={(value) =>
              onOwnerChange(value === ALL_OWNERS ? null : value)
            }
          >
            <SelectTrigger
              size="sm"
              aria-label={t(($) => $.page.owner_filter)}
              className="w-44 shrink-0"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent align="end">
              <SelectItem value={ALL_OWNERS}>
                {t(($) => $.page.all_owners)}
              </SelectItem>
              {ownerOptions.map((owner) => (
                <SelectItem key={owner.id} value={owner.id}>
                  <span className="min-w-0 flex-1 truncate">{owner.name}</span>
                  <span className="ml-3 tabular-nums text-caption text-muted-foreground">
                    {owner.count}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </div>
    </div>
  );
}
