import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import {
  internalConnectorUpdateInput,
  type InternalConnector,
} from "../api/internal-connector-schema";
import { internalConnectorKeys } from "./queries";

/** Adds an official app to the workspace (idempotent on the server). The
 * new connector shows up in the list and the catalog after the refetch. */
export function useAddCatalogConnector(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (slug: string) => api.addCatalogConnector(wsId, slug),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}

export interface StartInternalConnectorOAuthInput {
  connectorId: string;
  returnTo?: string;
}

/** Starts the provider sign-in for the workspace-wide shared account and
 * resolves to the authorization URL ("" when the server sent none). The
 * start binds the sign-in to the browser that receives the response, so the
 * caller must navigate this same browser to the URL (web only). */
export function useStartInternalConnectorOAuth(wsId: string) {
  return useMutation({
    mutationFn: ({ connectorId, returnTo }: StartInternalConnectorOAuthInput) =>
      api.startInternalConnectorOAuth(wsId, connectorId, returnTo),
  });
}

/** Re-discovers a catalog connector's tools with the workspace credential. */
export function useRefreshInternalConnectorTools(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (connectorId: string) => api.refreshInternalConnectorTools(wsId, connectorId),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}

export interface SetInternalConnectorWriteEnabledInput {
  connector: InternalConnector;
  writeEnabled: boolean;
}

/** Toggles write tools for a catalog connector. Not optimistic: the server
 * recomputes the pinned tool list from the discovered tools. */
export function useSetInternalConnectorWriteEnabled(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ connector, writeEnabled }: SetInternalConnectorWriteEnabledInput) =>
      api.updateInternalConnector(
        wsId,
        connector.id,
        internalConnectorUpdateInput(connector, { write_enabled: writeEnabled }),
      ),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}

export interface SetInternalConnectorCredentialInput {
  connectorId: string;
  bearer: string;
}

/** Stores the workspace credential (Bearer or Personal Access Token). The
 * variables hold the plaintext secret, so the mutation is garbage-collected
 * as soon as it has no observer (gcTime 0). */
export function useSetInternalConnectorCredential(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ connectorId, bearer }: SetInternalConnectorCredentialInput) =>
      api.setInternalConnectorCredential(wsId, connectorId, bearer),
    gcTime: 0,
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}
