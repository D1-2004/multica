import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  AgentContextCapabilities,
  AgentSceneDetail,
  SetAgentSceneBindingInput,
  DeleteContextConnectorCredentialInput,
  ContextResourceType,
  ResolveContextConfigSceneInput,
  SetContextCapabilityBindingInput,
  SetContextConnectorCredentialInput,
  StartContextConnectorConnectionInput,
} from "../types/context-capability";
import { invalidateConnectorViews, patchInternalConnector } from "../internal-connectors/mutations";
import { contextCapabilityKeys, contextConfigKeys } from "./queries";

/** Redeems an agent-issued configuration link and refreshes every grant-
 * derived view (agent list, agent detail, scenes). */
export function useRedeemContextConfigLink() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => api.redeemContextConfigLink(token),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: contextConfigKeys.all() }),
  });
}

/** The configure-page cache a scope write changes. A scene write changes
 * only that scene's detail. The agent detail holds the personal scope (its
 * bindings and credentials), and scene details live under the same prefix,
 * so a person write refreshes both. Scene writes stay off the agent detail:
 * for an agent manager it lists every scene of the agent, which is the
 * expensive read of the page. */
function scopeWriteKey(agentId: string, input: { scopeType: string; scopeKey: string }) {
  return input.scopeType === "scene"
    ? contextConfigKeys.scene(agentId, input.scopeKey)
    : contextConfigKeys.agent(agentId);
}

export function useSetContextCapabilityBinding(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SetContextCapabilityBindingInput) =>
      api.setContextCapabilityBinding(agentId, input),
    onSettled: (_data, _error, input) =>
      queryClient.invalidateQueries({ queryKey: scopeWriteKey(agentId, input) }),
  });
}

/** Stores a write-only Bearer. The mutation's variables hold the plaintext
 * secret, so the mutation is garbage-collected as soon as it has no observer
 * (gcTime 0) instead of lingering in the MutationCache for the default five
 * minutes. */
export function useSetContextConnectorCredential(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SetContextConnectorCredentialInput) =>
      api.setContextConnectorCredential(agentId, input),
    gcTime: 0,
    // Only the scope identity is read from the variables, never the secret.
    onSettled: (_data, _error, input) =>
      queryClient.invalidateQueries({ queryKey: scopeWriteKey(agentId, input) }),
  });
}

export function useDeleteContextConnectorCredential(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: DeleteContextConnectorCredentialInput) =>
      api.deleteContextConnectorCredential(agentId, input),
    onSettled: (_data, _error, input) =>
      queryClient.invalidateQueries({ queryKey: scopeWriteKey(agentId, input) }),
  });
}

/** Starts connecting an OAuth connector for a scene or personal scope and
 * resolves to the provider authorization URL ("" when the server sent none).
 * Nothing is cached: the page navigates away and refetches on return. */
export function useStartContextConnectorConnection(agentId: string) {
  return useMutation({
    mutationFn: (input: StartContextConnectorConnectionInput) =>
      api.startContextConnectorConnection(agentId, input),
  });
}

/** JSAPI group picker path: converts the picked group into a scene grant. */
export function useResolveContextConfigScene(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ResolveContextConfigSceneInput) =>
      api.resolveContextConfigScene(agentId, input),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: contextConfigKeys.agents() }),
  });
}

/** An offer write answers with the full tab body. It goes straight into the
 * cache, and only the views nested under the agent key (connected apps,
 * scenes) refetch; without a body the whole agent refetches. */
function useOfferWriteCache(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  const queryKey = contextCapabilityKeys.agent(wsId, agentId);
  return {
    onSuccess: (data: AgentContextCapabilities | null) => {
      if (data) queryClient.setQueryData(queryKey, data);
    },
    onSettled: (data: AgentContextCapabilities | null | undefined) =>
      queryClient.invalidateQueries({
        queryKey,
        predicate: data ? (query) => query.queryKey.length > queryKey.length : undefined,
      }),
  };
}

export interface SetAgentScenePromptInput {
  sceneKey: string;
  prompt: string;
}

/** Saves a scene prompt (admin only). The echo is written into the scene
 * detail before the settle-time refetch; the list refetches for has_prompt. */
export function useSetAgentScenePrompt(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ sceneKey, prompt }: SetAgentScenePromptInput) =>
      api.setAgentScenePrompt(wsId, agentId, sceneKey, prompt),
    onSuccess: (prompt, { sceneKey }) => {
      queryClient.setQueryData<AgentSceneDetail | null>(
        contextCapabilityKeys.scene(wsId, agentId, sceneKey),
        (current) => (current ? { ...current, prompt } : current),
      );
    },
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: contextCapabilityKeys.scenes(wsId, agentId),
      }),
  });
}

export interface SetAgentSceneBindingMutationInput extends SetAgentSceneBindingInput {
  sceneKey: string;
}

/** Admin toggle of one offered connector or skill in a scene. Not
 * optimistic: the server gates the write on the offer catalog. The agent key
 * covers the scene detail, the scene list counts and the offer usage
 * counts. */
export function useSetAgentSceneBinding(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ sceneKey, ...input }: SetAgentSceneBindingMutationInput) =>
      api.setAgentSceneBinding(wsId, agentId, sceneKey, input),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: contextCapabilityKeys.agent(wsId, agentId),
      }),
  });
}

export interface SetAgentOfferInput {
  resourceType: ContextResourceType;
  resourceId: string;
  offered: boolean;
}

/** Adds one connector or skill to, or removes it from, the agent's offer
 * catalog. The offers PUT replaces the whole catalog, so the write starts
 * from a fresh read and sends every other offer unchanged: a switch never
 * undoes an offer changed elsewhere since the page loaded. Resolves to the
 * saved tab body (the fresh read when nothing changed). */
async function writeOffer(
  wsId: string,
  agentId: string,
  { resourceType, resourceId, offered }: SetAgentOfferInput,
): Promise<AgentContextCapabilities | null> {
  const current = await api.getAgentContextCapabilities(wsId, agentId);
  if (!current) throw new Error("context capabilities unavailable");
  const offers = { connectorIds: current.offers.connectorIds, skillIds: current.offers.skillIds };
  const field = resourceType === "connector" ? "connectorIds" : "skillIds";
  const others = offers[field].filter((id) => id !== resourceId);
  if ((others.length !== offers[field].length) === offered) return current;
  offers[field] = offered ? [...others, resourceId] : others;
  return api.setAgentContextCapabilityOffers(wsId, agentId, offers);
}

/** Offer switch of one connector or skill (允许群聊、个人连接自己的账号 for
 * an app, 允许群聊 / 个人单独开启 for an Aone FaaS connector or a skill). Not
 * optimistic: removing an offer turns the resource off in every scene and
 * for every person, and the server owns the admin-only rule for adding a
 * connector. */
export function useSetAgentOffer(wsId: string, agentId: string) {
  const cache = useOfferWriteCache(wsId, agentId);
  return useMutation({
    mutationFn: (input: SetAgentOfferInput) => writeOffer(wsId, agentId, input),
    ...cache,
  });
}

/** Adds an official app to this agent (未添加 → 添加): creates the workspace
 * catalog connector when it is missing (idempotent on the server), then
 * offers it so groups and people may connect their own accounts. Adding
 * never grants it to everyone: that needs a working shared account first.
 * Resolves to the connector id. */
export function useAddConnectedApp(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (slug: string): Promise<string> => {
      const echoed = await api.addCatalogConnector(wsId, slug);
      // A malformed echo still created the connector; read it back.
      const connector =
        echoed ??
        (await api.listInternalConnectors(wsId)).find((item) => item.catalogSlug === slug) ??
        null;
      if (!connector) throw new Error(`official app ${slug} was not added`);
      await writeOffer(wsId, agentId, { resourceType: "connector", resourceId: connector.id, offered: true });
      return connector.id;
    },
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
  });
}

/** Removes a connector from this agent: revokes the agent grant and the
 * offer. The workspace library connector and its shared account stay. */
export function useRemoveAgentConnector(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (connectorId: string) => {
      await patchInternalConnector(wsId, { connectorId, grant: { agentId, granted: false } });
      await writeOffer(wsId, agentId, { resourceType: "connector", resourceId: connectorId, offered: false });
    },
    onSettled: () => invalidateConnectorViews(queryClient, wsId),
  });
}
