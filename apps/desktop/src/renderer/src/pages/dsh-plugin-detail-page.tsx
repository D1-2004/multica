import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { DshPluginDetailPage as SharedDshPluginDetailPage } from "@multica/views/dsh-plugins";
import { useWorkspaceId } from "@multica/core/hooks";
import { dshPluginDetailOptions } from "@multica/core/dsh-plugins";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function DshPluginDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  const { data: plugin } = useQuery(dshPluginDetailOptions(wsId, id ?? ""));

  useDocumentTitle(plugin?.packageName ?? "DSH Plugin");

  if (!id) return null;
  return <SharedDshPluginDetailPage pluginId={id} />;
}
