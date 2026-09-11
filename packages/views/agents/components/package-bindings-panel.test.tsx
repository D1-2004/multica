// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales/en/agents.json";
import { PackageBindingsPanel } from "./package-bindings-panel";

const mocked = vi.hoisted(() => ({ get: vi.fn(), confirm: vi.fn(), navigate: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: { getAgentPackageBindings: mocked.get, confirmAgentPackageBinding: mocked.confirm } }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
const report = { revision: "revision-1", bindings: [{ path: "/bindings/github_identity", status: "pending", declaration: { ref: "author" }, current: { ref: "current" }, current_fingerprint: "fingerprint", config_tab: "general", message: "" }], resources: [{ ref: "current", kind: "github-identity", label: "Verified account" }] };
function mount(expanded = true) {
  return render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}><I18nProvider locale="en" resources={{ en: { agents: enAgents } }}><PackageBindingsPanel agentId="agent-1" expanded={expanded} onNavigate={mocked.navigate} /></I18nProvider></QueryClientProvider>);
}
beforeEach(() => { vi.clearAllMocks(); mocked.get.mockResolvedValue(report); mocked.confirm.mockResolvedValue(report); });

describe("package resource setup", () => {
  it("requires an explicit reference choice and sends the displayed resource evidence", async () => {
    mount();
    const confirm = await screen.findByRole("button", { name: "Confirm reference mapping" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Open settings" }));
    expect(mocked.navigate).toHaveBeenCalledWith("general");
    expect(mocked.confirm).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("author"), { target: { value: "current" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(mocked.confirm).toHaveBeenCalledWith("agent-1", { path: "/bindings/github_identity", revision: "revision-1", current_fingerprint: "fingerprint", mappings: { author: "current" } }));
  });

  it("keeps pending setup discoverable from other configuration tabs", async () => {
    mount(false);
    fireEvent.click(await screen.findByRole("button", { name: "Review bindings" }));
    expect(mocked.navigate).toHaveBeenCalledWith("publish");
  });

  it("does not confirm an unverified or unknown status", async () => {
    mocked.get.mockResolvedValue({ ...report, bindings: [{ ...report.bindings[0], status: "unavailable", current_fingerprint: "" }] });
    mount();
    const confirm = await screen.findByRole("button", { name: "Confirm reference mapping" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(confirm);
    expect(mocked.confirm).not.toHaveBeenCalled();
  });
});
