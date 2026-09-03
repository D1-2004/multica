// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider, queryOptions } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";
import { CoordinatorSessionsTab } from "./coordinator-sessions-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/agents")>();
  return {
    ...actual,
    agentCoordinatorSessionsOptions: (wsId: string, agentId: string) =>
      queryOptions({
        queryKey: ["coord", wsId, agentId],
        queryFn: async () => [],
      }),
    useAgentPresenceDetail: () => "loading",
  };
});

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsCompact: () => false,
}));

describe("CoordinatorSessionsTab", () => {
  it("shows inbound conversations without scene memory", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <QueryClientProvider client={client}>
          <CoordinatorSessionsTab
            agent={{ id: "agent-1", workspace_id: "ws-1", name: "测试号" } as Agent}
          />
        </QueryClientProvider>
      </I18nProvider>,
    );
    expect(await screen.findByText("No inbound conversations yet.")).toBeInTheDocument();
    expect(screen.queryByText("Scene memory")).toBeNull();
    expect(screen.queryByLabelText("Write")).toBeNull();
  });
});
