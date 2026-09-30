import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  AgentContextCapabilities,
  AgentSceneDetail,
  SetAgentSceneBindingInput,
  DeleteContextConnectorCredentialInput,
  ResolveContextConfigSceneInput,
  SetAgentContextCapabilityOffersInput,
  SetContextCapabilityBindingInput,
  SetContextConnectorCredentialInput,
  StartContextConnectorConnectionInput,
} from "../types/context-capability";
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

export function useSetContextCapabilityBinding(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SetContextCapabilityBindingInput) =>
      api.setContextCapabilityBinding(agentId, input),
    // Agent detail holds the personal scope; scene detail lives under the
    // same prefix, so one invalidation covers both.
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: contextConfigKeys.agent(agentId),
      }),
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
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: contextConfigKeys.agent(agentId),
      }),
  });
}

export function useDeleteContextConnectorCredential(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: DeleteContextConnectorCredentialInput) =>
      api.deleteContextConnectorCredential(agentId, input),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: contextConfigKeys.agent(agentId),
      }),
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

/** Replaces the agent's offer catalog. The server answers with the full tab
 * body, which is written to the cache before the settle-time refetch. */
export function useSetAgentContextCapabilityOffers(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  const queryKey = contextCapabilityKeys.agent(wsId, agentId);
  return useMutation({
    mutationFn: (input: SetAgentContextCapabilityOffersInput) =>
      api.setAgentContextCapabilityOffers(wsId, agentId, input),
    onSuccess: (data: AgentContextCapabilities | null) => {
      if (data) queryClient.setQueryData(queryKey, data);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  });
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
