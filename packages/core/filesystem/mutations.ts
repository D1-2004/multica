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
