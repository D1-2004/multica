import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  AgentContextCapabilities,
  ContextConfigSceneDetail,
  ContextNodeDetail,
  ContextNodeRef,
  ContextPromptComponentInput,
  CreateAgentTenantInput,
  DeleteContextConnectorCredentialInput,
  ContextResourceType,
  ResolveContextConfigSceneInput,
  SetContextCapabilityBindingInput,
  SetContextConnectorCredentialInput,
  SetContextNodeBindingInput,
  SetContextNodeCredentialInput,
  StartContextConnectorConnectionInput,
  StartContextNodeConnectionInput,
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
 * only that scene's detail. The agent detail holds the personal and
 * enterprise levels (their bindings and credentials), and scene details
 * live under the same agent prefix, so a person write refreshes both and an
 * enterprise write refreshes the details. Scene writes stay off the agent
 * detail: for an agent manager it lists every scene of the agent, which is
 * the expensive read of the page. */
function scopeWriteKey(agentId: string, input: { scopeType: string; scopeKey: string }) {
  switch (input.scopeType) {
    case "scene":
      return contextConfigKeys.scene(agentId, input.scopeKey);
    case "org":
      return contextConfigKeys.details(agentId);
    default:
      return contextConfigKeys.agent(agentId);
  }
}

/** Refreshes what a scope write changed. A 1:1 chat scene is its person's
 * configuration (the server writes the person scope), so a write there also
 * refreshes the agent details, which hold the caller's personal scope; not
 * every scene under the agent. */
function invalidateScopeWrite(
  queryClient: QueryClient,
  agentId: string,
  input: { scopeType: string; scopeKey: string },
) {
  const key = scopeWriteKey(agentId, input);
  const scene =
    input.scopeType === "scene" ? queryClient.getQueryData<ContextConfigSceneDetail | null>(key) : null;
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: key }),
    scene?.scene.kind === "dm"
      ? queryClient.invalidateQueries({ queryKey: contextConfigKeys.details(agentId) })
      : undefined,
  ]);
}

export function useSetContextCapabilityBinding(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: SetContextCapabilityBindingInput) =>
      api.setContextCapabilityBinding(agentId, input),
    onSettled: (_data, _error, input) =>
      invalidateScopeWrite(queryClient, agentId, input),
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
      invalidateScopeWrite(queryClient, agentId, input),
  });
}

export function useDeleteContextConnectorCredential(agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: DeleteContextConnectorCredentialInput) =>
      api.deleteContextConnectorCredential(agentId, input),
    onSettled: (_data, _error, input) =>
      invalidateScopeWrite(queryClient, agentId, input),
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

// ---------------------------------------------------------------------------
// Admin 场域: tenants and Context Builder nodes
// ---------------------------------------------------------------------------

/** Creates a tenant (企业) for a DingTalk OrgId. Not optimistic: the server
 * validates the OrgId and answers 409 for an existing tenant; the tree
 * selects the new tenant only after the list refetched. */
export function useCreateAgentTenant(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateAgentTenantInput) => api.createAgentTenant(wsId, agentId, input),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.tenants(wsId, agentId), exact: true }),
  });
}

export interface RenameAgentTenantInput {
  orgId: string;
  name: string;
}

export function useRenameAgentTenant(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orgId, name }: RenameAgentTenantInput) =>
      api.renameAgentTenant(wsId, agentId, orgId, name),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.tenants(wsId, agentId), exact: true }),
  });
}

/** Deletes a tenant with its enterprise-level configuration. The caller
 * awaits it before moving the selection away; every node refetches because
 * the enterprise layer left their effective preview. */
export function useDeleteAgentTenant(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (orgId: string) => api.deleteAgentTenant(wsId, agentId, orgId),
    onSettled: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.tenants(wsId, agentId) }),
        queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.contextNodes(wsId, agentId) }),
        queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.connectedApps(wsId, agentId) }),
        // Its enterprise switches leave the offer usage counts.
        queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.agent(wsId, agentId), exact: true }),
      ]),
  });
}

/** What a node write changes. A level feeds the effective preview of the
 * levels below it, so every node of the agent refetches (only the open one
 * is active). Switches and accounts also move the connected-app usage
 * counts and the offer usage counts of the agent's capabilities (which the
 * offer switches read before asking to take an offer away), and a person
 * level may make that person known to the tenant. */
function invalidateNodeWrite(
  queryClient: QueryClient,
  wsId: string,
  agentId: string,
  node: ContextNodeRef,
  { usage }: { usage: boolean },
) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.contextNodes(wsId, agentId) }),
    usage
      ? queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.connectedApps(wsId, agentId) })
      : undefined,
    usage
      ? queryClient.invalidateQueries({ queryKey: contextCapabilityKeys.agent(wsId, agentId), exact: true })
      : undefined,
    node.scopeType === "person"
      ? queryClient.invalidateQueries({
          queryKey: contextCapabilityKeys.tenantPersons(wsId, agentId, node.orgId),
        })
      : undefined,
  ]);
}

export interface SetContextNodeBindingMutationInput extends SetContextNodeBindingInput {
  node: ContextNodeRef;
}

/** Switch of one offered connector or skill at a level. Not optimistic:
 * the server gates the write on the offer catalog. */
export function useSetContextNodeBinding(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ node, ...input }: SetContextNodeBindingMutationInput) =>
      api.setContextNodeBinding(wsId, agentId, node, input),
    onSettled: (_data, _error, { node }) =>
      invalidateNodeWrite(queryClient, wsId, agentId, node, { usage: true }),
  });
}

export interface SetContextNodePromptsInput {
  node: ContextNodeRef;
  /** The whole list: the PUT replaces the level's prompt components. */
  prompts: ContextPromptComponentInput[];
}

/** Saves a level's prompt components. The stored echo goes into the node
 * before the settle-time refetch. */
export function useSetContextNodePrompts(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ node, prompts }: SetContextNodePromptsInput) =>
      api.setContextNodePrompts(wsId, agentId, node, prompts),
    onSuccess: (prompts, { node }) => {
      if (!prompts) return;
      queryClient.setQueryData<ContextNodeDetail | null>(
        contextCapabilityKeys.contextNode(wsId, agentId, node),
        (current) => (current ? { ...current, prompts } : current),
      );
    },
    onSettled: (_data, _error, { node }) =>
      invalidateNodeWrite(queryClient, wsId, agentId, node, { usage: false }),
  });
}

export interface SetContextNodeMcpConfigInput {
  node: ContextNodeRef;
  /** The whole `mcp_config` document of the level; null clears it. */
  mcpConfig: Record<string, unknown> | null;
}

/** Saves a level's custom MCP servers. Not optimistic: the server
 * validates the document. */
export function useSetContextNodeMcpConfig(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ node, mcpConfig }: SetContextNodeMcpConfigInput) =>
      api.setContextNodeMcpConfig(wsId, agentId, node, mcpConfig),
    onSuccess: (mcpConfig, { node }) => {
      queryClient.setQueryData<ContextNodeDetail | null>(
        contextCapabilityKeys.contextNode(wsId, agentId, node),
        (current) => (current ? { ...current, mcpConfig } : current),
      );
    },
    onSettled: (_data, _error, { node }) =>
      invalidateNodeWrite(queryClient, wsId, agentId, node, { usage: false }),
  });
}

export interface SetContextNodeCredentialMutationInput extends SetContextNodeCredentialInput {
  node: ContextNodeRef;
}

/** Stores a level's token (Bearer, or a Personal Access Token). The
 * variables hold the secret, so the mutation is dropped as soon as nothing
 * observes it (gcTime 0). */
export function useSetContextNodeCredential(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ node, connectorId, bearer }: SetContextNodeCredentialMutationInput) =>
      api.setContextNodeCredential(wsId, agentId, node, { connectorId, bearer }),
    gcTime: 0,
    // Only the node is read from the variables, never the secret.
    onSettled: (_data, _error, { node }) =>
      invalidateNodeWrite(queryClient, wsId, agentId, node, { usage: true }),
  });
}

export interface DeleteContextNodeCredentialInput {
  node: ContextNodeRef;
  connectorId: string;
}

/** Removes a level's token or disconnects its OAuth account. */
export function useDeleteContextNodeCredential(wsId: string, agentId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ node, connectorId }: DeleteContextNodeCredentialInput) =>
      api.deleteContextNodeCredential(wsId, agentId, node, connectorId),
    onSettled: (_data, _error, { node }) =>
      invalidateNodeWrite(queryClient, wsId, agentId, node, { usage: true }),
  });
}

export interface StartContextNodeConnectionMutationInput extends StartContextNodeConnectionInput {
  node: ContextNodeRef;
}

/** Starts an official app sign-in for a level and resolves to the provider
 * authorization URL ("" when the server sent none). Nothing is cached: the
 * page navigates away and refetches on return. */
export function useStartContextNodeConnection(wsId: string, agentId: string) {
  return useMutation({
    mutationFn: ({ node, ...input }: StartContextNodeConnectionMutationInput) =>
      api.startContextNodeConnection(wsId, agentId, node, input),
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
