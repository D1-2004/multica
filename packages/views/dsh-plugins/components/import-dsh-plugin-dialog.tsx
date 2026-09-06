"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { ImportDshPluginResult } from "@multica/core/dsh-plugins";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Separator } from "@multica/ui/components/ui/separator";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

/**
 * Import one plugin by package reference.
 *
 * The field takes exactly what `dsh plugin add` takes, because that is the
 * install contract DeepSeek Harness actually has — there is no registry to
 * pick from and no Multica-specific manifest. The server resolves the package,
 * checks it is something the harness can load, and reports why not when it is
 * not, so a bad package fails here rather than inside a task.
 */
export function ImportDshPluginDialog({
  open,
  onOpenChange,
  initialSource,
  onImported,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialSource: string;
  onImported: () => void;
}) {
  const { t } = useT("dsh-plugins");
  const [source, setSource] = useState(initialSource);
  const [message, setMessage] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);
  const [fileName, setFileName] = useState("");

  useEffect(() => {
    if (open) {
      setSource(initialSource);
      setMessage("");
      setFileName("");
      if (fileInput.current) fileInput.current.value = "";
    }
  }, [open, initialSource]);

  const importPlugin = useMutation({
    mutationFn: (spec: string) => api.importDshPlugin({ source: spec }),
    onSuccess: (result: ImportDshPluginResult) => handleResult(result),
    onError: (error: unknown) => {
      setMessage(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.import_dialog.failed),
      );
    },
  });

  const handleResult = (result: ImportDshPluginResult) => {
    if (result.plugin) {
      onImported();
      onOpenChange(false);
      return;
    }
    setMessage(
      result.error ||
        (result.existingPlugin
          ? t(($) => $.import_dialog.conflict, {
              name: result.existingPlugin.packageName,
            })
          : t(($) => $.import_dialog.failed)),
    );
  };

  const uploadPlugin = useMutation({
    mutationFn: (file: File) => api.uploadDshPlugin(file),
    onSuccess: handleResult,
    onError: (error: unknown) => {
      setMessage(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.import_dialog.failed),
      );
    },
  });

  const busy = importPlugin.isPending || uploadPlugin.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.import_dialog.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.import_dialog.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-2">
          <Label htmlFor="dsh-plugin-source">
            {t(($) => $.import_dialog.source_label)}
          </Label>
          <Input
            id="dsh-plugin-source"
            value={source}
            onChange={(event) => setSource(event.target.value)}
            placeholder={t(($) => $.import_dialog.source_placeholder)}
            autoComplete="off"
            spellCheck={false}
          />
          <p className="text-caption text-muted-foreground">
            {t(($) => $.import_dialog.source_help)}
          </p>

          <Separator className="my-1" />

          {/* A package that is not published anywhere still has to be
              importable. The file is normalised and then held to exactly the
              same checks as a package fetched from a registry. */}
          <Label htmlFor="dsh-plugin-file">
            {t(($) => $.import_dialog.upload_label)}
          </Label>
          <input
            ref={fileInput}
            id="dsh-plugin-file"
            type="file"
            accept=".zip,.tgz,.tar.gz,application/zip,application/gzip"
            className="text-caption file:mr-3 file:rounded-md file:border file:bg-background file:px-2 file:py-1 file:text-caption"
            onChange={(event) => {
              const file = event.target.files?.[0];
              setFileName(file?.name ?? "");
              setMessage("");
              if (file) uploadPlugin.mutate(file);
            }}
            disabled={busy}
          />
          <p className="text-caption text-muted-foreground">
            {uploadPlugin.isPending && fileName
              ? t(($) => $.import_dialog.uploading, { name: fileName })
              : t(($) => $.import_dialog.upload_help)}
          </p>
          {message ? (
            <p role="alert" className="text-caption text-destructive">
              {message}
            </p>
          ) : null}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.import_dialog.cancel)}
          </Button>
          <Button
            onClick={() => importPlugin.mutate(source.trim())}
            disabled={!source.trim() || busy}
          >
            {importPlugin.isPending
              ? t(($) => $.import_dialog.submitting)
              : t(($) => $.import_dialog.submit)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
