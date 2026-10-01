import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import {
  internalConnectorUpdateInput,
  type InternalConnectorInput,
} from "../api/internal-connector-schema";
import { contextCapabilityKeys } from "../context-capabilities/queries";
import { internalConnectorKeys } from "./queries";

/** A library change (grant, credential, tools, switches) also changes what
 * the agent pages derive from it: connected apps, the offer library and
 * scene offers. Refresh both families. */
export function invalidateConnectorViews(queryClient: QueryClient, wsId: string) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: internalConnectorKeys.all(wsId) }),
    queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.all(wsId) }),
  ]);
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
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
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
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
  });
}

/** Disconnects the workspace shared account (OAuth account or pasted
 * token) of a connector. Scene and personal credentials are untouched. */
export function useDeleteInternalConnectorCredential(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (connectorId: string) => api.deleteInternalConnectorCredential(wsId, connectorId),
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
  });
}

export interface PatchInternalConnectorInput {
  connectorId: string;
  /** Workspace kill switch (every agent, scene and person). */
  enabled?: boolean;
  /** Catalog connectors only: expose write tools too. */
  writeEnabled?: boolean;
  /** Grants the connector to one agent (对所有用户启用) or revokes it. */
  grant?: { agentId: string; granted: boolean };
}

/** Changes one library connector by id and skips the write when nothing
 * would change. The update endpoint replaces the connector wholesale, so
 * the write starts from a fresh read of the stored connector instead of a
 * possibly stale cache entry. */
export async function patchInternalConnector(
  wsId: string,
  { connectorId, enabled, writeEnabled, grant }: PatchInternalConnectorInput,
): Promise<void> {
  const connector = (await api.listInternalConnectors(wsId)).find((item) => item.id === connectorId);
  if (!connector) throw new Error("connector not found");
  const patch: Partial<Pick<InternalConnectorInput, "agent_ids" | "enabled" | "write_enabled">> = {};
  if (enabled !== undefined && enabled !== connector.enabled) patch.enabled = enabled;
  if (writeEnabled !== undefined && writeEnabled !== connector.writeEnabled) patch.write_enabled = writeEnabled;
  if (grant && connector.agentIds.includes(grant.agentId) !== grant.granted) {
    const others = connector.agentIds.filter((id) => id !== grant.agentId);
    patch.agent_ids = grant.granted ? [...others, grant.agentId] : others;
  }
  if (Object.keys(patch).length === 0) return;
  await api.updateInternalConnector(wsId, connector.id, internalConnectorUpdateInput(connector, patch));
}

/** Not optimistic: the server recomputes the pinned tools and credential
 * state. */
export function usePatchInternalConnector(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: PatchInternalConnectorInput) => patchInternalConnector(wsId, input),
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
  });
}
