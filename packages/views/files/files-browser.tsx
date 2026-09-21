"use client";

import { useEffect, useRef, useState, type DragEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import type { FilesystemEntry } from "@multica/core/filesystem";
import {
  filesystemContentOptions,
  filesystemEntriesOptions,
  useFilesystemDownload,
  useFilesystemMkdir,
  useFilesystemUpload,
} from "@multica/core/filesystem";
import { Button } from "@multica/ui/components/ui/button";
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
import { Download, File, Folder, FolderPlus, Upload } from "lucide-react";
import { toast } from "sonner";
import { useT } from "../i18n";
import { AppLink } from "../navigation";

const GRID_COLS = "grid-cols-[0.75rem_minmax(8rem,1fr)_5.5rem_5rem_0.75rem]";
const PREVIEW_MAX_BYTES = 2 * 1024 * 1024;
const FOLDER_UPLOAD_MAX_FILES = 100;

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
  const [dragging, setDragging] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const folderRef = useRef<HTMLInputElement>(null);
  const listing = useQuery(filesystemEntriesOptions(wsId, root, path));
  const mkdir = useFilesystemMkdir(wsId);
  const upload = useFilesystemUpload(wsId);
  const download = useFilesystemDownload();
  const entries = listing.data?.entries ?? [];
  const relPath = path === "." ? "" : path;

  const uploadOne = async (file: File, destDir: string, filename: string) => {
    await upload.mutateAsync({ root, path: destDir, file, filename });
  };

  const uploadMany = async (files: File[]) => {
    if (files.length === 0) return;
    if (files.length > FOLDER_UPLOAD_MAX_FILES) {
      toast.error(t(($) => $.files.upload_folder_too_many));
      return;
    }
    const dirs = new Set<string>();
    for (const file of files) {
      const relative = file.webkitRelativePath || file.name;
      const parts = relative.split("/").filter(Boolean);
      let acc = relPath;
      for (let i = 0; i < parts.length - 1; i++) {
        acc = acc ? `${acc}/${parts[i]}` : parts[i];
        dirs.add(acc);
      }
    }
    const ordered = [...dirs].sort((a, b) => a.split("/").length - b.split("/").length);
    for (const dir of ordered) {
      try {
        await mkdir.mutateAsync({ root, path: dir });
      } catch {
        // Parent may already exist.
      }
    }
    for (const file of files) {
      const relative = file.webkitRelativePath || file.name;
      const parts = relative.split("/").filter(Boolean);
      const filename = parts[parts.length - 1] ?? file.name;
      const dirParts = parts.slice(0, -1);
      const dest = [relPath, ...dirParts].filter(Boolean).join("/");
      try {
        await uploadOne(file, dest, filename);
      } catch {
        toast.error(t(($) => $.files.upload_failed));
        return;
      }
    }
    toast.success(t(($) => $.files.upload_done));
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
            <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2" onClick={() => fileRef.current?.click()}>
              <Upload aria-hidden="true" className="size-3.5" />
              {t(($) => $.files.upload)}
            </Button>
            <Button type="button" size="sm" variant="outline" className="h-7 gap-1 px-2" onClick={() => folderRef.current?.click()}>
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
            </ListGridHeader>
            <ListGridBody>
              {entries.map((entry) => (
                <ListGridRow
                  key={entry.path}
                  data-active={selected?.path === entry.path || undefined}
                  className={cn("cursor-pointer", selected?.path === entry.path && "bg-accent font-medium hover:bg-accent")}
                  onClick={() => {
                    if (entry.is_dir) {
                      setPath(entry.path);
                      setSelected(null);
                      return;
                    }
                    setSelected(entry);
                  }}
                >
                  <ListGridCell className="gap-2">
                    {entry.is_dir ? <Folder aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" /> : <File aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />}
                    <span className="truncate">{entry.name}</span>
                  </ListGridCell>
                  <ListGridCell className="text-caption text-muted-foreground">
                    {entry.is_dir ? "—" : formatBytes(entry.size_bytes ?? 0)}
                  </ListGridCell>
                  <ListGridCell className="text-caption text-muted-foreground">
                    {entry.is_dir ? t(($) => $.files.kind_folder) : t(($) => $.files.kind_file)}
                  </ListGridCell>
                </ListGridRow>
              ))}
            </ListGridBody>
          </ListGrid>
          {listing.isPending ? (
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
          <FileInspector
            entry={selected}
            root={root}
            download={() =>
              download.mutateAsync({ root, path: selected.path }).then((blob) => {
                const url = URL.createObjectURL(blob);
                const link = document.createElement("a");
                link.href = url;
                link.download = selected.name;
                link.click();
                URL.revokeObjectURL(url);
              }).catch(() => toast.error(t(($) => $.files.download_failed)))
            }
          />
        ) : null}
      </div>
    </>
  );
}

function FileInspector({
  entry,
  root,
  download,
}: {
  entry: FilesystemEntry;
  root: string;
  download: () => void;
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
      setPreview({ kind: "image", url: URL.createObjectURL(blob) });
      return () => { cancelled = true; };
    }
    void blob.text().then((text) => {
      if (cancelled) return;
      setPreview({ kind: /\.html?$/i.test(entry.name) ? "html" : "text", text });
    });
    return () => { cancelled = true; };
  }, [content.data, entry.name]);
  return (
    <aside className="flex w-72 shrink-0 flex-col overflow-y-auto border-l p-4">
      <p className="truncate text-body font-medium">{entry.name}</p>
      <dl className="mt-3 space-y-2 text-caption text-muted-foreground">
        <div>
          <dt>{t(($) => $.files.col_size)}</dt>
          <dd className="text-foreground">{formatBytes(entry.size_bytes ?? 0)}</dd>
        </div>
        {entry.modified_at ? (
          <div>
            <dt>{t(($) => $.files.col_modified)}</dt>
            <dd className="text-foreground">{entry.modified_at.replace("T", " ").replace("Z", " UTC")}</dd>
          </div>
        ) : null}
        {entry.sha256 ? (
          <div>
            <dt>SHA-256</dt>
            <dd className="break-all font-mono text-[11px] text-foreground">{entry.sha256}</dd>
          </div>
        ) : null}
        <div>
          <dt>{t(($) => $.files.col_kind)}</dt>
          <dd className="text-foreground">{t(($) => $.files.kind_file)}</dd>
        </div>
      </dl>
      <Button type="button" size="sm" className="mt-4 gap-1" onClick={() => download()}>
        <Download aria-hidden="true" className="size-3.5" />
        {t(($) => $.files.download)}
      </Button>
      {canPreview(entry) ? (
        <div className="mt-4 min-h-0 flex-1">
          <p className="mb-2 text-caption font-medium text-muted-foreground">{t(($) => $.files.preview)}</p>
          {content.isPending ? <Skeleton className="h-32 w-full" /> : <PreviewBody entry={entry} data={preview} />}
        </div>
      ) : (
        <p className="mt-4 text-caption text-muted-foreground">{t(($) => $.files.preview_unavailable)}</p>
      )}
    </aside>
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
    return <img src={data.url} alt={entry.name} className="max-h-64 w-full rounded-md object-contain" />;
  }
  if (data.kind === "html") {
    return <iframe title={entry.name} sandbox="" srcDoc={data.text} className="h-64 w-full rounded-md border bg-background" />;
  }
  return <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-2 text-caption">{data.text}</pre>;
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
      const named = new File([file], file.name, { type: file.type, lastModified: file.lastModified });
      Object.defineProperty(named, "webkitRelativePath", { value: prefix + file.name });
      files.push(named);
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


