"use client";

import { useMemo, useState } from "react";
import { AlertCircle, ArrowLeft, Blocks, FileQuestion } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  dshPluginDetailOptions,
  dshPluginFileOptions,
  dshPluginFilesOptions,
} from "@multica/core/dsh-plugins";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { FileTree } from "../../skills/components/file-tree";
import { FileViewer, isMarkdownPath } from "../../skills/components/file-viewer";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

/**
 * What is actually inside an imported plugin.
 *
 * A plugin is code that runs beside the agent's own tools, so being able to
 * read it without leaving Multica matters — especially for an uploaded package,
 * which exists nowhere else to go and look at. The bytes shown here are read
 * from the stored package, so they are by construction the bytes the sandbox
 * runs.
 */
export function DshPluginDetailPage({ pluginId }: { pluginId: string }) {
  const { t } = useT("dsh-plugins");
  const wsId = useWorkspaceId();
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const [selectedPath, setSelectedPath] = useState("");

  const plugin = useQuery(dshPluginDetailOptions(wsId, pluginId));
  const listing = useQuery(dshPluginFilesOptions(wsId, pluginId));

  const filePaths = useMemo(
    () => (listing.data?.files ?? []).map((file) => file.path),
    [listing.data],
  );
  const selected = useMemo(
    () => (listing.data?.files ?? []).find((file) => file.path === selectedPath),
    [listing.data, selectedPath],
  );
  // Only fetch a body once a viewable file is chosen; the listing already told
  // us which ones the server would refuse.
  const file = useQuery(
    dshPluginFileOptions(wsId, pluginId, selected?.viewable ? selectedPath : ""),
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Blocks}
        title={plugin.data?.packageName || t(($) => $.page.title)}
        description={plugin.data?.description}
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigation.push(paths.dshPlugins())}
          >
            <ArrowLeft />
            {t(($) => $.detail.back)}
          </Button>
        }
      />

      <div className="flex flex-wrap items-center gap-2 border-b px-5 py-2">
        {plugin.data?.resolvedVersion ? (
          <Badge variant="secondary" className="font-mono tabular-nums">
            {plugin.data.resolvedVersion}
          </Badge>
        ) : null}
        {plugin.data?.sourceKind ? (
          <Badge variant="outline">{plugin.data.sourceKind}</Badge>
        ) : null}
        {plugin.data?.bundleRows?.length ? (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.detail.rows, { rows: plugin.data.bundleRows.join(", ") })}
          </span>
        ) : null}
        {plugin.data?.integrity ? (
          <span className="truncate font-mono text-caption text-muted-foreground">
            {plugin.data.integrity}
          </span>
        ) : null}
      </div>

      {listing.isPending ? (
        <div className="space-y-2 p-5">
          {Array.from({ length: 6 }, (_, index) => (
            <Skeleton key={index} className="h-4 w-64" />
          ))}
        </div>
      ) : listing.isError ? (
        <CollectionPageState
          icon={AlertCircle}
          tone="destructive"
          title={t(($) => $.detail.files_error)}
          description={t(($) => $.detail.files_error_hint)}
        />
      ) : filePaths.length === 0 ? (
        <CollectionPageState
          icon={FileQuestion}
          title={t(($) => $.detail.no_files)}
          description={t(($) => $.detail.no_files_hint)}
        />
      ) : (
        <div className="flex min-h-0 flex-1">
          <aside className="w-72 shrink-0 overflow-y-auto border-r p-2">
            <FileTree
              filePaths={filePaths}
              selectedPath={selectedPath}
              onSelect={setSelectedPath}
            />
            {listing.data?.truncated ? (
              <p className="px-2 py-2 text-caption text-muted-foreground">
                {t(($) => $.detail.truncated)}
              </p>
            ) : null}
          </aside>

          <section className="min-w-0 flex-1 overflow-y-auto p-4">
            {!selectedPath ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.detail.pick_a_file)}
              </p>
            ) : selected && !selected.viewable ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.detail.not_viewable)}
              </p>
            ) : file.isPending ? (
              <Skeleton className="h-64 w-full" />
            ) : file.isError ? (
              <p role="alert" className="text-caption text-destructive">
                {t(($) => $.detail.file_error)}
              </p>
            ) : (
              <FileViewer
                path={selectedPath}
                content={file.data?.content ?? ""}
                mode={isMarkdownPath(selectedPath) ? "preview" : "raw"}
                readOnly
                onChange={() => {
                  // A stored package is immutable; the viewer is read-only.
                }}
              />
            )}
          </section>
        </div>
      )}
    </div>
  );
}
