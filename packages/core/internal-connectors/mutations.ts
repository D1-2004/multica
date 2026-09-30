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

export interface SetInternalConnectorAgentGrantInput {
  connector: InternalConnector;
  agentId: string;
  granted: boolean;
}

/** Grants a workspace connector to one agent (or revokes it) from the
 * agent's connector tab. The update endpoint replaces the connector
 * wholesale, so the write starts from the full current connector. */
export function useSetInternalConnectorAgentGrant(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ connector, agentId, granted }: SetInternalConnectorAgentGrantInput) => {
      const others = connector.agentIds.filter((id) => id !== agentId);
      return api.updateInternalConnector(
        wsId,
        connector.id,
        internalConnectorUpdateInput(connector, {
          agent_ids: granted ? [...others, agentId] : others,
        }),
      );
    },
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}

export interface SetInternalConnectorEnabledInput {
  connector: InternalConnector;
  enabled: boolean;
}

/** Workspace kill switch of one connector (every agent, scene and person). */
export function useSetInternalConnectorEnabled(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ connector, enabled }: SetInternalConnectorEnabledInput) =>
      api.updateInternalConnector(
        wsId,
        connector.id,
        internalConnectorUpdateInput(connector, { enabled }),
      ),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}

export interface AddAgentCatalogConnectorInput {
  slug: string;
  agentId: string;
}

/** Adds an official app to one agent: creates the workspace catalog
 * connector when it is missing (idempotent on the server), then grants it
 * to the agent. Resolves to the granted connector. */
export function useAddAgentCatalogConnector(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ slug, agentId }: AddAgentCatalogConnectorInput): Promise<InternalConnector> => {
      const echoed = await api.addCatalogConnector(wsId, slug);
      // A malformed echo still created the connector; read it back so the
      // grant starts from the stored connector.
      const connector =
        echoed ??
        (await api.listInternalConnectors(wsId)).find((item) => item.catalogSlug === slug) ??
        null;
      if (!connector) throw new Error(`official app ${slug} was not added`);
      if (connector.agentIds.includes(agentId)) return connector;
      await api.updateInternalConnector(
        wsId,
        connector.id,
        internalConnectorUpdateInput(connector, {
          agent_ids: [...connector.agentIds, agentId],
        }),
      );
      return { ...connector, agentIds: [...connector.agentIds, agentId] };
    },
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
  });
}
