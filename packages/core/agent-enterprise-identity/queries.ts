import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const agentEnterpriseIdentityKeys = {
  all: (wsId: string) => ["agent-enterprise-identity", wsId] as const,
  status: (wsId: string, agentId: string) => [
    ...agentEnterpriseIdentityKeys.all(wsId),
    "status",
    agentId,
  ] as const,
};

export const agentEnterpriseIdentityStatusOptions = (
  wsId: string,
  agentId: string,
) =>
  queryOptions({
    queryKey: agentEnterpriseIdentityKeys.status(wsId, agentId),
    queryFn: () => api.getAgentEnterpriseIdentityStatus(wsId, agentId),
    enabled: !!wsId && !!agentId,
    refetchOnWindowFocus: "always" as const,
  });
