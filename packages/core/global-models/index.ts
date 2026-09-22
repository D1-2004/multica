import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { getApi } from "../api";
import type { GlobalModels, ModelProvider } from "../api/global-models-schema";
export type {
  GlobalModels,
  ModelProvider,
  ModelRef,
} from "../api/global-models-schema";
export function useDeveloperCapabilities() {
  return useQuery({
    queryKey: ["developer-capabilities"],
    queryFn: () => getApi().getDeveloperCapabilities(),
    retry: false,
  });
}
export function useGlobalModels(enabled: boolean) {
  return useQuery({
    queryKey: ["global-models"],
    queryFn: () => getApi().getGlobalModels(),
    enabled,
    retry: false,
  });
}
export function useSaveGlobalModels() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (v: GlobalModels) => getApi().saveGlobalModels(v),
    onSuccess: () => client.invalidateQueries({ queryKey: ["global-models"] }),
  });
}
export function useDiscoverProviderModels() {
  return useMutation({
    mutationFn: (v: ModelProvider) => getApi().discoverProviderModels(v),
  });
}

export function useRestoreGlobalModels() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (revision: number) => getApi().restoreGlobalModels(revision),
    onSuccess: () => client.invalidateQueries({ queryKey: ["global-models"] }),
  });
}

export function useTestProviderModel() {
  return useMutation({
    mutationFn: (v: { provider: ModelProvider; model: string }) =>
      getApi().testProviderModel(v.provider, v.model),
  });
}
