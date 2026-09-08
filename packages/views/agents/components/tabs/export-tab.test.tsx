import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../../locales/en/agents.json";
import { ExportTab } from "./export-tab";

const mocked = vi.hoisted(() => ({ download: vi.fn(), error: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: { exportAgent: mocked.download } }));
vi.mock("sonner", () => ({ toast: { error: mocked.error } }));
beforeEach(() => { vi.clearAllMocks(); });
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });
function mount(canEdit: boolean) {
  render(<QueryClientProvider client={new QueryClient()}><I18nProvider locale="en" resources={{ en: { agents: enAgents } }}><ExportTab agentId="agent-1" canEdit={canEdit} /></I18nProvider></QueryClientProvider>);
}
it("downloads the current agent package only after the export button is clicked", async () => {
  mocked.download.mockResolvedValue(new Blob(["source archive"]));
  const createURL = vi.fn(() => "blob:test");
  vi.stubGlobal("URL", class extends URL { static createObjectURL = createURL; static revokeObjectURL = vi.fn(); });
  const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
  mount(true);
  expect(mocked.download).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Download agent package" }));
  await waitFor(() => expect(click).toHaveBeenCalledOnce());
  expect(mocked.download).toHaveBeenCalledWith("agent-1");
  expect(createURL).toHaveBeenCalledOnce();
});
it("disables export for a user without management permission", () => {
  mount(false);
  expect(screen.getByRole("button", { name: "Download agent package" })).toBeDisabled();
  expect(mocked.download).not.toHaveBeenCalled();
});
