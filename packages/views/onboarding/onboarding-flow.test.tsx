import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { Workspace } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enOnboarding from "../locales/en/onboarding.json";

const TEST_RESOURCES = { en: { common: enCommon, onboarding: enOnboarding } };

const mocks = vi.hoisted(() => {
  const workspace = {
    id: "ws_acme",
    name: "Acme",
    slug: "acme",
    description: null,
    context: null,
    settings: {},
    repos: [],
    issue_prefix: "ACM",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  } as unknown as Workspace;

  return {
    workspace,
    completeOnboarding: vi.fn<() => Promise<void>>(),
    saveQuestionnaire: vi.fn<() => Promise<void>>(),
    setCurrentWorkspace: vi.fn(),
    setWelcomeSignal: vi.fn(),
    captureEvent: vi.fn(),
  };
});

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: unknown) => unknown) =>
    selector({
      user: {
        id: "user_test",
        onboarding_questionnaire: {},
      },
    }),
}));

vi.mock("@multica/core/analytics", () => ({
  captureEvent: mocks.captureEvent,
}));

vi.mock("@multica/core/onboarding", () => ({
  ONBOARDING_STEP_ORDER: ["workspace"],
  completeOnboarding: mocks.completeOnboarding,
  saveQuestionnaire: mocks.saveQuestionnaire,
  useWelcomeStore: {
    getState: () => ({ set: mocks.setWelcomeSignal }),
  },
}));

vi.mock("@multica/core/platform", () => ({
  setCurrentWorkspace: mocks.setCurrentWorkspace,
}));

vi.mock("@multica/core/workspace/queries", () => ({
  workspaceListOptions: () => ({
    queryKey: ["workspaces"],
    queryFn: async () => [],
  }),
}));

vi.mock("./steps/step-welcome", () => ({
  StepWelcome: ({ onNext }: { onNext: () => void }) => (
    <button type="button" onClick={onNext}>
      Welcome next
    </button>
  ),
}));

vi.mock("./steps/step-source", () => ({
  StepSource: () => <div data-testid="source-step" />,
}));

vi.mock("./steps/step-role", () => ({
  StepRole: () => <div data-testid="role-step" />,
}));

vi.mock("./steps/step-use-case", () => ({
  StepUseCase: () => <div data-testid="use-case-step" />,
}));

vi.mock("./steps/step-workspace", () => ({
  StepWorkspace: ({
    onCreated,
  }: {
    onCreated: (workspace: Workspace) => void;
  }) => (
    <div data-testid="workspace-step">
      <button type="button" onClick={() => onCreated(mocks.workspace)}>
        Continue with Acme
      </button>
    </div>
  ),
}));

vi.mock("./steps/step-runtime-connect", () => ({
  StepRuntimeConnect: () => <div data-testid="runtime-step" />,
}));

vi.mock("./steps/step-platform-fork", () => ({
  StepPlatformFork: () => <div data-testid="platform-step" />,
}));

import { OnboardingFlow } from "./onboarding-flow";

function TestWrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        {children}
      </I18nProvider>
    </QueryClientProvider>
  );
}

function renderFlow() {
  const onComplete = vi.fn();
  render(<OnboardingFlow onComplete={onComplete} />, {
    wrapper: TestWrapper,
  });
  return { onComplete };
}

describe("OnboardingFlow — workspace-only setup", () => {
  beforeEach(() => {
    mocks.completeOnboarding.mockReset();
    mocks.completeOnboarding.mockResolvedValue(undefined);
    mocks.saveQuestionnaire.mockReset();
    mocks.setCurrentWorkspace.mockReset();
    mocks.setWelcomeSignal.mockReset();
    mocks.captureEvent.mockReset();
  });

  it("starts at workspace and completes onboarding immediately after workspace selection", async () => {
    const user = userEvent.setup();
    const { onComplete } = renderFlow();

    expect(screen.getByTestId("workspace-step")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Continue with Acme" }));

    await waitFor(() => {
      expect(mocks.completeOnboarding).toHaveBeenCalledWith(
        "runtime_skipped",
        "ws_acme",
      );
    });
    expect(mocks.setCurrentWorkspace).toHaveBeenCalledWith("acme", "ws_acme");
    expect(mocks.setWelcomeSignal).not.toHaveBeenCalled();
    expect(onComplete).toHaveBeenCalledWith(mocks.workspace, undefined);
    expect(screen.queryByTestId("runtime-step")).not.toBeInTheDocument();
  });
});
