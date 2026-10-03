import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderWithI18n } from "../../test/i18n";
const state = vi.hoisted(() => ({
  developer: true,
  save: vi.fn(),
  discover: vi.fn(),
}));
vi.mock("@multica/core/global-models", () => ({
  useTestProviderModel: () => ({ mutateAsync: vi.fn() }),
  useDeveloperCapabilities: () => ({ data: { developer: state.developer } }),
  useGlobalModels: () => ({
    data: {
      revision: 1,
      providers: [
        {
          id: "mass",
          name: "Diamond",
          baseUrl: "https://default.example/v1",
          models: ["qwen"],
          enabled: true,
          builtin: true,
          hasKey: true,
          apiKey: "",
        },
      ],
      agentModels: [{ provider: "mass", model: "qwen" }],
      defaultModel: { provider: "mass", model: "qwen" },
      coordinator: [{ provider: "mass", model: "qwen" }],
      diamondFallback: true,
    },
  }),
  useSaveGlobalModels: () => ({ mutateAsync: state.save }),
  useDiscoverProviderModels: () => ({ mutateAsync: state.discover }),
  useRestoreGlobalModels: () => ({ mutate: vi.fn() }),
}));
vi.mock(
  "../../runtimes/components/stable-fc-e2b-runtime-overview-page",
  () => ({
    StableFCE2BRuntimeOverviewPage: () => <div data-testid="runtime-release" />,
  }),
);
import { DeveloperTab } from "./developer-tab";
beforeEach(() => {
  state.developer = true;
  state.save.mockReset();
  state.save.mockResolvedValue({});
  state.discover.mockReset();
});
describe("developer settings", () => {
  it("hides global editing from non developers", () => {
    state.developer = false;
    renderWithI18n(<DeveloperTab />);
    expect(screen.getByText("Developer access required")).toBeInTheDocument();
    expect(screen.queryByText("Add provider")).toBeNull();
  });
  it("keeps Diamond readonly and hosts runtime release controls", () => {
    renderWithI18n(<DeveloperTab />);
    fireEvent.click(screen.getByText("Diamond", { selector: "strong" }));
    expect(
      screen.getByDisplayValue("https://default.example/v1"),
    ).toBeDisabled();
    expect(screen.queryByLabelText("API Key")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Runtime releases" }));
    expect(screen.getByTestId("runtime-release")).toBeInTheDocument();
  });
  it("preserves newlines while manually editing a provider catalog", () => {
    renderWithI18n(<DeveloperTab />);
    fireEvent.click(screen.getByRole("button", { name: "Add provider" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit catalog" }));
    const catalogs = screen.getAllByLabelText(
      "Model catalog (one model ID per line)",
    );
    const input = catalogs[catalogs.length - 1]!;
    fireEvent.change(input, { target: { value: "model-one\n" } });
    expect(input).toHaveValue("model-one\n");
  });
});

it("preserves selected models across search and selection filters", async () => {
  renderWithI18n(<DeveloperTab />);
  fireEvent.change(screen.getByLabelText("Search models or providers"), {
    target: { value: "not-found" },
  });
  expect(screen.queryByRole("checkbox", { name: "mass/qwen" })).toBeNull();
  fireEvent.change(screen.getByLabelText("Search models or providers"), {
    target: { value: "QWEN" },
  });
  expect(screen.getByRole("checkbox", { name: "mass/qwen" })).toBeChecked();
  fireEvent.change(screen.getByLabelText("Filter selection"), {
    target: { value: "unselected" },
  });
  expect(screen.queryByRole("checkbox", { name: "mass/qwen" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Save global settings" }));
  expect(state.save).toHaveBeenCalledWith(
    expect.objectContaining({
      agentModels: [{ provider: "mass", model: "qwen" }],
    }),
  );
});
it("filters a catalog without altering its content", () => {
  renderWithI18n(<DeveloperTab />);
  fireEvent.click(screen.getByText("Diamond", { selector: "strong" }));
  fireEvent.change(screen.getByLabelText("Search catalog models"), {
    target: { value: "not-found" },
  });
  expect(
    screen.getByRole("region", { name: "Model catalog" }),
  ).toHaveTextContent("No matching results");
  fireEvent.change(screen.getByLabelText("Search catalog models"), {
    target: { value: "QWEN" },
  });
  expect(
    screen.getByRole("region", { name: "Model catalog" }),
  ).toHaveTextContent("qwen");
});

it("searches shared Coordinator / EmployeeLoop model choices without changing the selected model", () => {
  renderWithI18n(<DeveloperTab />);
  fireEvent.click(screen.getByRole("button", { name: "Coordinator / EmployeeLoop model 1" }));
  const search = screen
    .getAllByPlaceholderText("Search models or providers")
    .at(-1)!;
  fireEvent.change(search, { target: { value: "not-a-model" } });
  expect(screen.queryByRole("button", { name: "mass/qwen" })).toBeNull();
  fireEvent.change(search, { target: { value: "QWEN" } });
  expect(screen.getByRole("button", { name: "mass/qwen" })).toBeInTheDocument();
});
it("replaces a verified vendor catalog instead of merging rejected models back", async () => {
  state.discover.mockResolvedValue({
    models: ["verified"],
    replace: true,
    checked: 2,
    unavailable: 1,
    unverified: 0,
  });
  renderWithI18n(<DeveloperTab />);
  fireEvent.click(screen.getByRole("button", { name: "Add provider" }));
  fireEvent.click(screen.getByRole("button", { name: "Edit catalog" }));
  fireEvent.change(
    screen.getByLabelText("Model catalog (one model ID per line)"),
    { target: { value: "rejected" } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Fetch /models" }));
  await waitFor(() =>
    expect(
      screen.getByLabelText("Model catalog (one model ID per line)"),
    ).toHaveValue("verified"),
  );
});
