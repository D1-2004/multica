// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../../locales/en/agents.json";
import enCommon from "../../../locales/en/common.json";
import { DshHomeTab } from "./dsh-home-tab";

const calls = vi.hoisted(() => ({ get: vi.fn(), ensure: vi.fn(), entry: vi.fn(), profile: vi.fn(), prepareProfile: vi.fn(), navigate: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getDSHProfile: (...args: unknown[]) => calls.profile(...args),
  prepareDSHProfile: (...args: unknown[]) => calls.prepareProfile(...args),
  getDSHHome: (...args: unknown[]) => calls.get(...args),
  ensureDSHHome: (...args: unknown[]) => calls.ensure(...args),
  issueDSHNativeEntry: (...args: unknown[]) => calls.entry(...args),
} }));
const missing = { provisioned: false, state: "unprovisioned", step: 0, generation: 0, sandboxId: "" };
const ready = { provisioned: true, state: "offline", step: 6, generation: 0, sandboxId: "" };
const clients: QueryClient[] = [];
beforeEach(() => { vi.resetAllMocks(); calls.get.mockResolvedValue(missing); calls.profile.mockResolvedValue({state: "applied", current: true, builds: []}); vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { calls.navigate(this.href); }); });
afterEach(() => { vi.restoreAllMocks(); cleanup(); clients.forEach((c) => c.clear()); clients.length = 0; });

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  render(<I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}>
    <QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId="agent" /></QueryClientProvider>
  </I18nProvider>);
  return client;
}


it("opens from one action after storage and profile preparation without caching credentials", async () => {
  calls.ensure.mockImplementation(async () => { calls.get.mockResolvedValue(ready); return ready; });
  const entryUrl = "https://pre-fde-workbench.dingtalk.com/api/dsh-native/ui/access/_multica/open#entry=dnge_fixture";
  calls.entry.mockResolvedValue({ accessId: "access", entryUrl, expiresAt: new Date(Date.now()+60000).toISOString() });
  const client = show();
  const button = await screen.findByRole("button", { name: "Open native DSH" });
  await waitFor(() => expect(button).not.toBeDisabled());
  expect(calls.ensure).not.toHaveBeenCalled();
  await userEvent.click(button);
  await waitFor(() => expect(calls.navigate).toHaveBeenCalledWith(entryUrl));
  expect(calls.ensure).toHaveBeenCalledTimes(1);
  expect(calls.prepareProfile).toHaveBeenCalledTimes(1);
  expect(calls.entry).toHaveBeenCalledTimes(1);
  expect(JSON.stringify(client.getMutationCache().getAll().map((m) => m.state.data))).not.toContain("dnge_");
  expect(screen.queryByRole("link")).toBeNull();
});

it("waits for the actual plugin application before opening and shows progress", async () => {
  calls.get.mockResolvedValue(ready);
  calls.profile.mockResolvedValue({ state: "waiting_for_builds", current: false, builds: [] });
  const client = show();
  const button = await screen.findByRole("button", { name: "Open native DSH" });
  await userEvent.click(button);
  await waitFor(() => expect(calls.prepareProfile).toHaveBeenCalledTimes(1));
  expect(calls.entry).not.toHaveBeenCalled();
  expect(screen.getByRole("button")).toBeDisabled();
  calls.entry.mockResolvedValue({ accessId: "access", entryUrl: "https://pre.example/api/dsh-native/ui/access/", expiresAt: new Date(Date.now()+60000).toISOString() });
  await act(async () => client.setQueryData(["workspace", "ws", "agents", "agent", "dsh-profile"], {state: "applied", current: true, builds: []}));
  await waitFor(() => expect(calls.navigate).toHaveBeenCalledTimes(1));
});

it.each([null, { accessId: "expired", entryUrl: "secret", expiresAt: "2000-01-01T00:00:00Z" }])("does not navigate or replay an unconfirmed entry", async (entry) => {
  calls.get.mockResolvedValue(ready); calls.entry.mockResolvedValue(entry);
  show();
  const button = await screen.findByRole("button", { name: "Open native DSH" });
  await userEvent.click(button);
  await screen.findByRole("alert");
  expect(calls.navigate).not.toHaveBeenCalled();
  expect(calls.entry).toHaveBeenCalledTimes(1);
  expect(screen.getByRole("button")).not.toBeDisabled();
});

it("does not navigate a delayed result after switching employees", async () => {
  calls.get.mockResolvedValue(ready);
  let complete!: (value: unknown) => void;
  calls.entry.mockReturnValue(new Promise((resolve) => { complete = resolve; }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  clients.push(client);
  const page = (agentId: string) => <I18nProvider locale="en" resources={{ en: { agents: enAgents, common: enCommon } }}><QueryClientProvider client={client}><DshHomeTab workspaceId="ws" agentId={agentId} /></QueryClientProvider></I18nProvider>;
  const view = render(page("first"));
  await userEvent.click(await screen.findByRole("button", { name: "Open native DSH" }));
  await waitFor(() => expect(calls.entry).toHaveBeenCalledTimes(1));
  view.rerender(page("second"));
  await act(async () => complete({accessId: "access", entryUrl: "https://pre.example", expiresAt: new Date(Date.now()+60000).toISOString()}));
  expect(calls.navigate).not.toHaveBeenCalled();
});
