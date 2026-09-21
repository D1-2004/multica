import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { filesystemKeys } from "./queries";

export function useFilesystemMkdir(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { root: string; path: string }) => api.mkdirFilesystem(input),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: filesystemKeys.roots(workspaceId) });
      qc.invalidateQueries({
        queryKey: ["workspace", workspaceId, "filesystem", "entries"],
      });
    },
  });
}

export function useFilesystemGrant(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { agent_id: string; access: string }) => api.putFilesystemGrant(input),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: filesystemKeys.grants(workspaceId) });
    },
  });
}

export function useFilesystemDownload() {
  return useMutation({
    mutationFn: (input: { root: string; path: string }) =>
      api.downloadFilesystemFile(input),
  });
}

export function useFilesystemUpload(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { root: string; path: string; file: File; filename?: string }) =>
      api.uploadFilesystemFile(input),
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: ["workspace", workspaceId, "filesystem", "entries"],
      });
    },
  });
}

function invalidateEntries(qc: ReturnType<typeof useQueryClient>, workspaceId: string) {
  qc.invalidateQueries({
    queryKey: ["workspace", workspaceId, "filesystem", "entries"],
  });
}

export function useFilesystemUploadBatch(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (items: Array<{ root: string; path: string; file: File; filename?: string }>) => {
      const failed: string[] = [];
      const queue = [...items];
      const workers = Array.from({ length: Math.min(4, queue.length) }, async () => {
        while (queue.length > 0) {
          const item = queue.shift();
          if (!item) return;
          try {
            await api.uploadFilesystemFile(item);
          } catch {
            failed.push(item.filename || item.file.name);
          }
        }
      });
      await Promise.all(workers);
      if (failed.length > 0) {
        throw new Error(failed.slice(0, 3).join(", "));
      }
    },
    onSettled: () => invalidateEntries(qc, workspaceId),
  });
}

export function useFilesystemRename(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { root: string; path: string; name: string }) => api.renameFilesystem(input),
    onSettled: () => invalidateEntries(qc, workspaceId),
  });
}

export function useFilesystemDelete(workspaceId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: { root: string; path: string }) => api.deleteFilesystemEntry(input),
    onSettled: () => invalidateEntries(qc, workspaceId),
  });
}
