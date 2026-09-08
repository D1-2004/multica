"use client";

import { toast } from "sonner";
import { useExportAgent } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

export function ExportTab({ agentId, canEdit }: { agentId: string; canEdit: boolean }) {
  const { t } = useT("agents");
  const mutation = useExportAgent(agentId);
  const download = async () => {
    try {
      const blob = await mutation.mutateAsync();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = `agent-${agentId}-source.zip`;
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.tab_body.export.failed));
    }
  };
  return (
    <div className="space-y-4">
      <p className="text-body text-muted-foreground">{t(($) => $.tab_body.export.description)}</p>
      <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.export.hint)}</p>
      {!canEdit && <p className="text-caption text-muted-foreground">{t(($) => $.tab_body.export.unavailable)}</p>}
      <Button disabled={!canEdit || mutation.isPending} onClick={() => void download()}>
        {mutation.isPending ? t(($) => $.tab_body.export.downloading) : t(($) => $.tab_body.export.download)}
      </Button>
    </div>
  );
}
