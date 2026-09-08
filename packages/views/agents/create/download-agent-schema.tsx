"use client";

import { Download } from "lucide-react";
import { toast } from "sonner";
import { useDownloadAgentSchema } from "@multica/core/agents";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

export function DownloadAgentSchema() {
  const { t } = useT("agents");
  const mutation = useDownloadAgentSchema();
  const download = async () => {
    try {
      const blob = await mutation.mutateAsync();
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "agent.schema.json";
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      toast.error(t(($) => $.creation_studio.schema.failed));
    }
  };
  return <Button type="button" variant="outline" size="sm" disabled={mutation.isPending} onClick={() => void download()}>
    <Download className="size-4" aria-hidden="true" />
    {mutation.isPending ? t(($) => $.creation_studio.schema.downloading) : t(($) => $.creation_studio.schema.download)}
  </Button>;
}
