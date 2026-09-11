"use client";

import type { AgentSourceFileChange } from "@multica/core/types";
import { useT } from "../../../i18n";

export function ChangeList({ title, changes }: { title: string; changes: AgentSourceFileChange[] }) {
  const { t } = useT("agents");
  return (
    <div className="space-y-2">
      <h3 className="text-body font-medium">{title} ({changes.length})</h3>
      {changes.map((change) => (
        <details key={change.path} className="rounded-md border">
          <summary className="cursor-pointer break-all px-3 py-2 font-mono text-caption">{change.status} · {change.path}</summary>
          {change.before_mode !== change.after_mode && <p className="px-3 text-caption text-muted-foreground">{change.before_mode || "—"} → {change.after_mode || "—"}</p>}
          <div className="grid min-w-0 gap-3 p-3 md:grid-cols-2">
            <div className="min-w-0 space-y-1"><p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.before)}</p><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-caption">{change.before ?? "—"}</pre></div>
            <div className="min-w-0 space-y-1"><p className="text-caption text-muted-foreground">{t(($) => $.tab_body.publish.after)}</p><pre className="max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-caption">{change.after ?? "—"}</pre></div>
          </div>
        </details>
      ))}
    </div>
  );
}
