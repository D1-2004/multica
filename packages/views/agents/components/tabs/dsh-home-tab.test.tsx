// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import { DshHomeTab } from "./dsh-home-tab";

const calls = vi.hoisted(() => ({ get: vi.fn(), ensure: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getDSHHome: (...args: unknown[]) => calls.get(...args),
  ensureDSHHome: (...args: unknown[]) => calls.ensure(...args),
  listFilesystemEntries: async () => ({ entries: [] }),
  listFilesystemGrants: async () => ({ grants: [] }),
} }));
vi.mock("@multica/core/paths", () => ({ useWorkspacePaths: () => ({ files: () => "/workspace/files" }) }));
vi.mock("../../../navigation", () => ({ AppLink: ({href, children}: {href: string; children: React.ReactNode}) => <a href={href}>{children}</a> }));
const missing = { provisioned: false, state: "unprovisioned", step: 0, generation: 0, sandboxId: "" };
const ready = { provisioned: true, state: "offline", step: 6, generation: 0, sandboxId: "" };
const clients: QueryClient[] = [];
beforeEach(() => { vi.resetAllMocks(); calls.get.mockResolvedValue(missing); });
afterEach(() => { cleanup(); clients.forEach((c) => c.clear()); clients.length = 0; });
function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}>
    <QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId="agent" /></QueryClientProvider>
  </I18nProvider>);
}

it("does not show the shared disk inside DSH", async () => {
  show();
  await screen.findByRole("button", { name: "Prepare filesystem" });
  expect(screen.queryByRole("heading", { name: "Workspace shared disk" })).toBeNull();
  expect(screen.getByText(/DSH uses only this employee private disk/)).toBeTruthy();
  expect(screen.queryByRole("link")).toBeNull();
});

it("shows the shared disk only on the filesystem page", () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}>
    <QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId="agent" includeSharedDisk /></QueryClientProvider>
  </I18nProvider>);
  expect(screen.getByRole("heading", { name: "Workspace shared disk" })).toBeTruthy();
  expect(screen.getByText(/not a DSH setting/)).toBeTruthy();
  expect(screen.getAllByRole("link").map((link) => link.getAttribute("href"))).toEqual(["/workspace/files"]);
});

it("prepares storage only after a user action and exposes no native entry", async () => {
  calls.ensure.mockImplementation(async () => { calls.get.mockResolvedValue(ready); return ready; });
  show();
  const button = await screen.findByRole("button", { name: "Prepare filesystem" });
  await waitFor(() => expect(button).not.toBeDisabled());
  expect(calls.ensure).not.toHaveBeenCalled();
  await userEvent.click(button);
  await screen.findByText("Storage is ready");
  expect(calls.ensure).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.queryByRole("link")).toBeNull();
});

it("does not start a sandbox when storage is already ready", async () => {
  calls.get.mockResolvedValue(ready);
  show();
  await screen.findByText("Storage is ready");
  expect(screen.queryByRole("button")).toBeNull();
  expect(calls.ensure).not.toHaveBeenCalled();
});

it("reconciles an uncertain preparation without automatically retrying the write", async () => {
  calls.ensure.mockRejectedValue(new Error("response lost"));
  show();
  const button = await screen.findByRole("button", { name: "Prepare filesystem" });
  await waitFor(() => expect(button).not.toBeDisabled());
  await userEvent.click(button);
  await screen.findByRole("alert");
  expect(calls.ensure).toHaveBeenCalledTimes(1);
  expect(calls.get.mock.calls.length).toBeGreaterThan(1);
});
