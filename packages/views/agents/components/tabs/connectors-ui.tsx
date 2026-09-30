"use client";

import { Loader2 } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

/** Server message of a failed write, else the localized fallback. */
export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

/** Dashed placeholder for loading, empty and error states. */
export function ConnectorNotice({
  children,
  loading = false,
}: {
  children: React.ReactNode;
  loading?: boolean;
}) {
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-caption text-muted-foreground">
      {loading ? (
        <Loader2 className="size-4 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      ) : null}
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">{children}</div>
    </div>
  );
}

export type StatusTone = "success" | "warning" | "muted";

/** Small status label with a colored dot; the text carries the meaning. */
export function StatusPill({ tone, children }: { tone: StatusTone; children: React.ReactNode }) {
  return (
    <span
      className={cn(
        "inline-flex max-w-full shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-micro font-medium",
        tone === "success" && "border-success/40 bg-success/10 text-success",
        tone === "warning" && "border-warning/40 bg-warning/10 text-warning",
        tone === "muted" && "bg-muted text-muted-foreground",
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          "size-1.5 shrink-0 rounded-full",
          tone === "success" && "bg-success",
          tone === "warning" && "bg-warning",
          tone === "muted" && "bg-muted-foreground/60",
        )}
      />
      <span className="truncate">{children}</span>
    </span>
  );
}

/** Heading of a block (h2) or sub-section (h3) of the connector tab. */
export function SectionHeading({
  id,
  level,
  title,
  hint,
  action,
}: {
  id: string;
  level: 2 | 3;
  title: string;
  hint?: string;
  action?: React.ReactNode;
}) {
  const Heading = level === 2 ? "h2" : "h3";
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
      <div className="min-w-0 flex-1">
        <Heading
          id={id}
          className={level === 2 ? "text-title font-semibold" : "text-body font-medium"}
        >
          {title}
        </Heading>
        {hint ? (
          <p className="mt-1 max-w-2xl text-pretty text-caption leading-5 text-muted-foreground">
            {hint}
          </p>
        ) : null}
      </div>
      {action ? <div className="flex shrink-0 flex-wrap items-center gap-2">{action}</div> : null}
    </div>
  );
}

/** A labelled switch row: label and hint on the left, the switch (a spinner
 * while it saves) on the right. */
export function SwitchRow({
  id,
  label,
  hint,
  note,
  checked,
  disabled = false,
  pending = false,
  onCheckedChange,
}: {
  id: string;
  label: string;
  hint?: string;
  /** Extra line under the hint, e.g. why the switch is disabled. */
  note?: React.ReactNode;
  checked: boolean;
  disabled?: boolean;
  pending?: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  const hintId = hint ? `${id}-hint` : undefined;
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0 flex-1 space-y-0.5">
        <label htmlFor={id} className="block text-label font-medium">
          {label}
        </label>
        {hint ? (
          <p id={hintId} className="text-caption text-muted-foreground">
            {hint}
          </p>
        ) : null}
        {note}
      </div>
      <span className="flex h-6 w-10 shrink-0 items-center justify-end">
        {pending ? (
          <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" />
        ) : (
          <Switch
            id={id}
            checked={checked}
            disabled={disabled}
            onCheckedChange={onCheckedChange}
            aria-describedby={hintId}
          />
        )}
      </span>
    </div>
  );
}

/** Destructive confirmation. While `pending` it stays open with both buttons
 * disabled; the caller closes it (`onOpenChange(false)`) when it is done. */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  pending = false,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  confirmLabel: string;
  pending?: boolean;
  onConfirm: () => void;
}) {
  const { t } = useT("agents");
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (next || !pending) onOpenChange(next);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t(($) => $.tab_body.connectors.cancel)}</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={(event) => {
              event.preventDefault();
              onConfirm();
            }}
          >
            {pending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
