// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales/en/agents.json";
import { PackageRequirementsForm } from "./package-requirements-form";

const list = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({ api: { listDshPlugins: (...args: unknown[]) => list(...args) } }));
const digest = `sha256-${"a".repeat(64)}`;
const requirement = { ref: "lens", packageName: "dsh-mcp-lens", version: "1.0.0", integrity: digest };

function renderForm() {
  const change = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
    <QueryClientProvider client={client}>
      <PackageRequirementsForm workspaceId="destination-workspace" requirements={{ dshPlugins: [requirement], secrets: ["credential"], deferred_bindings: [], runtime_provider: "dsh" }} pluginBindings={{}} onPluginBindingsChange={change} secrets={{}} onSecretsChange={vi.fn()} deferBindings={false} onDeferChange={vi.fn()} disabled={false} />
    </QueryClientProvider>
  </I18nProvider>);
  return change;
}

describe("plugin recipe destination selection", () => {
  beforeEach(() => vi.clearAllMocks());
  it("requires explicit selection and excludes different artifacts", async () => {
    const match = { id: "match", packageName: requirement.packageName, resolvedVersion: requirement.version, integrity: digest };
    list.mockResolvedValue([match, { ...match, id: "other-version", resolvedVersion: "2.0.0" }, { ...match, id: "other-digest", integrity: `sha256-${"b".repeat(64)}` }, { ...match, id: "other-name", packageName: "different-plugin" }]);
    const change = renderForm();
    await screen.findByRole("option", { name: "dsh-mcp-lens @ 1.0.0" });
    const select = screen.getByRole("combobox");
    expect(select).toHaveValue("");
    expect(screen.getAllByRole("option")).toHaveLength(2);
    expect(change).not.toHaveBeenCalled();
    expect(document.querySelector('input[type="password"]')).toHaveValue("");
    await userEvent.selectOptions(select, "match");
    expect(change).toHaveBeenCalledWith({ lens: "match" });
  });
  it("shows missing packages without creating or selecting a binding", async () => {
    list.mockResolvedValue([]);
    const change = renderForm();
    await screen.findByText(enAgents.creation_studio.local.plugin_missing);
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(change).not.toHaveBeenCalled();
  });
  it("reports a failed lookup without binding a fallback", async () => {
    list.mockRejectedValue(new Error("fixture lookup failure"));
    const change = renderForm();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(enAgents.creation_studio.local.plugin_load_failed));
    expect(change).not.toHaveBeenCalled();
  });
});
