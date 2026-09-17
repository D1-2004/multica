"use client";

import { useInfiniteQuery } from "@tanstack/react-query";
import { agentPublicationsOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { PackageError } from "../../create/package-error";
import { useT } from "../../../i18n";

export function AgentPublicationHistory({ agentId, disabled, onRestore }: { agentId: string; disabled: boolean; onRestore: (id: string) => void }) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const history = useInfiniteQuery(agentPublicationsOptions(workspaceId, agentId));
  const publications = history.data?.pages.flatMap((page) => page.publications) ?? [];
  return <section className="space-y-4 border-t pt-6" aria-label={t(($) => $.publication_history.title)}>
    <h3 className="text-title-sm font-medium">{t(($) => $.publication_history.title)}</h3>
    <p className="text-caption text-muted-foreground">{t(($) => $.publication_history.description)}</p>
    <PackageError error={history.error} />
    {history.isPending && <p>{t(($) => $.tab_body.publish.loading)}</p>}
    {history.isSuccess && !publications.length && <p className="text-body text-muted-foreground">{t(($) => $.publication_history.empty)}</p>}
    {publications.map((item, index) => <article key={item.id} className="flex flex-wrap items-start justify-between gap-3 rounded-md border p-4">
      <div className="min-w-0 space-y-1 text-caption">
        <p className="break-all font-mono">{item.ref || item.source_type} · {item.commit_sha.slice(0, 12)}</p>
        <p className="text-muted-foreground">{new Date(item.published_at).toLocaleString()} · {item.author_name || item.published_by}</p>
        <p>{index === 0 ? `${t(($) => $.publication_history.latest)} · ` : ""}{item.rollback_of ? t(($) => $.publication_history.rollback) : item.initial_publication ? t(($) => $.publication_history.created) : t(($) => $.publication_history.published)}</p>
        {!item.has_configuration_snapshot && <p className="text-muted-foreground">{t(($) => $.publication_history.legacy)}</p>}
      </div>
      {item.source_type === "git" && <Button variant="outline" disabled={disabled} onClick={() => onRestore(item.id)}>{t(($) => $.publication_history.restore)}</Button>}
    </article>)}
    {history.hasNextPage && <Button variant="outline" disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>{t(($) => $.publication_history.more)}</Button>}
  </section>;
}
