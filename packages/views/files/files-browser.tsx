"use client";

import { useEffect, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import type { FilesystemEntry } from "@multica/core/filesystem";
import {
  filesystemContentOptions,
  filesystemEntriesOptions,
  useFilesystemDelete,
  useFilesystemDownload,
  useFilesystemMkdir,
  useFilesystemRename,
  useFilesystemUploadBatch,
} from "@multica/core/filesystem";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import {
  ListGrid,
  ListGridBody,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
} from "@multica/ui/components/ui/list-grid";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { Download, File, Folder, FolderPlus, MoreHorizontal, Pencil, Trash2, Upload, X } from "lucide-react";
import { toast } from "sonner";
import { useT } from "../i18n";
import { AppLink } from "../navigation";

const GRID_COLS = "grid-cols-[0.75rem_minmax(8rem,1fr)_5.5rem_5rem_2rem]";
const PREVIEW_MAX_BYTES = 2 * 1024 * 1024;
const FOLDER_UPLOAD_MAX_FILES = 200;

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|bmp)$/i;
const TEXT_EXT = /\.(txt|md|markdown|json|csv|xml|ya?ml|html?|css|js|ts|tsx|jsx|go|py|sh|log|env)$/i;

export type DiskInfo = {
  name: string;
  kind: "shared" | "agent";
  access?: string;
};

export function FileBrowser({
  wsId,
  root,
  disk,
  canWrite,
  agentHref,
}: {
  wsId: string;
  root: string;
  disk: DiskInfo;
  canWrite: boolean;
  agentHref: string | null;
}) {
  const { t } = useT("layout");
  const [path, setPath] = useState(".");
  const [folderOpen, setFolderOpen] = useState(false);
  const [folderName, setFolderName] = useState("");
  const [selected, setSelected] = useState<FilesystemEntry | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const folderRef = useRef<HTMLInputElement>(null);
  const listing = useQuery(filesystemEntriesOptions(wsId, root, path));
  const mkdir = useFilesystemMkdir(wsId);
  const uploadBatch = useFilesystemUploadBatch(wsId);
  const download = useFilesystemDownload();
  const rename = useFilesystemRename(wsId);
  const remove = useFilesystemDelete(wsId);
  const entries = listing.data?.entries ?? [];
  const relPath = path === "." ? "" : path;

  const uploadMany = async (files: File[]) => {
    if (files.length === 0) return;
    if (files.length > FOLDER_UPLOAD_MAX_FILES) {
      toast.error(t(($) => $.files.folder_upload_limit));
      return;
    }
    setBusy(true);
    try {
      await uploadBatch.mutateAsync(
        files.map((file) => {
          const relative = file.webkitRelativePath || file.name;
          const parts = relative.split("/").filter(Boolean);
          const filename = parts[parts.length - 1] ?? file.name;
          const dest = [relPath, ...parts.slice(0, -1)].filter(Boolean).join("/");
          return { root, path: dest, file, filename };
        }),
      );
      toast.success(t(($) => $.files.upload_done));
    } catch {
      toast.error(t(($) => $.files.upload_failed));
    } finally {
      setBusy(false);
    }
  };

  const startRename = (entry: FilesystemEntry) => {
    setRenaming(entry.path);
    setRenameValue(entry.name);
  };

  const commitRename = (entry: FilesystemEntry) => {
    const name = renameValue.trim();
    setRenaming(null);
    if (!name || name === entry.name) return;
    void rename.mutateAsync({ root, path: entry.path, name }).then(() => {
      if (selected?.path === entry.path) {
        const parent = entry.path.includes("/") ? entry.path.slice(0, entry.path.lastIndexOf("/")) : "";
        setSelected({ ...entry, name, path: parent ? `${parent}/${name}` : name });
      }
    }).catch(() => toast.error(t(($) => $.files.rename_failed)));
  };

  const commitDelete = (entry: FilesystemEntry) => {
    if (!window.confirm(t(($) => $.files.delete_confirm, { name: entry.name }))) return;
    void remove.mutateAsync({ root, path: entry.path }).then(() => {
      if (selected?.path === entry.path) setSelected(null);
    }).catch(() => toast.error(t(($) => $.files.delete_failed)));
  };

  const downloadEntry = (entry: FilesystemEntry) => {
    void download.mutateAsync({ root, path: entry.path }).then((blob) => {
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = entry.name;
      link.click();
      URL.revokeObjectURL(url);
    }).catch(() => toast.error(t(($) => $.files.download_failed)));
  };

  return (
    <>
      <div className="flex h-10 shrink-0 items-center gap-2 border-b px-4">
        <Folder aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        <nav aria-label="breadcrumb" className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden text-body">
          <button type="button" className="truncate font-medium hover:underline" onClick={() => { setPath("."); setSelected(null); }}>
            {disk.name}
          </button>
          {(path === "." || path === "" ? [] : path.split("/")).map((segment, index, all) => {
            const next = all.slice(0, index + 1).join("/");
            return (
              <span key={next} className="flex min-w-0 items-center gap-1">
                <span className="text-muted-foreground">/</span>
                <button type="button" className="truncate hover:underline" onClick={() => { setPath(next); setSelected(null); }}>
                  {segment}
                </button>
              </span>
            );
          })}
        </nav>
        {canWrite ? (
          <>
            <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2" onClick={() => { setFolderName(""); setFolderOpen(true); }}>
              <FolderPlus aria-hidden="true" className="size-3.5" />
              {t(($) => $.files.new_folder)}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2" disabled={busy} onClick={() => fileRef.current?.click()}>
              <Upload aria-hidden="true" className="size-3.5" />
              {t(($) => $.files.upload)}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2" disabled={busy} onClick={() => folderRef.current?.click()}>
              <Folder aria-hidden="true" className="size-3.5" />
              {t(($) => $.files.upload_folder)}
            </Button>
            <input ref={fileRef} type="file" className="hidden" multiple onChange={(event) => {
              const list = event.target.files ? [...event.target.files] : [];
              event.target.value = "";
              void uploadMany(list);
            }} />
            <input ref={folderRef} type="file" className="hidden" multiple
              // @ts-expect-error webkitdirectory is not in the React type
              webkitdirectory=""
              onChange={(event) => {
                const list = event.target.files ? [...event.target.files] : [];
                event.target.value = "";
                void uploadMany(list);
              }} />
          </>
        ) : null}
        {agentHref ? (
          <AppLink href={agentHref} className="shrink-0 text-caption text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
            {t(($) => $.files.open_agent)}
          </AppLink>
        ) : null}
      </div>
      {folderOpen ? (
        <form className="flex shrink-0 items-center gap-2 border-b px-4 py-2" onSubmit={(event) => {
          event.preventDefault();
          const name = folderName.trim();
          if (!name) return;
          const next = relPath ? `${relPath}/${name}` : name;
          void mkdir.mutateAsync({ root, path: next }).then(() => { setFolderOpen(false); setFolderName(""); }).catch(() => toast.error(t(($) => $.files.create_failed)));
        }}>
          <Input value={folderName} onChange={(event) => setFolderName(event.target.value)} aria-label={t(($) => $.files.folder_name)} placeholder={t(($) => $.files.folder_name)} className="h-8 max-w-xs" autoFocus />
          <Button type="submit" size="sm" disabled={mkdir.isPending || !folderName.trim()}>{t(($) => $.files.create_folder)}</Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setFolderOpen(false)}>{t(($) => $.files.cancel)}</Button>
        </form>
      ) : null}
      <div
        className={cn("flex min-h-0 flex-1", dragging && "bg-accent/30")}
        onDragOver={(event: DragEvent) => { if (!canWrite) return; event.preventDefault(); setDragging(true); }}
        onDragLeave={() => setDragging(false)}
        onDrop={(event: DragEvent) => {
          if (!canWrite) return;
          event.preventDefault();
          setDragging(false);
          void collectDroppedFiles(event.dataTransfer).then((files) => uploadMany(files));
        }}
      >
        <div className="@container flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto">
          <ListGrid className={GRID_COLS}>
            <ListGridHeader>
              <ListGridHeaderCell>{t(($) => $.files.col_name)}</ListGridHeaderCell>
              <ListGridHeaderCell>{t(($) => $.files.col_size)}</ListGridHeaderCell>
              <ListGridHeaderCell>{t(($) => $.files.col_kind)}</ListGridHeaderCell>
              <ListGridHeaderCell />
            </ListGridHeader>
            <ListGridBody>
              {entries.map((entry) => (
                <ListGridRow
                  key={entry.path}
                  data-active={selected?.path === entry.path || undefined}
                  className={cn("cursor-pointer", selected?.path === entry.path && "bg-accent font-medium hover:bg-accent")}
                  onClick={() => {
                    if (renaming === entry.path) return;
                    if (entry.is_dir) {
                      setPath(entry.path);
                      setSelected(null);
                      return;
                    }
                    setSelected(entry);
                  }}
                  onDoubleClick={() => {
                    if (entry.is_dir) return;
                    setSelected(entry);
                  }}
                  onKeyDown={(event: KeyboardEvent<HTMLDivElement>) => {
                    if (event.key === "F2" && canWrite) {
                      event.preventDefault();
                      startRename(entry);
                    }
                    if ((event.key === "Delete" || event.key === "Backspace") && canWrite) {
                      event.preventDefault();
                      commitDelete(entry);
                    }
                  }}
                >
                  <ListGridCell className="gap-2">
                    {entry.is_dir ? <Folder aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" /> : <File aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />}
                    {renaming === entry.path ? (
                      <Input
                        value={renameValue}
                        autoFocus
                        className="h-7"
                        onClick={(event) => event.stopPropagation()}
                        onChange={(event) => setRenameValue(event.target.value)}
                        onBlur={() => commitRename(entry)}
                        onKeyDown={(event) => {
                          if (event.key === "Enter") {
                            event.preventDefault();
                            commitRename(entry);
                          }
                          if (event.key === "Escape") setRenaming(null);
                        }}
                      />
                    ) : (
                      <span className="truncate">{entry.name}</span>
                    )}
                  </ListGridCell>
                  <ListGridCell className="text-caption text-muted-foreground">
                    {entry.is_dir ? "—" : formatBytes(entry.size_bytes ?? 0)}
                  </ListGridCell>
                  <ListGridCell className="text-caption text-muted-foreground">
                    {entry.is_dir ? t(($) => $.files.kind_folder) : t(($) => $.files.kind_file)}
                  </ListGridCell>
                  <ListGridCell>
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={<Button type="button" size="icon-sm" variant="ghost" className="size-7" onClick={(event) => event.stopPropagation()} />}
                      >
                        <MoreHorizontal className="size-3.5" />
                        <span className="sr-only">{t(($) => $.files.more)}</span>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="w-36">
                        {canWrite ? (
                          <DropdownMenuItem onClick={() => startRename(entry)}>
                            <Pencil className="size-3.5" />
                            {t(($) => $.files.rename)}
                          </DropdownMenuItem>
                        ) : null}
                        {!entry.is_dir ? (
                          <DropdownMenuItem onClick={() => downloadEntry(entry)}>
                            <Download className="size-3.5" />
                            {t(($) => $.files.download)}
                          </DropdownMenuItem>
                        ) : null}
                        {canWrite ? (
                          <>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem variant="destructive" onClick={() => commitDelete(entry)}>
                              <Trash2 className="size-3.5" />
                              {t(($) => $.files.delete)}
                            </DropdownMenuItem>
                          </>
                        ) : null}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </ListGridCell>
                </ListGridRow>
              ))}
            </ListGridBody>
          </ListGrid>
          {listing.isPending || busy ? (
            <div className="p-4"><Skeleton className="h-8 w-full" /></div>
          ) : entries.length === 0 ? (
            <div className="flex flex-1 flex-col items-center justify-center gap-2 px-6 py-10">
              <Folder aria-hidden="true" className="size-8 text-muted-foreground" />
              <p className="text-body font-medium">{t(($) => $.files.empty_folder)}</p>
              <p className="max-w-sm text-center text-caption text-muted-foreground">
                {canWrite ? t(($) => $.files.empty_folder_hint) : t(($) => $.files.empty_folder)}
              </p>
            </div>
          ) : null}
        </div>
        {selected && !selected.is_dir ? (
          <FilePreview
            entry={selected}
            root={root}
            onClose={() => setSelected(null)}
            onDownload={() => downloadEntry(selected)}
          />
        ) : null}
      </div>
    </>
  );
}

function FilePreview({
  entry,
  root,
  onClose,
  onDownload,
}: {
  entry: FilesystemEntry;
  root: string;
  onClose: () => void;
  onDownload: () => void;
}) {
  const { t } = useT("layout");
  const content = useQuery(filesystemContentOptions(root, entry.path, canPreview(entry)));
  const [preview, setPreview] = useState<PreviewData | undefined>(undefined);
  useEffect(() => {
    if (!content.data) {
      setPreview(undefined);
      return;
    }
    let cancelled = false;
    const blob = content.data;
    if (IMAGE_EXT.test(entry.name)) {
      const url = URL.createObjectURL(blob);
      setPreview({ kind: "image", url });
      return () => {
        cancelled = true;
        URL.revokeObjectURL(url);
      };
    }
    void blob.text().then((text) => {
      if (cancelled) return;
      setPreview({ kind: /\.html?$/i.test(entry.name) ? "html" : "text", text });
    });
    return () => { cancelled = true; };
  }, [content.data, entry.name]);
  return (
    <section className="flex min-w-0 flex-[1.4] flex-col border-l bg-background">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
        <p className="min-w-0 flex-1 truncate text-body font-medium">{entry.name}</p>
        <span className="shrink-0 text-caption text-muted-foreground">{formatBytes(entry.size_bytes ?? 0)}</span>
        <Button type="button" size="sm" variant="outline" className="h-7 gap-1" onClick={onDownload}>
          <Download aria-hidden="true" className="size-3.5" />
          {t(($) => $.files.download)}
        </Button>
        <Button type="button" size="icon-sm" variant="ghost" className="size-7" onClick={onClose} aria-label={t(($) => $.files.cancel)}>
          <X className="size-3.5" />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto bg-muted/30 p-4">
        {canPreview(entry) ? (
          content.isPending ? <Skeleton className="h-full min-h-64 w-full" /> : <PreviewBody entry={entry} data={preview} />
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
            <File className="size-10 text-muted-foreground" />
            <p className="text-body">{t(($) => $.files.preview_unavailable)}</p>
            {entry.sha256 ? <p className="max-w-sm break-all font-mono text-caption text-muted-foreground">{entry.sha256}</p> : null}
          </div>
        )}
      </div>
    </section>
  );
}

function canPreview(entry: FilesystemEntry): boolean {
  const size = entry.size_bytes ?? 0;
  if (size <= 0 || size > PREVIEW_MAX_BYTES) return false;
  return IMAGE_EXT.test(entry.name) || TEXT_EXT.test(entry.name);
}

function PreviewBody({ entry, data }: { entry: FilesystemEntry; data?: PreviewData }) {
  if (!data) return null;
  if (data.kind === "image") {
    return <img src={data.url} alt={entry.name} className="mx-auto max-h-full max-w-full object-contain" />;
  }
  if (data.kind === "html") {
    return (
      <iframe
        title={entry.name}
        sandbox="allow-same-origin"
        srcDoc={data.text}
        className="h-full min-h-[28rem] w-full rounded-md border bg-white"
      />
    );
  }
  return <pre className="h-full overflow-auto whitespace-pre-wrap break-all rounded-md bg-background p-4 text-body">{data.text}</pre>;
}

type PreviewData = { kind: "image"; url: string } | { kind: "text" | "html"; text: string };

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

async function collectDroppedFiles(dt: DataTransfer): Promise<File[]> {
  const files: File[] = [];
  const items = [...dt.items];
  const walk = async (entry: FileSystemEntry, prefix: string) => {
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) => {
        (entry as FileSystemFileEntry).file(resolve, reject);
      });
      Object.defineProperty(file, "webkitRelativePath", { value: prefix + file.name });
      files.push(file);
      return;
    }
    if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      const readBatch = async () => {
        const batch = await new Promise<FileSystemEntry[]>((resolve) => reader.readEntries(resolve));
        if (batch.length === 0) return;
        for (const child of batch) await walk(child, prefix + entry.name + "/");
        await readBatch();
      };
      await readBatch();
    }
  };
  let usedEntries = false;
  for (const item of items) {
    const entry = item.webkitGetAsEntry?.();
    if (entry) {
      usedEntries = true;
      await walk(entry, "");
    }
  }
  if (!usedEntries) files.push(...dt.files);
  return files;
}
