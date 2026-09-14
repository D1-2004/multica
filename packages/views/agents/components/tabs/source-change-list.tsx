"use client";

import { lazy, Suspense, useState } from "react";
import type { AgentSourceFileChange } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@multica/ui/components/ui/dialog";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@multica/ui/components/ui/resizable";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { FileTree } from "../../../skills/components/file-tree";
import { useT } from "../../../i18n";

const SourceFileDiff = lazy(() => import("./source-file-diff"));

export function SourceChangesDialog({ open, onOpenChange, groups }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  groups: { id: string; title: string; changes: AgentSourceFileChange[] }[];
}) {
  const { t } = useT("agents");
  const [selectedGroup, setSelectedGroup] = useState<string>();
  const [selectedPaths, setSelectedPaths] = useState<Record<string, string>>({});
  const [expandUnchanged, setExpandUnchanged] = useState(false);
  const activeGroup = groups.find((group) => group.id === selectedGroup) ?? groups[0];

  return <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent className="flex h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-none flex-col gap-3 overflow-hidden p-4 sm:max-w-none">
      <DialogTitle className="pr-8">{t(($) => $.tab_body.publish.preview)}</DialogTitle>
      <DialogDescription className="sr-only">{t(($) => $.package_diff.description)}</DialogDescription>
      <Tabs value={activeGroup?.id} onValueChange={(value) => setSelectedGroup(String(value))} className="min-h-0 min-w-0 flex-1">
        {groups.length > 1 ? <TabsList className="shrink-0">
          {groups.map((group) => <TabsTrigger key={group.id} value={group.id}>{group.title} ({group.changes.length})</TabsTrigger>)}
        </TabsList> : activeGroup && <p className="shrink-0 text-body font-medium">{activeGroup.title} ({activeGroup.changes.length})</p>}
        {groups.map((group) => {
          const selected = group.changes.find((change) => change.path === selectedPaths[group.id]) ?? group.changes[0];
          return <TabsContent key={group.id} value={group.id} className="min-h-0 min-w-0 overflow-hidden rounded-lg border">
            {selected ? <ChangeViewer changes={group.changes} selected={selected} onSelect={(path) => setSelectedPaths((current) => ({ ...current, [group.id]: path }))} expandUnchanged={expandUnchanged} onExpandUnchanged={setExpandUnchanged} /> :
              <p className="p-4 text-caption text-muted-foreground">{t(($) => $.package_diff.empty)}</p>}
          </TabsContent>;
        })}
      </Tabs>
    </DialogContent>
  </Dialog>;
}

function ChangeViewer({ changes, selected, onSelect, expandUnchanged, onExpandUnchanged }: {
  changes: AgentSourceFileChange[];
  selected: AgentSourceFileChange;
  onSelect: (path: string) => void;
  expandUnchanged: boolean;
  onExpandUnchanged: (expanded: boolean) => void;
}) {
  const { t } = useT("agents");
  const byPath = new Map(changes.map((change) => [change.path, change]));
  // null also represents withheld text. Only an added or removed file may
  // substitute an absent side with empty text.
  const hasText = (selected.before != null || selected.status === "added") && (selected.after != null || selected.status === "removed");

  return <ResizablePanelGroup orientation="horizontal">
    <ResizablePanel defaultSize="25%" minSize="100px" maxSize="45%" className="min-w-0">
      <div className="flex h-full flex-col border-r bg-muted/20">
        <p className="shrink-0 border-b px-3 py-2 text-caption font-medium">{t(($) => $.package_diff.files)} ({changes.length})</p>
        <div role="tablist" aria-label={t(($) => $.package_diff.files)} aria-orientation="vertical" className="min-h-0 flex-1 overflow-auto p-2">
          <FileTree filePaths={changes.map((change) => change.path)} selectedPath={selected.path} onSelect={onSelect} useFullPathLabels
            renderFileSuffix={(path) => <ChangeStatus status={byPath.get(path)?.status ?? ""} compact />} />
        </div>
      </div>
    </ResizablePanel>
    <ResizableHandle />
    <ResizablePanel minSize="160px" className="min-w-0">
      <div className="flex h-full min-w-0 flex-col">
        <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b px-3 py-2">
          <div className="flex min-w-0 items-center gap-2 text-caption"><code className="break-all">{selected.path}</code><ChangeStatus status={selected.status} /></div>
          {hasText && <Button variant="ghost" size="sm" aria-pressed={expandUnchanged} onClick={() => onExpandUnchanged(!expandUnchanged)}>
            {expandUnchanged ? t(($) => $.package_diff.collapse_unchanged) : t(($) => $.package_diff.expand_unchanged)}
          </Button>}
        </div>
        {selected.before_mode !== selected.after_mode && <p className="shrink-0 border-b px-3 py-2 font-mono text-caption text-muted-foreground">{selected.before_mode || "—"} → {selected.after_mode || "—"}</p>}
        <div role="tabpanel" aria-label={selected.path} tabIndex={0} className="min-h-0 min-w-0 flex-1 overflow-auto">
          <div className="min-w-[32rem]">
          <div className="sticky top-0 z-10 grid grid-cols-2 border-b bg-background text-caption text-muted-foreground">
            <span className="border-r px-3 py-2">{t(($) => $.package_diff.before)}</span>
            <span className="px-3 py-2">{t(($) => $.package_diff.after)}</span>
          </div>
          {hasText ? <Suspense fallback={<p role="status" className="p-4 text-caption text-muted-foreground">{t(($) => $.package_diff.loading)}</p>}>
            <SourceFileDiff path={selected.path} before={selected.before ?? ""} after={selected.after ?? ""} expandUnchanged={expandUnchanged} />
          </Suspense> : <div className="space-y-3 p-4 text-caption text-muted-foreground">
            <p>{t(($) => $.package_diff.unavailable)}</p>
            <div className="grid grid-cols-2 gap-3 font-mono"><code className="break-all">{selected.before_sha || "—"}</code><code className="break-all">{selected.after_sha || "—"}</code></div>
          </div>}
          </div>
        </div>
      </div>
    </ResizablePanel>
  </ResizablePanelGroup>;
}

function ChangeStatus({ status, compact = false }: { status: string; compact?: boolean }) {
  const { t } = useT("agents");
  let label: string;
  let marker: string;
  let color: string;
  switch (status) {
    case "added": label = t(($) => $.package_diff.added); marker = "A"; color = "text-success"; break;
    case "removed": label = t(($) => $.package_diff.removed); marker = "D"; color = "text-destructive"; break;
    case "modified": label = t(($) => $.package_diff.modified); marker = "M"; color = "text-warning"; break;
    default: label = status; marker = "•"; color = "text-muted-foreground";
  }
  return <span title={label} aria-hidden={compact || undefined} className={`shrink-0 text-caption font-medium ${compact ? "ml-auto font-mono" : ""} ${color}`}>{compact ? marker : label}</span>;
}
