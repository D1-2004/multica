"use client";

import { useQuery } from "@tanstack/react-query";
import { filesystemRootsOptions } from "@multica/core/filesystem";
import { useWorkspaceId } from "@multica/core/hooks";
import { File } from "lucide-react";
import { CollectionPageHeader } from "../layout/collection-page";
import { useT } from "../i18n";

export function FilesPage() {
  const { t } = useT("layout");
  const wsId = useWorkspaceId();
  const roots = useQuery(filesystemRootsOptions(wsId ?? ""));
  const shared = roots.data?.roots.find((root) => root.kind === "shared");
  const agents = roots.data?.roots.filter((root) => root.kind === "agent") ?? [];
  return (
    <main className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-5xl px-4 py-6 sm:px-6 sm:py-8">
        <CollectionPageHeader icon={File} title={t(($) => $.nav.files)} />
        <p className="mt-2 text-body text-muted-foreground">
          {t(($) => $.files.intro)}
        </p>
        <section className="mt-6 space-y-4">
          <h2 className="text-title">{t(($) => $.files.shared)}</h2>
          <p className="text-caption text-muted-foreground">
            {roots.isPending
              ? t(($) => $.files.loading)
              : shared?.provisioned
                ? t(($) => $.files.shared_ready)
                : t(($) => $.files.shared_unprovisioned)}
          </p>
          <h2 className="text-title">{t(($) => $.files.agents)}</h2>
          {agents.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.files.agents_empty)}</p>
          ) : (
            <ul className="space-y-1 text-body">
              {agents.map((agent) => (
                <li key={agent.id} className="font-mono text-caption">{agent.id}</li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </main>
  );
}
