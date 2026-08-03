// @vitest-environment jsdom

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import enRuntimes from "../../locales/en/runtimes.json";

const validateCredential = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  reset: vi.fn(),
  isPending: false,
  isError: false,
  error: null as Error | null,
  data: {
    valid: true as const,
    quotas: [
      {
        network_zone: "ALITest",
        region: "cn-zhangjiakou",
        quota: 5,
        usage: 2,
        remaining: 3,
        volume_size_quota_gib: 20,
        volume_usage_gib: 4,
      },
    ],
  },
}));

const updateCredential = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
}));

const credentialQuery = vi.hoisted(() => ({
  isPending: false,
  isError: false,
  error: null as Error | null,
  refetch: vi.fn(),
  data: {
    configured: true,
    api_key_hint: "cafe12",
    quotas: [
      {
        network_zone: "ALITest",
        region: "cn-zhangjiakou",
        quota: 5,
        usage: 1,
        remaining: 4,
        volume_size_quota_gib: 20,
        volume_usage_gib: 3,
      },
    ],
  },
}));

const toastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/runtimes", () => ({
  useASBRuntimeCredential: () => credentialQuery,
  useValidateASBRuntimeCredential: () => validateCredential,
  useUpdateASBRuntimeCredential: () => updateCredential,
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccess, error: vi.fn() },
}));

import { ASBRuntimeCredentialSection } from "./asb-runtime-credential-section";

describe("ASBRuntimeCredentialSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    validateCredential.mutateAsync.mockResolvedValue(validateCredential.data);
    updateCredential.mutateAsync.mockResolvedValue({
      configured: true,
      api_key_hint: "beef34",
      quotas: validateCredential.data.quotas,
      invalidated_sandbox_count: 2,
    });
  });

  it("shows the masked key and quota, then validates before updating", async () => {
    render(
      <I18nProvider locale="en" resources={{ en: { runtimes: enRuntimes } }}>
        <ASBRuntimeCredentialSection runtimeId="runtime-asb" />
      </I18nProvider>,
    );

    expect(screen.getByText("••••••••cafe12")).toBeInTheDocument();
    expect(
      screen.getByText("Sandboxes: 1 / 5 used, 4 remaining"),
    ).toBeInTheDocument();

    const saveButton = screen.getByRole("button", { name: "Save new key" });
    expect(saveButton).toBeDisabled();

    fireEvent.change(screen.getByLabelText("Update ASB API Key"), {
      target: { value: "new-api-key" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Validate and check quota" }),
    );

    await waitFor(() => expect(saveButton).toBeEnabled());
    fireEvent.click(saveButton);

    await waitFor(() =>
      expect(updateCredential.mutateAsync).toHaveBeenCalledWith({
        runtimeId: "runtime-asb",
        data: { api_key: "new-api-key" },
      }),
    );
    expect(toastSuccess).toHaveBeenCalledWith(
      "ASB API Key updated; 2 sandboxes invalidated",
    );
  });
});
