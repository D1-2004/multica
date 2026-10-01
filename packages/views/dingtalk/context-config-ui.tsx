"use client";

import { Children } from "react";
import { Loader2 } from "lucide-react";
import { Switch } from "@multica/ui/components/ui/switch";

/** A titled list of the configuration page (connectors, skills, prompts,
 * MCP servers). `action` sits at the right of the title; `children` are the
 * list items, or `empty` when there are none. */
export function ItemGroup({
  label,
  action,
  empty,
  children,
}: {
  label: string;
  action?: React.ReactNode;
  /** Shown instead of the list when it has no items. */
  empty?: React.ReactNode;
  children?: React.ReactNode;
}) {
  const hasItems = Children.toArray(children).length > 0;
  return (
    <div className="space-y-1.5">
      <div className="flex min-h-7 items-center justify-between gap-2">
        <h3 className="min-w-0 truncate text-caption font-medium text-muted-foreground">{label}</h3>
        {action}
      </div>
      {hasItems ? (
        <ul className="divide-y rounded-lg border bg-card">{children}</ul>
      ) : empty ? (
        <p className="text-caption text-muted-foreground">{empty}</p>
      ) : null}
    </div>
  );
}

/** A row's on/off switch; a spinner while its write is in flight. */
export function ToggleControl({
  busy,
  checked,
  disabled = false,
  label,
  onToggle,
}: {
  busy: boolean;
  checked: boolean;
  disabled?: boolean;
  label: string;
  onToggle: (enabled: boolean) => void;
}) {
  return (
    <span className="flex h-8 w-10 shrink-0 items-center justify-end">
      {busy ? (
        <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
      ) : (
        <Switch checked={checked} disabled={disabled} onCheckedChange={(next) => onToggle(next)} aria-label={label} />
      )}
    </span>
  );
}
