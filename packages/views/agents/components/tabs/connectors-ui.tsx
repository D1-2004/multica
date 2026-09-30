"use client";

import { useState } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
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
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import { cn } from "@multica/ui/lib/utils";
import { ConnectorLogo } from "../../../common/connector-logo";
import { MAX_BEARER_LENGTH, isValidBearer } from "../../../common/connector-credential";
import { isDesktopShell } from "../../../platform/local-directory";
import { openExternal } from "../../../platform/open-external";
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
  action,
}: {
  id: string;
  level: 2 | 3;
  title: string;
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
      </div>
      {action ? <div className="flex shrink-0 flex-wrap items-center gap-2">{action}</div> : null}
    </div>
  );
}

/** A labelled switch row: label (and an optional note) on the left, the
 * switch (a spinner while it saves) on the right. */
export function SwitchRow({
  id,
  label,
  note,
  checked,
  disabled = false,
  pending = false,
  onCheckedChange,
}: {
  id: string;
  label: string;
  /** Extra line under the label, e.g. why the switch is disabled. */
  note?: React.ReactNode;
  checked: boolean;
  disabled?: boolean;
  pending?: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0 flex-1 space-y-0.5">
        <label htmlFor={id} className="block text-label font-medium">
          {label}
        </label>
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

/** Grid of compact app tiles: two columns on phones, up to four on wide
 * screens. */
export function AppTileGrid({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <ul aria-label={label} className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-4">
      {children}
    </ul>
  );
}

/** One app as a compact tile (logo, name, status); opens its dialog. */
export function AppTile({
  slug,
  name,
  ariaLabel,
  onOpen,
  children,
}: {
  slug: string;
  name: string;
  ariaLabel: string;
  onOpen: () => void;
  /** Status pills. */
  children: React.ReactNode;
}) {
  return (
    <li className="min-w-0">
      <button
        type="button"
        onClick={onOpen}
        aria-label={ariaLabel}
        className="flex h-full w-full min-w-0 flex-col gap-2 rounded-lg border bg-card p-3 text-left transition-colors hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex w-full min-w-0 items-center gap-2">
          <ConnectorLogo slug={slug} />
          <span className="min-w-0 truncate text-body font-medium">{name}</span>
        </span>
        <span className="flex min-w-0 flex-wrap gap-1">{children}</span>
      </button>
    </li>
  );
}

/** A titled section inside an app dialog. */
export function DialogSection({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3 border-t pt-4" aria-labelledby={id}>
      <h3 id={id} className="text-label font-medium text-muted-foreground">
        {title}
      </h3>
      {children}
    </section>
  );
}

/** Provider page where users grant the app access to their resources
 * (GitHub App installation). Desktop opens it in the system browser. */
export function InstallLink({ url }: { url: string }) {
  const { t } = useT("agents");
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center gap-1 text-caption font-medium text-foreground underline-offset-4 hover:underline"
      onClick={(event) => {
        if (!isDesktopShell()) return;
        event.preventDefault();
        openExternal(url);
      }}
    >
      {t(($) => $.internal_mcp.catalog.install_link)}
      <ExternalLink className="size-3" aria-hidden="true" />
    </a>
  );
}

/** Write-only token input (a Bearer or a Personal Access Token). `onSave`
 * stores it; a rejection shows its message under the input. */
export function TokenForm({
  inputId,
  label,
  placeholder,
  pending,
  onSave,
  onCancel,
}: {
  inputId: string;
  label: string;
  placeholder: string;
  pending: boolean;
  onSave: (token: string) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useT("agents");
  const [token, setToken] = useState("");
  const [error, setError] = useState("");

  const submit = async () => {
    const value = token.trim();
    if (!isValidBearer(value)) {
      setError(t(($) => $.internal_mcp.catalog.pat_invalid));
      return;
    }
    setError("");
    try {
      await onSave(value);
      setToken("");
    } catch (e) {
      setError(errorMessage(e, t(($) => $.internal_mcp.catalog.pat_failed)));
    }
  };

  return (
    <div className="space-y-2">
      <label htmlFor={inputId} className="sr-only">
        {label}
      </label>
      <Input
        id={inputId}
        type="password"
        autoComplete="off"
        autoCapitalize="off"
        spellCheck={false}
        maxLength={MAX_BEARER_LENGTH}
        value={token}
        aria-invalid={error ? true : undefined}
        placeholder={placeholder}
        onChange={(event) => {
          setToken(event.target.value);
          setError("");
        }}
      />
      {error ? (
        <p role="alert" className="text-caption text-destructive">
          {error}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={pending}>
          {t(($) => $.tab_body.connectors.cancel)}
        </Button>
        <Button size="sm" onClick={() => void submit()} disabled={pending || !token.trim()}>
          {pending && <Loader2 className="size-3.5 animate-spin motion-reduce:animate-none" />}
          {t(($) => $.internal_mcp.catalog.save)}
        </Button>
      </div>
    </div>
  );
}
