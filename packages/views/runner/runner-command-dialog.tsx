"use client";

import { useEffect, useState } from "react";
import { Check, Copy, Terminal } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { copyText } from "@multica/ui/lib/clipboard";
import { CODE_LIGATURE_CLASS } from "@multica/ui/lib/code-style";
import { cn } from "@multica/ui/lib/utils";

export function RunnerCommandDialog({
  command,
  title,
  description,
  expiry,
  copiedToast,
  copyAria,
  closeLabel,
  onClose,
}: {
  command: string | null;
  title: string;
  description: string;
  expiry: string;
  copiedToast: string;
  copyAria: string;
  closeLabel: string;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!command) setCopied(false);
  }, [command]);

  const handleCopy = async () => {
    if (!command) return;
    if (await copyText(command)) {
      setCopied(true);
      toast.success(copiedToast);
    }
  };

  return (
    <Dialog open={command !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {command && (
          <div className="space-y-3">
            <div className="flex items-start gap-2 rounded-lg bg-muted px-3 py-3">
              <Terminal
                className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <code
                className={cn(
                  "min-w-0 flex-1 break-all whitespace-pre-wrap font-mono text-xs",
                  CODE_LIGATURE_CLASS,
                )}
              >
                {command}
              </code>
              <button
                type="button"
                className="shrink-0 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={copyAria}
                onClick={() => void handleCopy()}
              >
                {copied ? (
                  <Check className="h-4 w-4 text-success" aria-hidden />
                ) : (
                  <Copy className="h-4 w-4" aria-hidden />
                )}
              </button>
            </div>
            <p className="text-xs text-muted-foreground">{expiry}</p>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {closeLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
