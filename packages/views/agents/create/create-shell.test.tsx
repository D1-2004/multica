import { afterEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales/en/agents.json";
import { AgentCreateShell } from "./create-shell";

const download = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({ api: { downloadAgentSchema: download } }));
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("downloads the manifest schema from the creation header without creating an agent", async () => {
  download.mockResolvedValue(new Blob(["schema"]));
  vi.stubGlobal("URL", class extends URL { static createObjectURL = () => "blob:schema"; static revokeObjectURL = vi.fn(); });
  let filename = "";
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { filename = this.download; });
  render(<QueryClientProvider client={new QueryClient()}><I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
    <AgentCreateShell title="Create agent" step="Choose" onBack={() => {}}>Form</AgentCreateShell>
  </I18nProvider></QueryClientProvider>);
  expect(download).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Download Schema" }));
  await waitFor(() => expect(filename).toBe("agent.schema.json"));
  expect(download).toHaveBeenCalledOnce();
});
