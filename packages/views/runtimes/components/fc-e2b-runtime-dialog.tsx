"use client";

import { useState } from "react";
import type { FormEvent } from "react";
import { Cloud, Loader2 } from "lucide-react";
import { toast } from "sonner";
import type { RuntimeVisibility } from "@multica/core/types/agent";
import { useCreateFCE2BRuntime } from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
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
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { useT } from "../../i18n";

const DEFAULT_NAME = "FC-Hermes";

export function FCE2BRuntimeDialog({ onClose }: { onClose: () => void }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const createRuntime = useCreateFCE2BRuntime(wsId);
  const [name, setName] = useState(DEFAULT_NAME);
  const [visibility, setVisibility] = useState<RuntimeVisibility>("private");

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      await createRuntime.mutateAsync({
        name: name.trim() || DEFAULT_NAME,
        visibility,
      });
      toast.success(t(($) => $.fc_e2b_runtime.toast_created));
      onClose();
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.fc_e2b_runtime.toast_create_failed),
      );
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <Cloud className="h-4 w-4 text-muted-foreground" />
            {t(($) => $.fc_e2b_runtime.title)}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {t(($) => $.fc_e2b_runtime.description)}
          </DialogDescription>
        </DialogHeader>

        <form
          id="fc-e2b-runtime-form"
          onSubmit={handleSubmit}
          className="space-y-4"
        >
          <div className="space-y-1.5">
            <Label htmlFor="fc-e2b-runtime-name" className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.name)}
            </Label>
            <Input
              id="fc-e2b-runtime-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
              placeholder={DEFAULT_NAME}
            />
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">
              {t(($) => $.fc_e2b_runtime.fields.visibility)}
            </Label>
            <Select
              value={visibility}
              onValueChange={(value) => setVisibility(value as RuntimeVisibility)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="private">
                  {t(($) => $.detail.visibility_label.private)}
                </SelectItem>
                <SelectItem value="public">
                  {t(($) => $.detail.visibility_label.public)}
                </SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.fc_e2b_runtime.visibility_hint)}
            </p>
          </div>
        </form>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={onClose}
            disabled={createRuntime.isPending}
          >
            {t(($) => $.fc_e2b_runtime.cancel)}
          </Button>
          <Button
            type="submit"
            form="fc-e2b-runtime-form"
            disabled={createRuntime.isPending}
          >
            {createRuntime.isPending && (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            )}
            {createRuntime.isPending
              ? t(($) => $.fc_e2b_runtime.creating)
              : t(($) => $.fc_e2b_runtime.create)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
