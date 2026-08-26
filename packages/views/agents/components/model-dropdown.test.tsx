// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  AgentRuntime,
  RuntimeModel,
  RuntimeModelListRequest,
} from "@multica/core/types";
import enAgents from "../../locales/en/agents.json";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";

const mockInitiateListModels = vi.hoisted(() => vi.fn());
const mockGetListModelsResult = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    initiateListModels: (...args: unknown[]) => mockInitiateListModels(...args),
    getListModelsResult: (...args: unknown[]) => mockGetListModelsResult(...args),
  },
  ApiError: class ApiError extends Error {},
}));

import { ModelDropdown } from "./model-dropdown";

const MODEL: RuntimeModel = {
  id: "qwen3.8-max",
  label: "Qwen 3.8 Max",
  provider: "opencode",
  default: true,
  pricing: {
    input: 1.768842,
    output: 5.306526,
    cache_read: 0.221105,
    cache_write: 2.211052,
  },
};

const RUNTIME = {
  id: "runtime-1",
  provider: "opencode",
  status: "online",
  runtime_mode: "local",
  metadata: {},
} as AgentRuntime;

function result(): RuntimeModelListRequest {
  return {
    id: "request-1",
    runtime_id: RUNTIME.id,
    status: "completed",
    models: [MODEL],
    supported: true,
    created_at: "2026-08-24T00:00:00Z",
    updated_at: "2026-08-24T00:00:00Z",
  };
}

describe("ModelDropdown pricing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockInitiateListModels.mockResolvedValue(result());
    mockGetListModelsResult.mockResolvedValue(result());
  });

  afterEach(cleanup);

  it("shows the deployment-owned price for the selected model", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <I18nProvider
        locale="en"
        resources={{ en: { common: enCommon, agents: enAgents, issues: enIssues } }}
      >
        <QueryClientProvider client={queryClient}>
          <ModelDropdown
            runtimeId={RUNTIME.id}
            runtime={RUNTIME}
            runtimeOnline
            value={MODEL.id}
            onChange={vi.fn()}
          />
        </QueryClientProvider>
      </I18nProvider>,
    );

    expect(await screen.findByText(/Input \$1\.77 · output \$5\.31 \/ 1M tokens/)).toBeTruthy();
  });
});
