// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

const TEST_RESOURCES = {
  en: { common: enCommon, runtimes: enRuntimes },
};

type ValidationResult = {
  valid: true;
  quotas: Array<{
    network_zone: string;
    region: string;
    quota: number;
    usage: number;
    remaining: number;
    volume_usage_gib: number;
  }>;
};

const mockCreateRuntime = vi.hoisted(() => vi.fn());
const mockValidateCredential = vi.hoisted(() => vi.fn());
const mockResetValidation = vi.hoisted(() => vi.fn());
const mockValidationState = vi.hoisted(() => ({
  data: undefined as ValidationResult | undefined,
  error: null as Error | null,
  isError: false,
  isPending: false,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/runtimes", () => ({
  FC_E2B_RUNTIME_PROVIDERS: ["hermes", "opencode", "pi"],
  fcE2BProviderForTemplate: () => "hermes",
  isReadyFCE2BTemplate: () => true,
  useCloudSandboxStableChannel: () => ({
    data: {
      current: {
        artifact_alias: "multica-asb-runtime:stable",
      },
    },
  }),
  useCreateCloudSandboxRuntime: () => ({
    mutateAsync: (...args: unknown[]) => mockCreateRuntime(...args),
    isPending: false,
  }),
  useFCE2BTemplates: () => ({
    data: [],
    isLoading: false,
    isError: false,
    error: null,
  }),
  useValidateASBRuntimeCredential: () => ({
    ...mockValidationState,
    mutateAsync: (...args: unknown[]) => mockValidateCredential(...args),
    reset: mockResetValidation,
  }),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { FCE2BRuntimeDialog } from "./fc-e2b-runtime-dialog";

function renderDialog() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <FCE2BRuntimeDialog
        sandboxBackend="asb"
        canPublish={false}
        onClose={vi.fn()}
      />
    </I18nProvider>,
  );
}

describe("FCE2BRuntimeDialog ASB credential validation", () => {
  afterEach(() => {
    cleanup();
  });

  beforeEach(() => {
    vi.clearAllMocks();
    mockValidationState.data = undefined;
    mockValidationState.error = null;
    mockValidationState.isError = false;
    mockValidationState.isPending = false;
    mockCreateRuntime.mockResolvedValue(undefined);
    mockValidateCredential.mockImplementation(async () => {
      const result = {
        valid: true as const,
        quotas: [
          {
            network_zone: "ALITest",
            region: "cn-zhangjiakou",
            quota: 5,
            usage: 1,
            remaining: 4,
            volume_usage_gib: 0,
          },
        ],
      };
      mockValidationState.data = result;
      return result;
    });
  });

  it("requires live validation and displays current quota before creation", async () => {
    renderDialog();

    const create = screen.getByRole("button", { name: "Create runtime" });
    const apiKey = screen.getByLabelText("ASB API Key");
    expect((create as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(apiKey, { target: { value: "tenant-key-1234" } });
    expect((create as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(
      screen.getByRole("button", { name: "Validate and check quota" }),
    );

    await waitFor(() =>
      expect(screen.queryByText("API Key is valid")).not.toBeNull(),
    );
    expect(screen.queryByText("ALITest · cn-zhangjiakou")).not.toBeNull();
    expect(
      screen.queryByText("Sandboxes: 1 / 5 used, 4 remaining"),
    ).not.toBeNull();
    expect((create as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(create);
    await waitFor(() =>
      expect(mockCreateRuntime).toHaveBeenCalledWith(
        expect.objectContaining({
          sandbox_backend: "asb",
          api_key: "tenant-key-1234",
          artifact_channel: "stable",
          provider: "hermes",
        }),
      ),
    );
  });

  it("invalidates the result whenever the API Key changes", async () => {
    renderDialog();

    const apiKey = screen.getByLabelText("ASB API Key");
    fireEvent.change(apiKey, { target: { value: "tenant-key-1234" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Validate and check quota" }),
    );
    await screen.findByText("API Key is valid");

    fireEvent.change(apiKey, { target: { value: "tenant-key-5678" } });

    expect(screen.queryByText("API Key is valid")).toBeNull();
    expect(
      (
        screen.getByRole("button", {
          name: "Create runtime",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(mockResetValidation).toHaveBeenCalled();
  });

  it("does not accept a late result for an API Key that was edited", async () => {
    let resolveValidation: ((result: ValidationResult) => void) | undefined;
    mockValidateCredential.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveValidation = resolve;
        }),
    );
    renderDialog();

    const apiKey = screen.getByLabelText("ASB API Key");
    fireEvent.change(apiKey, { target: { value: "tenant-key-1234" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Validate and check quota" }),
    );
    fireEvent.change(apiKey, { target: { value: "tenant-key-5678" } });

    const result = {
      valid: true as const,
      quotas: [
        {
          network_zone: "ALITest",
          region: "cn-zhangjiakou",
          quota: 5,
          usage: 1,
          remaining: 4,
          volume_usage_gib: 0,
        },
      ],
    };
    mockValidationState.data = result;
    await act(async () => resolveValidation?.(result));

    expect(screen.queryByText("API Key is valid")).toBeNull();
    expect(
      (
        screen.getByRole("button", {
          name: "Create runtime",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });
});
