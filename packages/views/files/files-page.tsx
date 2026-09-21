"use client";

import { useMemo, useState, type ReactNode } from "react";

import { useQuery } from "@tanstack/react-query";
import type { FilesystemRoot } from "@multica/core/filesystem";
import { filesystemRootsOptions } from "@multica/core/filesystem";
import { FileBrowser } from "./files-browser";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { agentListOptions } from "@multica/core/workspace/queries";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { File, Folder, HardDrive, Search } from "lucide-react";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { useT } from "../i18n";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../layout/collection-page";


type DiskKey = "shared" | `agent:${string}`;

type Disk = {
  key: DiskKey;
  kind: "shared" | "agent";
  name: string;
  provisioned: boolean;
  access?: string;
  agentId?: string;
  agent?: Agent;
};

function diskKey(root: FilesystemRoot): DiskKey | null {
  if (root.kind === "shared") return "shared";
  if (root.kind === "agent" && root.id) return `agent:${root.id}`;
  return null;
}

function avatarInitials(name: string): string {
  const chars = Array.from(name.trim());
  if (chars.length === 0) return "?";
  const useTwo = chars.length >= 2 && !/[\u4e00-\u9fff]/.test(name);
  return chars.slice(0, useTwo ? 2 : 1).join("").toUpperCase();
}

function nameMatches(name: string, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if (name.toLowerCase().includes(q)) return true;
  return matchesPinyin(name, q);
}

export function FilesPage() {
  const { t } = useT("layout");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const rootsQuery = useQuery(filesystemRootsOptions(wsId ?? ""));
  const agentsQuery = useQuery(agentListOptions(wsId ?? ""));
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<DiskKey | null>(null);

  const agentsById = useMemo(() => {
    const map = new Map<string, Agent>();
    for (const agent of agentsQuery.data ?? []) map.set(agent.id, agent);
    return map;
  }, [agentsQuery.data]);

  const disks = useMemo(() => {
    const next: Disk[] = [];
    for (const root of rootsQuery.data?.roots ?? []) {
      const key = diskKey(root);
      if (!key) continue;
      if (root.kind === "shared") {
        next.push({
          key,
          kind: "shared",
          name: t(($) => $.files.shared),
          provisioned: root.provisioned === true,
          access: root.access,
        });
        continue;
      }
      const agent = agentsById.get(root.id ?? "");
      const fallbackId = (root.id ?? "").slice(0, 8);
      next.push({
        key,
        kind: "agent",
        agentId: root.id,
        agent,
        name:
          agent?.name?.trim() ||
          t(($) => $.files.agent_fallback, { id: fallbackId }),
        provisioned: root.provisioned === true,
        access: root.access,
      });
    }
    return next;
  }, [agentsById, rootsQuery.data?.roots, t]);

  const visibleDisks = useMemo(
    () => disks.filter((disk) => nameMatches(disk.name, query)),
    [disks, query],
  );

  const activeKey =
    disks.find((disk) => disk.key === selected)?.key ?? disks[0]?.key ?? null;
  const selectedDisk = disks.find((disk) => disk.key === activeKey) ?? null;
  const sharedCount = disks.filter((disk) => disk.kind === "shared").length;
  const agentDisks = disks.filter((disk) => disk.kind === "agent");
  const visibleShared = visibleDisks.filter((disk) => disk.kind === "shared");
  const visibleAgents = visibleDisks.filter((disk) => disk.kind === "agent");
  const showSearch = agentDisks.length > 0;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CollectionPageHeader
        icon={File}
        title={t(($) => $.nav.files)}
        count={disks.length}
        description={t(($) => $.files.description)}
      />
      {rootsQuery.isError ? (
        <div className="flex flex-1 items-center justify-center">
          <CollectionPageState
            role="alert"
            icon={HardDrive}
            tone="destructive"
            title={t(($) => $.files.load_error)}
            actions={
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => void rootsQuery.refetch()}
              >
                {t(($) => $.files.retry)}
              </Button>
            }
          />
        </div>
      ) : rootsQuery.isPending ? (
        <FilesLoadingState />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col md:flex-row">
          <aside className="flex max-h-56 w-full shrink-0 flex-col border-b md:max-h-none md:w-64 md:border-b-0 md:border-r">
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto p-3">
              {showSearch ? (
                <div className="relative mb-3">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    aria-label={t(($) => $.files.search_disks)}
                    placeholder={t(($) => $.files.search_disks)}
                    className="h-8 pl-8 text-body"
                  />
                </div>
              ) : null}
              <div
                role="listbox"
                aria-label={t(($) => $.files.disks)}
                className="flex flex-col gap-4"
              >
                {sharedCount > 0 ? (
                  <DiskGroup label={t(($) => $.files.kind_shared)}>
                    {visibleShared.length === 0 ? (
                      <p className="px-2 text-caption text-muted-foreground">
                        {t(($) => $.files.filter_empty)}
                      </p>
                    ) : (
                      visibleShared.map((disk) => (
                        <DiskRow
                          key={disk.key}
                          disk={disk}
                          selected={disk.key === activeKey}
                          onSelect={setSelected}
                        />
                      ))
                    )}
                  </DiskGroup>
                ) : null}
                {agentDisks.length > 0 ? (
                  <DiskGroup label={t(($) => $.files.agents)}>
                    {visibleAgents.length === 0 ? (
                      <p className="px-2 text-caption text-muted-foreground">
                        {t(($) => $.files.filter_empty)}
                      </p>
                    ) : (
                      visibleAgents.map((disk) => (
                        <DiskRow
                          key={disk.key}
                          disk={disk}
                          selected={disk.key === activeKey}
                          onSelect={setSelected}
                        />
                      ))
                    )}
                  </DiskGroup>
                ) : null}
              </div>
            </div>
          </aside>
          <section className="flex min-h-0 min-w-0 flex-1 flex-col">
            {selectedDisk ? (
              <DiskPane
                disk={selectedDisk}
                wsId={wsId ?? ""}
                agentHref={
                  selectedDisk.agentId
                    ? `${paths.agentDetail(selectedDisk.agentId)}?view=filesystem`
                    : null
                }
              />
            ) : (
              <div className="flex flex-1 items-center justify-center">
                <CollectionPageState
                  icon={Folder}
                  title={t(($) => $.files.agents_empty)}
                  description={t(($) => $.files.agents_empty_hint)}
                />
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}

function DiskGroup({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1">
      <p className="px-2 text-caption font-medium text-muted-foreground">
        {label}
      </p>
      {children}
    </div>
  );
}

function DiskRow({
  disk,
  selected,
  onSelect,
}: {
  disk: Disk;
  selected: boolean;
  onSelect: (key: DiskKey) => void;
}) {
  const { t } = useT("layout");
  const meta =
    disk.provisioned !== true
      ? t(($) => $.files.status_unready)
      : disk.kind === "shared"
        ? t(($) => $.files.shared_hint)
        : disk.access === "write"
          ? t(($) => $.files.access_write)
          : disk.access === "read"
            ? t(($) => $.files.access_read)
            : disk.access === "none"
              ? t(($) => $.files.access_none)
              : t(($) => $.files.kind_private);
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      data-active={selected || undefined}
      onClick={() => onSelect(disk.key)}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-body outline-none",
        "focus-visible:ring-3 focus-visible:ring-ring/50",
        selected
          ? "bg-accent font-medium text-foreground hover:bg-accent"
          : "text-foreground hover:bg-accent/40",
      )}
    >
      <DiskIcon disk={disk} />
      <span className="min-w-0 flex-1">
        <span className="block truncate">{disk.name}</span>
        <span className="block truncate text-caption font-normal text-muted-foreground">
          {meta}
        </span>
      </span>
      {disk.kind === "agent" ? (
        <Badge variant="outline">{t(($) => $.files.kind_private)}</Badge>
      ) : null}
    </button>
  );
}

function DiskIcon({ disk }: { disk: Disk }) {
  if (disk.kind === "agent") {
    return (
      <ActorAvatar
        name={disk.name}
        initials={avatarInitials(disk.name)}
        avatarUrl={resolvePublicFileUrl(disk.agent?.avatar_url)}
        isAgent
        size="sm"
      />
    );
  }
  return (
    <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
      <Folder aria-hidden="true" className="size-3" />
    </span>
  );
}

function DiskPane({
  disk,
  wsId,
  agentHref,
}: {
  disk: Disk;
  wsId: string;
  agentHref: string | null;
}) {
  const root = disk.kind === "shared" ? "shared" : `agent:${disk.agentId ?? ""}`;
  const canWrite = disk.access === "write";
  return (
    <FileBrowser
      wsId={wsId}
      root={root}
      disk={{ name: disk.name, kind: disk.kind, access: disk.access }}
      canWrite={canWrite}
      agentHref={agentHref}
    />
  );
}

function FilesLoadingState() {
  const { t } = useT("layout");
  return (
    <div
      role="status"
      aria-live="polite"
      className="flex min-h-0 flex-1 flex-col md:flex-row"
    >
      <span className="sr-only">{t(($) => $.files.loading)}</span>
      <div className="w-full shrink-0 border-b p-3 md:w-64 md:border-b-0 md:border-r">
        <Skeleton className="mb-3 h-8 w-full" />
        <Skeleton className="mb-2 h-3 w-16" />
        <Skeleton className="mb-2 h-10 w-full" />
        <Skeleton className="mb-2 mt-4 h-3 w-20" />
        <Skeleton className="mb-2 h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
      <div className="flex flex-1 flex-col p-4">
        <Skeleton className="mb-4 h-4 w-40" />
        <Skeleton className="h-3 w-full" />
      </div>
    </div>
  );
}
