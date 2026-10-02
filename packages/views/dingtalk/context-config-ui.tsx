"use client";

import { Children } from "react";
import { Loader2 } from "lucide-react";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";

/** Heading of one capability slot (指令, Skills, 连接器和插件) of a level, or
 * of a list inside a slot (`size="group"`). */
export function SlotHeading({
  label,
  action,
  size = "slot",
}: {
  label: string;
  action?: React.ReactNode;
  size?: "slot" | "group";
}) {
  return (
    <div className="flex min-h-7 items-center justify-between gap-2">
      <h3
        className={
          size === "slot"
            ? "min-w-0 truncate text-body font-semibold"
            : "min-w-0 truncate text-caption font-medium text-muted-foreground"
        }
      >
        {label}
      </h3>
      {action}
    </div>
  );
}

/** A titled list of the configuration page (skills, prompts, MCP servers).
 * `action` sits at the right of the title; `children` are the list items,
 * or `empty` when there are none. A `slot` list is one capability slot of
 * a level; a `group` list sits inside one. */
export function ItemGroup({
  label,
  action,
  empty,
  size = "group",
  children,
}: {
  label: string;
  action?: React.ReactNode;
  /** Shown instead of the list when it has no items. */
  empty?: React.ReactNode;
  size?: "slot" | "group";
  children?: React.ReactNode;
}) {
  const hasItems = Children.toArray(children).length > 0;
  return (
    <div className="space-y-1.5">
      <SlotHeading label={label} action={action} size={size} />
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

/** A list of one-line rows (ConfigRow), or `empty` when there are none. */
export function ConfigList({
  label,
  empty,
  children,
}: {
  label: string;
  empty?: React.ReactNode;
  children?: React.ReactNode;
}) {
  if (Children.toArray(children).length === 0) {
    return empty ? <p className="text-caption text-muted-foreground">{empty}</p> : null;
  }
  return (
    <ul aria-label={label} className="divide-y rounded-lg border bg-card">
      {children}
    </ul>
  );
}

/**
 * One item on one line: its icon, its name (truncated) and one status,
 * opening its detail; `action` (a switch or a button) sits on the right,
 * outside the name's button.
 */
export function ConfigRow({
  icon,
  name,
  status,
  openLabel,
  onOpen,
  action,
  muted = false,
}: {
  icon?: React.ReactNode;
  name: string;
  /** One short status next to the name. */
  status?: React.ReactNode;
  /** Accessible name of the row's open button; defaults to its text. */
  openLabel?: string;
  onOpen?: () => void;
  action?: React.ReactNode;
  /** Switched off: the name is dimmed. */
  muted?: boolean;
}) {
  const body = (
    <>
      {icon}
      <span className={cn("min-w-0 truncate text-body font-medium", muted && "text-muted-foreground")}>{name}</span>
      {status ? <span className="flex shrink-0 items-center">{status}</span> : null}
    </>
  );
  return (
    <li className="flex min-h-12 items-center gap-2 py-1.5 pr-2 pl-1.5">
      {onOpen ? (
        <button
          type="button"
          onClick={onOpen}
          aria-label={openLabel}
          className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md px-1.5 py-1 text-left transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {body}
        </button>
      ) : (
        <span className="flex min-w-0 flex-1 items-center gap-2.5 px-1.5 py-1">{body}</span>
      )}
      {action ? <span className="flex shrink-0 items-center">{action}</span> : null}
    </li>
  );
}
